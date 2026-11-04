package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDialer struct {
	dial    func(context.Context) (net.Conn, error)
	ping    func(context.Context) error
	closed  atomic.Int32
	onClose func()
}

func (f *fakeDialer) Dial(c context.Context) (net.Conn, error) { return f.dial(c) }
func (f *fakeDialer) Ping(c context.Context) error {
	if f.ping != nil {
		return f.ping(c)
	}
	return nil
}
func (f *fakeDialer) Close() error {
	f.closed.Add(1)
	if f.onClose != nil {
		f.onClose()
	}
	return nil
}
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

func TestCallerCancellationPreservesClient(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			var once sync.Once
			var count atomic.Int32
			old := &fakeDialer{dial: func(ctx context.Context) (net.Conn, error) {
				once.Do(func() { close(entered) })
				<-ctx.Done()
				return nil, ctx.Err()
			}}
			e := NewEngine(func() Dialer { count.Add(1); return old })
			defer e.Close()
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "cancel" {
				ctx, cancel = context.WithCancel(context.Background())
			} else {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := e.Dial(ctx); done <- err }()
			<-entered
			if mode == "cancel" {
				cancel()
			}
			if err := <-done; !errors.Is(err, ErrNoAnswer) {
				t.Fatal(err)
			}
			if count.Load() != 1 || old.closed.Load() != 0 {
				t.Fatal("caller cancellation rebuilt healthy client", count.Load(), old.closed.Load())
			}
		})
	}
}

func TestRebuildOutlivesCanceledWaiter(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var count atomic.Int32
	old := &fakeDialer{dial: func(context.Context) (net.Conn, error) { return nil, errors.New("transport failure") }}
	fresh := &fakeDialer{dial: successful, ping: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	e := NewEngine(func() Dialer {
		if count.Add(1) == 1 {
			return old
		}
		return fresh
	})
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		c, err := e.Dial(ctx)
		if c != nil {
			c.Close()
		}
		first <- err
	}()
	<-entered
	cancel()
	if err := <-first; !errors.Is(err, ErrNoAnswer) {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() { second <- e.rebuild(context.Background(), 0) }()
	close(release)
	if err := <-second; err != nil {
		t.Fatal("one waiter canceled shared recovery", err)
	}
	if count.Load() != 2 {
		t.Fatal(count.Load())
	}
}

func TestCallerDeadlineKeepsExistingStream(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go io.Copy(b, b)
	var calls atomic.Int32
	var factories atomic.Int32
	old := &fakeDialer{dial: func(ctx context.Context) (net.Conn, error) {
		if calls.Add(1) == 1 {
			return a, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, onClose: func() { b.Close() }}
	e := NewEngine(func() Dialer { factories.Add(1); return old })
	defer e.Close()
	live, err := e.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.Dial(ctx); !errors.Is(err, ErrNoAnswer) {
		t.Fatal(err)
	}
	live.SetDeadline(time.Now().Add(time.Second))
	if _, err := live.Write([]byte("x")); err != nil {
		t.Fatal("existing stream closed", err)
	}
	var echo [1]byte
	if _, err := io.ReadFull(live, echo[:]); err != nil || echo[0] != 'x' {
		t.Fatal("existing stream lost", err)
	}
	if factories.Load() != 1 || old.closed.Load() != 0 {
		t.Fatal("healthy client rebuilt")
	}
}
