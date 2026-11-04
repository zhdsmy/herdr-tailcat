package serve

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

func clientIdle(cfg store.ServerConfig) (time.Duration, error) {
	if cfg.ClientIdleTimeout == "" {
		return 90 * time.Second, nil
	}
	d, err := time.ParseDuration(cfg.ClientIdleTimeout)
	if err != nil || d < 30*time.Second {
		return 0, errors.New("client_idle_timeout must be a duration of at least 30s")
	}
	return d, nil
}

type streamKey struct{ peer, session string }
type lease struct {
	cancel    context.CancelFunc
	done      chan struct{}
	admitted  *admission
	committed bool
	retiring  *lease
}

// admission owns exactly one main or temporary capacity token. All ownership
// changes and lease publication happen under manager.mu.
type admission struct {
	main, replacement bool
}

const replacementSlots = 4

type manager struct {
	mu           sync.Mutex
	current      map[streamKey]*lease
	pending      map[streamKey]*lease
	sem          chan struct{}
	replacements chan struct{}
	allowed      map[string]bool
	idle         time.Duration
	logger       *log.Logger
	sequence     atomic.Uint64
	active       atomic.Int64
}

func newManager(idle time.Duration, logger *log.Logger) *manager {
	return &manager{current: make(map[streamKey]*lease), pending: make(map[streamKey]*lease), idle: idle, logger: logger}
}

// ready excludes a lease still writing its handshake or retiring a predecessor.
func (l *lease) ready() bool {
	if !l.committed {
		return false
	}
	if l.retiring == nil {
		return true
	}
	select {
	case <-l.retiring.done:
		l.retiring = nil
		return true
	default:
		return false
	}
}

func (m *manager) canReplace(peer, session string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.current[streamKey{peer, session}]
	return old != nil && old.ready() && old.admitted != nil && old.admitted.main
}

// install reserves one key before writing success. current still names the old
// stream, which remains live. A second reservation for this key receives busy.
// With temporary admission, move the main token to the reservation and lend the
// temporary token to the predecessor. Even if it exits during the reply, the
// reservation retains capacity and can always commit an acknowledged request.
func (m *manager) install(peer, session string, cancel context.CancelFunc, a *admission) (*lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := streamKey{peer, session}
	if peer != "" && m.pending[k] != nil {
		return nil, false
	}
	old := m.current[k]
	if a != nil && a.replacement && (old == nil || !old.ready() || old.admitted == nil || !old.admitted.main) {
		return nil, false
	}
	own := &lease{cancel: cancel, done: make(chan struct{}), admitted: a, retiring: old}
	if a != nil && a.replacement {
		old.admitted.main, old.admitted.replacement = false, true
		a.main, a.replacement = true, false
	}
	if peer != "" {
		m.pending[k] = own
	}
	return own, true
}

// commit publishes only after success was written. No capacity acquisition can
// fail here. Cancellation of the parent still tears down both handlers normally.
func (m *manager) commit(peer, session string, l *lease) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.committed = true
	if peer != "" {
		k := streamKey{peer, session}
		delete(m.pending, k)
		m.current[k] = l
	}
	if l.retiring != nil {
		l.retiring.cancel()
	}
}
func (m *manager) finish(peer, session string, l *lease) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := streamKey{peer, session}
	if !l.committed {
		// Failed reply: restore the predecessor's main token if it has not freed
		// its temporary token yet. Otherwise free() returns our reserved main token.
		if old := l.retiring; old != nil && old.admitted != nil && old.admitted.replacement {
			old.admitted.main, old.admitted.replacement = true, false
			l.admitted.main, l.admitted.replacement = false, true
		}
		if m.pending[k] == l {
			delete(m.pending, k)
		}
	}
	if peer != "" && m.current[k] == l {
		delete(m.current, k)
	}
	close(l.done)
}
func (m *manager) free(a *admission) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.main {
		<-m.sem
		a.main = false
	}
	if a.replacement {
		<-m.replacements
		a.replacement = false
	}
}
func (m *manager) admit(peer string) *admission {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case m.sem <- struct{}{}:
		return &admission{main: true}
	default:
	}
	// The peer selector is authenticated by Tailcat. Recheck the loaded allowlist
	// before granting any capacity beyond max_conns. Empty selectors never qualify.
	if peer == "" || !m.allowed[peer] {
		return nil
	}
	for k, l := range m.current {
		if k.peer == peer && l.ready() && l.admitted != nil && l.admitted.main {
			select {
			case m.replacements <- struct{}{}:
				return &admission{replacement: true}
			default:
				return nil
			}
		}
	}
	return nil
}

type activity struct {
	mu   sync.Mutex
	last time.Time
	max  time.Duration
}

func (a *activity) measure(now time.Time) (time.Duration, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	gap := now.Sub(a.last)
	if gap > a.max {
		a.max = gap
	}
	return gap, a.max
}
func (a *activity) received() {
	now := time.Now()
	a.mu.Lock()
	gap := now.Sub(a.last)
	if gap > a.max {
		a.max = gap
	}
	a.last = now
	a.mu.Unlock()
}

type activeReader struct {
	io.Reader
	a *activity
}

func (r activeReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if n > 0 {
		r.a.received()
	}
	return n, err
}

func (m *manager) watch(ctx context.Context, cancel context.CancelFunc, a *activity, id uint64) {
	tick := time.NewTicker(min(m.idle/4, time.Second))
	defer tick.Stop()
	lastLog := time.Now()
	defer func() {
		_, max := a.measure(time.Now())
		m.logger.Printf("stream=%d client_silence_max_seconds=%.3f", id, max.Seconds())
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			gap, max := a.measure(now)
			if now.Sub(lastLog) >= 15*time.Second {
				m.logger.Printf("stream=%d client_silence_max_seconds=%.3f", id, max.Seconds())
				lastLog = now
			}
			if gap >= m.idle {
				m.logger.Printf("stream=%d client idle timeout", id)
				cancel()
				return
			}
		}
	}
}
