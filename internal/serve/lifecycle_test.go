package serve

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

func fakeCore(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "core")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func startStream(t *testing.T, m *manager, core, peer, session string) (net.Conn, <-chan struct{}) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.handle(context.Background(), b, store.ServerConfig{HerdrCore: core, Exports: []string{"tc-work", "tc-other"}}, peer, time.Second)
	}()
	a.SetDeadline(time.Now().Add(5 * time.Second))
	if err := proto.Write(a, proto.Request{Version: proto.Version, Op: "connect", Session: &session}); err != nil {
		t.Fatal(err)
	}
	var reply proto.Response
	if err := proto.Read(bufio.NewReader(a), &reply); err != nil || !reply.OK {
		t.Fatalf("handshake: %v %+v", err, reply)
	}
	return a, done
}
func echoReady(t *testing.T, c net.Conn) {
	t.Helper()
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil || b[0] != 'x' {
		t.Fatal("not forwarding", err)
	}
}
func waitFinished(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream child was not reaped")
	}
}

func TestPeerSessionReplacement(t *testing.T) {
	core := fakeCore(t, "exec cat")
	for _, tc := range []struct {
		name, p1, s1, p2, s2 string
		replace              bool
	}{
		{"same", "peer-a", "tc-work", "peer-a", "tc-work", true},
		{"different-session", "peer-a", "tc-work", "peer-a", "tc-other", false},
		{"different-peer", "peer-a", "tc-work", "peer-b", "tc-work", false},
		{"missing-key", "", "tc-work", "", "tc-work", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newManager(90*time.Second, redact.Logger(io.Discard))
			a, adone := startStream(t, m, core, tc.p1, tc.s1)
			defer a.Close()
			echoReady(t, a)
			b, bdone := startStream(t, m, core, tc.p2, tc.s2)
			defer b.Close()
			echoReady(t, b)
			if tc.replace {
				var x [1]byte
				if _, err := a.Read(x[:]); err == nil {
					t.Fatal("old stream survived replacement")
				}
				waitFinished(t, adone)
				// The old handler has finished: its release must not erase b.
				c, cdone := startStream(t, m, core, tc.p2, tc.s2)
				echoReady(t, c)
				if _, err := b.Read(x[:]); err == nil {
					t.Fatal("successor was removed by old release")
				}
				waitFinished(t, bdone)
				c.Close()
				waitFinished(t, cdone)
			} else {
				echoReady(t, a)
				a.Close()
				waitFinished(t, adone)
			}
			b.Close()
			waitFinished(t, bdone)
		})
	}
}

func TestServerOutputDoesNotResetClientIdle(t *testing.T) {
	// Give a newly-created executable time to start on macOS before asserting
	// server traffic is present. This changes only the accelerated test timer.
	m := newManager(2*time.Second, redact.Logger(io.Discard))
	core := fakeCore(t, "while :; do printf x; sleep 0.01; done")
	a, done := startStream(t, m, core, "peer-a", "tc-work")
	defer a.Close()
	// Bound the read while leaving margin for the stream's own cancellation.
	a.SetDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	n, err := io.Copy(io.Discard, a)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 || time.Since(start) > 4*time.Second {
		t.Fatal("server traffic reset client idle", n, time.Since(start))
	}
	waitFinished(t, done)
}

func TestClientIdleConfig(t *testing.T) {
	for _, s := range []string{"0s", "-1s", "29s", "bad"} {
		if _, err := clientIdle(store.ServerConfig{ClientIdleTimeout: s}); err == nil {
			t.Fatal("invalid setting accepted", s)
		}
	}
	for _, tc := range []struct {
		setting string
		want    time.Duration
	}{{"", 90 * time.Second}, {"30s", 30 * time.Second}, {"90s", 90 * time.Second}} {
		d, err := clientIdle(store.ServerConfig{ClientIdleTimeout: tc.setting})
		if err != nil || d != tc.want {
			t.Fatal(tc.setting, d, err)
		}
	}
}

func TestInvalidIdleRejectsBeforeStart(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "server")
	if err := store.WriteJSON(filepath.Join(dir, "config.json"), store.ServerConfig{HerdrCore: "/unused/core", Exports: []string{"tc-work"}, ClientIdleTimeout: "29s"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "allowed_clients"), []byte("nodekey:"+strings.Repeat("01", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, _, err := Prepare(home, redact.Logger(io.Discard), func(*tailcat.Server) error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "client_idle_timeout") || called {
		t.Fatal("invalid setting reached Start", err)
	}
}
