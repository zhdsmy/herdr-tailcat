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
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

type peerConn struct {
	net.Conn
	peer string
}
type capacityClient struct {
	net.Conn
	reader *bufio.Reader
	done   <-chan struct{}
}

func capacityFixture(t *testing.T) (*manager, func(net.Conn)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := newManager(90*time.Second, redact.Logger(io.Discard))
	m.sem = make(chan struct{}, 1)
	m.replacements = make(chan struct{}, replacementSlots)
	m.allowed = map[string]bool{"peer-a": true, "peer-b": true}
	cfg := store.ServerConfig{HerdrCore: fakeCore(t, "exec cat"), Exports: []string{"tc-work", "tc-other"}, MaxConns: 1}
	return m, m.handler(ctx, cfg, func(c net.Conn) string { return c.(*peerConn).peer })
}
func capacityPipe(t *testing.T, handler func(net.Conn), peer string) *capacityClient {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); handler(&peerConn{b, peer}) }()
	c := &capacityClient{a, bufio.NewReaderSize(a, proto.MaxLine), done}
	c.SetDeadline(time.Now().Add(8 * time.Second))
	t.Cleanup(func() { c.Close(); waitFinished(t, done) })
	return c
}
func capacityRequest(t *testing.T, c *capacityClient, session string) proto.Response {
	t.Helper()
	// Rejections precede request reads. Keep both directions live, as TCP does.
	written := make(chan error, 1)
	go func() {
		written <- proto.Write(c, proto.Request{Version: proto.Version, Op: "connect", Session: &session})
	}()
	var reply proto.Response
	if err := proto.Read(c.reader, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.OK {
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	} else {
		c.Close()
		<-written
	}
	return reply
}
func waitCapacity(t *testing.T, m *manager, main, replacement int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(m.sem) != main || len(m.replacements) != replacement {
		if time.Now().After(deadline) {
			t.Fatalf("main=%d replacement=%d want %d/%d", len(m.sem), len(m.replacements), main, replacement)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestCapacityReplacementUsesHandler(t *testing.T) {
	m, handler := capacityFixture(t)
	first := capacityPipe(t, handler, "peer-a")
	if r := capacityRequest(t, first, "tc-work"); !r.OK {
		t.Fatal(r)
	}
	echoReady(t, first)
	second := capacityPipe(t, handler, "peer-a")
	if r := capacityRequest(t, second, "tc-work"); !r.OK {
		t.Fatal(r)
	}
	waitFinished(t, first.done)
	var b [1]byte
	if _, err := first.Read(b[:]); err == nil {
		t.Fatal("old stream survived")
	}
	echoReady(t, second)
	waitCapacity(t, m, 1, 0)
	for _, peer := range []string{"peer-b", "unknown", ""} {
		c := capacityPipe(t, handler, peer)
		if r := capacityRequest(t, c, "tc-work"); r.OK || r.Error != "server busy" {
			t.Fatal(peer, r)
		}
	}
	c := capacityPipe(t, handler, "peer-a")
	if r := capacityRequest(t, c, "tc-other"); r.OK || r.Error != "server busy" {
		t.Fatal(r)
	}
	waitFinished(t, c.done)
	waitCapacity(t, m, 1, 0)
	m.mu.Lock()
	n := len(m.current)
	m.mu.Unlock()
	if n != 1 {
		t.Fatal("extra resident stream", n)
	}
	// An active peer removed from the loaded policy cannot use an extra token.
	m.mu.Lock()
	m.allowed["peer-a"] = false
	m.mu.Unlock()
	denied := capacityPipe(t, handler, "peer-a")
	if r := capacityRequest(t, denied, "tc-work"); r.Error != "server busy" {
		t.Fatal(r)
	}
	second.Close()
	waitFinished(t, second.done)
	waitCapacity(t, m, 0, 0)
}
func TestReplacementBudgetReturnsBusy(t *testing.T) {
	m, handler := capacityFixture(t)
	first := capacityPipe(t, handler, "peer-a")
	if r := capacityRequest(t, first, "tc-work"); !r.OK {
		t.Fatal(r)
	}
	echoReady(t, first)
	// Four concurrent, stalled handshakes occupy the whole replacement budget.
	var stalled []*capacityClient
	for range replacementSlots {
		stalled = append(stalled, capacityPipe(t, handler, "peer-a"))
	}
	waitCapacity(t, m, 1, replacementSlots)
	overflow := capacityPipe(t, handler, "peer-a")
	var r proto.Response
	if err := proto.Read(overflow.reader, &r); err != nil || r.Error != "server busy" {
		t.Fatal(err, r)
	}
	// Release all handshakes concurrently. They either replace the exact stream or
	// receive busy while its predecessor retires. Every admission must be returned.
	var wg sync.WaitGroup
	for _, c := range stalled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session := "tc-work"
			go proto.Write(c, proto.Request{Version: proto.Version, Op: "connect", Session: &session})
			var reply proto.Response
			if err := proto.Read(c.reader, &reply); err != nil {
				t.Error(err)
			} else if !reply.OK && reply.Error != "server busy" {
				t.Error(reply)
			}
			c.Close()
		}()
	}
	wg.Wait()
	first.Close()
	waitFinished(t, first.done)
	for _, c := range stalled {
		waitFinished(t, c.done)
	}
	waitCapacity(t, m, 0, 0)
	m.mu.Lock()
	n := len(m.current)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("leases leaked", n)
	}
}
func TestHandlerPreservesBufferedStreamBytes(t *testing.T) {
	_, handler := capacityFixture(t)
	c := capacityPipe(t, handler, "peer-a")
	go io.WriteString(c, "{\"tailcat_herdr\":1,\"op\":\"connect\",\"session\":\"tc-work\"}\nraw")
	var reply proto.Response
	if err := proto.Read(c.reader, &reply); err != nil || !reply.OK {
		t.Fatal(err, reply)
	}
	b := make([]byte, 3)
	if _, err := io.ReadFull(c.reader, b); err != nil || string(b) != "raw" {
		t.Fatal("buffered stream bytes lost", err, string(b))
	}
}
