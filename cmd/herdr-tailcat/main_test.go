package main

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/daemon"
	"github.com/zhdsmy/herdr-tailcat/internal/line"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

// The production entry point must translate a failed daemon dial into exactly
// 255, or core would silently perform a second Windows probe.
func TestViaFailureExit255(t *testing.T) {
	if os.Getenv("HERDR_TAILCAT_TEST_HELPER") == "1" {
		s := "tc-work"
		os.Args = []string{"herdr-tailcat", "via", "p1", line.Template(&s)}
		main()
		os.Exit(0)
	}
	dir, e := os.MkdirTemp("/tmp", "tc-cli-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	profile := filepath.Join(dir, "profiles", "p1")
	if e = os.MkdirAll(profile, 0700); e != nil {
		t.Fatal(e)
	}
	if e = store.WriteJSON(filepath.Join(profile, "profile.json"), store.Profile{}); e != nil {
		t.Fatal(e)
	}
	ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(profile, "daemon.sock"), Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	served := make(chan error, 1)
	go func() {
		c, e := ln.AcceptUnix()
		if e != nil {
			served <- e
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		var q proto.LocalRequest
		if e = proto.Read(bufio.NewReaderSize(c, proto.MaxLine), &q); e == nil {
			proto.Fail(c, daemon.ErrNoAnswer)
		}
		served <- e
	}()
	cmd := exec.Command(os.Args[0], "-test.run=^TestViaFailureExit255$")
	cmd.Env = append(os.Environ(), "HERDR_TAILCAT_TEST_HELPER=1", "HERDR_TAILCAT_HOME="+dir, "HC_SESSION=tc-cli", "XDG_STATE_HOME="+dir+"/s", "XDG_CONFIG_HOME="+dir+"/c", "XDG_RUNTIME_DIR="+dir+"/r")
	output, e := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 255 {
		t.Fatalf("want exit 255, got %v", e)
	}
	if string(output) != daemon.ErrNoAnswer.Error()+"\n" {
		t.Fatalf("unexpected diagnostic: %q", output)
	}
	if e = <-served; e != nil {
		t.Fatal(e)
	}
}
