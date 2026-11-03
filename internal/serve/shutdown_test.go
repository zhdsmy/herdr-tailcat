package serve

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

type observedConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *observedConn) Close() error { c.closed.Store(true); return c.Conn.Close() }

func TestShutdownClosesEveryStreamBeforeDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newManager(90*time.Second, redact.Logger(io.Discard))
	core := fakeCore(t, "exec cat")
	var wg sync.WaitGroup
	var streams []*observedConn
	for range 3 {
		client, server := net.Pipe()
		t.Cleanup(func() { client.Close() })
		observed := &observedConn{Conn: server}
		streams = append(streams, observed)
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.handle(ctx, observed, store.ServerConfig{HerdrCore: core, Exports: []string{"tc-work"}}, "", time.Second)
		}()
		client.SetDeadline(time.Now().Add(2 * time.Second))
		session := "tc-work"
		if err := proto.Write(client, proto.Request{Version: proto.Version, Op: "connect", Session: &session}); err != nil {
			t.Fatal(err)
		}
		var reply proto.Response
		if err := proto.Read(bufio.NewReader(client), &reply); err != nil || !reply.OK {
			t.Fatal(err, reply)
		}
		echoReady(t, client)
	}
	start := time.Now()
	cancel()
	drained := false
	closed := false
	shutdown(&wg, func(ctx context.Context) error {
		for _, c := range streams {
			if !c.closed.Load() {
				t.Error("stack drained before stream close")
			}
		}
		drained = true
		return nil
	}, func() error {
		if !drained {
			t.Error("stack closed before drain")
		}
		closed = true
		return nil
	})
	if !closed || time.Since(start) >= 10*time.Second {
		t.Fatal("shutdown exceeded bound", time.Since(start))
	}
}

func TestShutdownBoundsStalledHandlersAndDrain(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Done()
	start := time.Now()
	closed := false
	shutdown(&wg, func(ctx context.Context) error {
		if time.Since(start) < 6*time.Second {
			t.Error("did not wait for handler budget")
		}
		<-ctx.Done()
		return ctx.Err()
	}, func() error { closed = true; return nil })
	elapsed := time.Since(start)
	if !closed || elapsed < 9*time.Second || elapsed >= 10*time.Second {
		t.Fatal("unbounded shutdown", elapsed)
	}
}
