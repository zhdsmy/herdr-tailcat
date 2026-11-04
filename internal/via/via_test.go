package via

import (
	"context"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A slow startup consumes the same budget as the following handshake.
func TestStartupAndHandshakeShareNineteenSeconds(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tc-budget-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("HERDR_TAILCAT_HOME", dir)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "fake.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		c, err := ln.AcceptUnix()
		if err == nil {
			defer c.Close()
			<-done
		}
	}()
	start := time.Now()
	err = run("p1", "echo ok", func(ctx context.Context, _, _ string) (*net.UnixConn, error) {
		select {
		case <-time.After(2800 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return net.DialUnix("unix", nil, ln.Addr().(*net.UnixAddr))
	})
	close(done)
	if err == nil || time.Since(start) > 19500*time.Millisecond || time.Since(start) < 18*time.Second {
		t.Fatalf("elapsed=%s err=%v", time.Since(start), err)
	}
}

func TestConnectDaemonHonorsLifecycle(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "tc-start-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	dir := filepath.Join(home, "profiles", "p1")
	if err := store.WriteJSON(filepath.Join(dir, "profile.json"), store.Profile{}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "daemon.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan *net.UnixConn, 2)
	go func() {
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	lock, err := store.ProfileLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c, err := connectDaemon(ctx, dir, "p1")
	if c != nil {
		c.Close()
	}
	if err == nil {
		t.Error("connected while deletion held lifecycle")
	}
	lock.Close()
	select {
	case c := <-accepted:
		c.Close()
		t.Error("canceled startup still dialed socket")
	default:
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err = connectDaemon(ctx, dir, "p1")
	if err != nil {
		t.Fatal("unlocked startup", err)
	}
	c.Close()
	select {
	case c := <-accepted:
		c.Close()
	case <-ctx.Done():
		t.Fatal("no fixture connection")
	}
	// A start queued behind removal must recheck existence, not recreate the
	// profile directory or launch a process with its old identity.
	lock, err = store.ProfileLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if c, err = connectDaemon(ctx, dir, "p1"); err == nil {
		c.Close()
		t.Fatal("missing profile started")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("startup recreated deleted directory", err)
	}
}
