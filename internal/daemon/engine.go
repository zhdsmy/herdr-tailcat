package daemon

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

var ErrNoAnswer = errors.New("tailcat: server did not answer (offline, or this client key is not allowed)")

// Dialer allows deterministic recovery tests without a network or real keys.
type Dialer interface {
	Dial(context.Context) (net.Conn, error)
	Ping(context.Context) error
	Close() error
}
type Engine struct {
	mu          sync.Mutex
	client      Dialer
	factory     func() Dialer
	generation  uint64
	rebuilding  chan struct{}
	rebuildErr  error
	closed      bool
	DialTimeout time.Duration
}

func NewEngine(factory func() Dialer) *Engine {
	return &Engine{factory: factory, client: factory(), DialTimeout: 8 * time.Second}
}
func (e *Engine) snapshot(ctx context.Context) (Dialer, uint64, error) {
	for {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return nil, 0, ErrNoAnswer
		}
		ch := e.rebuilding
		if ch == nil {
			c, g := e.client, e.generation
			e.mu.Unlock()
			return c, g, nil
		}
		e.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, 0, ErrNoAnswer
		}
	}
}
func (e *Engine) dial(ctx context.Context, c Dialer) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, e.DialTimeout)
	defer cancel()
	return c.Dial(ctx)
}
func (e *Engine) Dial(ctx context.Context) (net.Conn, error) {
	c, g, err := e.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := e.dial(ctx, c)
	if err == nil {
		return conn, nil
	}
	if err = e.rebuild(ctx, g); err != nil {
		return nil, ErrNoAnswer
	}
	c, _, err = e.snapshot(ctx)
	if err != nil {
		return nil, ErrNoAnswer
	}
	conn, err = e.dial(ctx, c)
	if err != nil {
		return nil, ErrNoAnswer
	}
	return conn, nil
}
func (e *Engine) rebuild(ctx context.Context, expected uint64) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrNoAnswer
	}
	if ch := e.rebuilding; ch != nil {
		e.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ErrNoAnswer
		}
		e.mu.Lock()
		err := e.rebuildErr
		e.mu.Unlock()
		return err
	}
	if e.generation != expected {
		err := e.rebuildErr
		e.mu.Unlock()
		return err
	}
	ch := make(chan struct{})
	e.rebuilding = ch
	old := e.client
	e.mu.Unlock()
	_ = old.Close()
	fresh := e.factory()
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := fresh.Ping(pingCtx)
	cancel()
	e.mu.Lock()
	if e.closed {
		_ = fresh.Close()
		err = ErrNoAnswer
	} else {
		e.client = fresh
	}
	e.generation++
	e.rebuildErr = err
	e.rebuilding = nil
	close(ch)
	e.mu.Unlock()
	return err
}
func (e *Engine) Close() { e.mu.Lock(); e.closed = true; c := e.client; e.mu.Unlock(); _ = c.Close() }
