package daemon

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDialer struct {
	dial   func(context.Context) (net.Conn, error)
	ping   func(context.Context) error
	closed atomic.Int32
}

func (f *fakeDialer) Dial(c context.Context) (net.Conn, error) { return f.dial(c) }
func (f *fakeDialer) Ping(c context.Context) error {
	if f.ping != nil {
		return f.ping(c)
	}
	return nil
}
func (f *fakeDialer) Close() error                 { f.closed.Add(1); return nil }
func successful(context.Context) (net.Conn, error) { a, b := net.Pipe(); b.Close(); return a, nil }
func TestTimeoutRebuildRetry(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovered", true: "still-offline"}[fail], func(t *testing.T) {
			var count atomic.Int32
			old := &fakeDialer{dial: func(ctx context.Context) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }}
			fresh := &fakeDialer{dial: successful}
			if fail {
				fresh.dial = func(context.Context) (net.Conn, error) { return nil, errors.New("secret error never forwarded") }
			}
			e := NewEngine(func() Dialer {
				if count.Add(1) == 1 {
					return old
				}
				return fresh
			})
			defer e.Close()
			e.DialTimeout = time.Millisecond
			c, err := e.Dial(context.Background())
			if fail {
				if !errors.Is(err, ErrNoAnswer) {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
			}
			if count.Load() != 2 || old.closed.Load() != 1 {
				t.Fatal("not exactly one rebuild")
			}
		})
	}
}
func TestConcurrentRebuildIsSingle(t *testing.T) {
	const n = 12
	ready := make(chan struct{})
	release := make(chan struct{})
	var entered atomic.Int32
	var count atomic.Int32
	old := &fakeDialer{dial: func(context.Context) (net.Conn, error) {
		if entered.Add(1) == n {
			close(ready)
		}
		<-release
		return nil, errors.New("stale client")
	}}
	fresh := &fakeDialer{dial: successful}
	e := NewEngine(func() Dialer {
		if count.Add(1) == 1 {
			return old
		}
		return fresh
	})
	defer e.Close()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := e.Dial(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			c.Close()
		}()
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("workers did not enter original dial")
	}
	close(release)
	wg.Wait()
	if count.Load() != 2 {
		t.Fatalf("created %d clients, want 2", count.Load())
	}
}
func TestPingFailureIsGeneric(t *testing.T) {
	var n int
	d := &fakeDialer{dial: func(context.Context) (net.Conn, error) { return nil, errors.New("failure") }, ping: func(context.Context) error { return errors.New("private diagnostic") }}
	e := NewEngine(func() Dialer { n++; return d })
	_, err := e.Dial(context.Background())
	if !errors.Is(err, ErrNoAnswer) || n != 2 {
		t.Fatal(err, n)
	}
	e.Close()
}
