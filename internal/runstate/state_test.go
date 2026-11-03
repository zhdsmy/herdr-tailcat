package runstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockAndIdentity(t *testing.T) {
	dir := t.TempDir()
	lock, e := Acquire(dir, "serve")
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if f, e := Acquire(dir, "serve"); e == nil {
		f.Close()
		t.Fatal("second lock succeeded")
	}
	s, e := New()
	if e != nil {
		t.Fatal(e)
	}
	s.Digest = "digest"
	s.Streams = 2
	if e = Write(dir, "serve", s); e != nil {
		t.Fatal(e)
	}
	got, running := Read(dir, "serve")
	if !running || got.PID != os.Getpid() || got.Streams != 2 {
		t.Fatalf("invalid live status: %+v %v", got, running)
	}
	info, e := os.Stat(filepath.Join(dir, "serve.state.json"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state is not private", e)
	}
	s.Identity += "different process"
	Write(dir, "serve", s)
	if _, running = Read(dir, "serve"); running {
		t.Fatal("stale PID identity accepted")
	}
	Write(dir, "serve", got)
	lock.Close()
	if _, running = Read(dir, "serve"); running {
		t.Fatal("unlocked state accepted")
	}
}
