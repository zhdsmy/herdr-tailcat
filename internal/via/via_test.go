package via

import (
	"context"
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
