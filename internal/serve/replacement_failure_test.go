package serve

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

// The reply barrier proves that the replacement reached the success write.
// Release it with either a closed pipe or an expired deadline.
type replyBarrier struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *replyBarrier) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.Write(p)
}
func TestFailedReplacementPreservesOldStream(t *testing.T) {
	for _, capacity := range []int{1, 2} {
		for _, failure := range []string{"disconnect", "timeout"} {
			t.Run(failure+string(rune('0'+capacity)), func(t *testing.T) {
				m, handler := capacityFixture(t)
				m.sem = make(chan struct{}, capacity)
				old := capacityPipe(t, handler, "peer-a")
				if !capacityRequest(t, old, "tc-work").OK {
					t.Fatal("old handshake")
				}
				echoReady(t, old)
				a, b := net.Pipe()
				defer a.Close()
				barrier := &replyBarrier{Conn: b, entered: make(chan struct{}), release: make(chan struct{})}
				done := make(chan struct{})
				go func() { defer close(done); handler(&peerConn{barrier, "peer-a"}) }()
				s := "tc-work"
				if err := proto.Write(a, proto.Request{Version: 1, Op: "connect", Session: &s}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-barrier.entered:
				case <-time.After(2 * time.Second):
					t.Fatal("no reply")
				}
				if failure == "disconnect" {
					a.Close()
				} else {
					b.SetWriteDeadline(time.Now().Add(-time.Second))
				}
				close(barrier.release)
				waitFinished(t, done)
				echoReady(t, old)
				waitCapacity(t, m, 1, 0)
			})
		}
	}
}

func TestReplacementReservationSurvivesOldExit(t *testing.T) {
	m, handler := capacityFixture(t)
	old := capacityPipe(t, handler, "peer-a")
	if !capacityRequest(t, old, "tc-work").OK {
		t.Fatal("old handshake")
	}
	echoReady(t, old)
	a, b := net.Pipe()
	defer a.Close()
	barrier := &replyBarrier{Conn: b, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); handler(&peerConn{barrier, "peer-a"}) }()
	s := "tc-work"
	proto.Write(a, proto.Request{Version: 1, Op: "connect", Session: &s})
	select {
	case <-barrier.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
	old.Close()
	waitFinished(t, old.done)
	close(barrier.release)
	var r proto.Response
	if err := proto.Read(bufio.NewReader(a), &r); err != nil || !r.OK {
		t.Fatal(err, r)
	}
	echoReady(t, a)
	a.Close()
	waitFinished(t, done)
	waitCapacity(t, m, 0, 0)
}

func TestReplacementParentCancellationReleasesReservation(t *testing.T) {
	m, _ := capacityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cfg := store.ServerConfig{HerdrCore: fakeCore(t, "exec cat"), Exports: []string{"tc-work"}}
	handler := m.handler(ctx, cfg, func(net.Conn) string { return "peer-a" })
	old := capacityPipe(t, handler, "peer-a")
	if !capacityRequest(t, old, "tc-work").OK {
		t.Fatal("old handshake")
	}
	echoReady(t, old)
	a, b := net.Pipe()
	defer a.Close()
	done := make(chan struct{})
	go func() { defer close(done); handler(b) }()
	s := "tc-work"
	proto.Write(a, proto.Request{Version: 1, Op: "connect", Session: &s})
	cancel()
	io.Copy(io.Discard, a)
	waitFinished(t, done)
	waitFinished(t, old.done)
	waitCapacity(t, m, 0, 0)
}

func TestConcurrentReservationHasOneWinner(t *testing.T) {
	for _, capacity := range []int{1, 3} {
		t.Run(string(rune('0'+capacity)), func(t *testing.T) {
			m, handler := capacityFixture(t)
			m.sem = make(chan struct{}, capacity)
			old := capacityPipe(t, handler, "peer-a")
			if !capacityRequest(t, old, "tc-work").OK {
				t.Fatal("old handshake")
			}
			echoReady(t, old)
			a, b := net.Pipe()
			defer a.Close()
			barrier := &replyBarrier{Conn: b, entered: make(chan struct{}), release: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); handler(&peerConn{barrier, "peer-a"}) }()
			s := "tc-work"
			proto.Write(a, proto.Request{Version: 1, Op: "connect", Session: &s})
			select {
			case <-barrier.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("no reservation")
			}
			loser := capacityPipe(t, handler, "peer-a")
			if r := capacityRequest(t, loser, "tc-work"); r.OK || r.Error != "server busy" {
				t.Fatal("second reservation acknowledged", r)
			}
			echoReady(t, old)
			close(barrier.release)
			var r proto.Response
			if err := proto.Read(bufio.NewReader(a), &r); err != nil || !r.OK {
				t.Fatal(err, r)
			}
			waitFinished(t, old.done)
			echoReady(t, a)
			a.Close()
			waitFinished(t, done)
			waitFinished(t, loser.done)
			waitCapacity(t, m, 0, 0)
			m.mu.Lock()
			n := len(m.pending) + len(m.current)
			m.mu.Unlock()
			if n != 0 {
				t.Fatal("lease leaked", n)
			}
		})
	}
}
func TestReservationAbortAfterOldExit(t *testing.T) {
	m, handler := capacityFixture(t)
	old := capacityPipe(t, handler, "peer-a")
	if !capacityRequest(t, old, "tc-work").OK {
		t.Fatal("old handshake")
	}
	echoReady(t, old)
	a, b := net.Pipe()
	defer a.Close()
	barrier := &replyBarrier{Conn: b, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); handler(&peerConn{barrier, "peer-a"}) }()
	s := "tc-work"
	proto.Write(a, proto.Request{Version: 1, Op: "connect", Session: &s})
	select {
	case <-barrier.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no reservation")
	}
	old.Close()
	waitFinished(t, old.done)
	a.Close()
	close(barrier.release)
	waitFinished(t, done)
	waitCapacity(t, m, 0, 0)
	m.mu.Lock()
	n := len(m.pending) + len(m.current)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("lease leaked", n)
	}
}
