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
type lease struct{ cancel context.CancelFunc }
type manager struct {
	mu       sync.Mutex
	current  map[streamKey]*lease
	idle     time.Duration
	logger   *log.Logger
	sequence atomic.Uint64
	active   atomic.Int64
}

func newManager(idle time.Duration, logger *log.Logger) *manager {
	return &manager{current: make(map[streamKey]*lease), idle: idle, logger: logger}
}

// install is called only after ok:true has been sent. Release compares identity
// so a superseded handler cannot remove its successor.
func (m *manager) install(peer, session string, cancel context.CancelFunc) func() {
	if peer == "" {
		m.logger.Print("stream peer key unavailable; replacement disabled")
		return func() {}
	}
	k := streamKey{peer, session}
	own := &lease{cancel}
	m.mu.Lock()
	old := m.current[k]
	m.current[k] = own
	m.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	return func() {
		m.mu.Lock()
		if m.current[k] == own {
			delete(m.current, k)
		}
		m.mu.Unlock()
	}
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
