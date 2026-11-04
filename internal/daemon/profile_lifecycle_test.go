package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/runstate"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"tailscale.com/types/key"
)

func TestProfileRemovalStartupOrder(t *testing.T) {
	if mode := os.Getenv("TC_LIFECYCLE_CHILD"); mode != "" {
		dir := filepath.Join(os.Getenv("HERDR_TAILCAT_HOME"), "profiles", "p1")
		if mode == "remove" {
			err := store.RemoveProfile(context.Background(), dir, func() error { fmt.Println("stopped"); io.ReadFull(os.Stdin, make([]byte, 1)); return nil })
			if err != nil {
				os.Exit(2)
			}
			return
		}
		err := run("p1", func(store.Profile, key.NodePrivate) *Engine {
			fmt.Println("loaded")
			io.ReadFull(os.Stdin, make([]byte, 1))
			return NewEngine(func() Dialer { return &fakeDialer{dial: successful} })
		})
		if err != nil {
			fmt.Println("missing")
			return
		}
		os.Exit(3)
	}
	home := t.TempDir()
	dir := filepath.Join(home, "profiles", "p1")
	if err := AddProfile(dir, profileAddress()); err != nil {
		t.Fatal(err)
	}
	start := func(mode string) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProfileRemovalStartupOrder$", "daemon", "p1")
		cmd.Env = append(os.Environ(), "TC_LIFECYCLE_CHILD="+mode, "HERDR_TAILCAT_HOME="+home)
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			in.Close()
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		return cmd, in, bufio.NewReader(out)
	}
	rm, release, rmout := start("remove")
	if line, err := rmout.ReadString(10); err != nil || line != "stopped\n" {
		t.Fatal("remove barrier", err, line)
	}
	starter, _, out := start("start")
	next := make(chan string, 1)
	go func() { line, _ := out.ReadString(10); next <- line }()
	var early string
	select {
	case early = <-next:
	case <-time.After(500 * time.Millisecond):
	}
	if early == "loaded\n" {
		t.Error("direct daemon loaded old identity after deletion had stopped daemon")
		starter.Process.Kill()
		starter.Wait()
	}
	io.WriteString(release, "x")
	release.Close()
	if err := rm.Wait(); err != nil {
		t.Fatal(err)
	}
	if early == "" {
		select {
		case early = <-next:
		case <-time.After(4 * time.Second):
			t.Fatal("startup not bounded")
		}
	}
	if early != "loaded\n" {
		if early != "missing\n" {
			t.Error("unexpected startup result", early)
		}
		if err := starter.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("profile remains or was recreated", err)
	}
	if err := AddProfile(dir, profileAddress()); err != nil {
		t.Fatal("recreate", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "daemon.sock")); !os.IsNotExist(err) {
		t.Fatal("old socket in new profile", err)
	}
}

func TestProfileStartupCompletesBeforeRemoval(t *testing.T) {
	if os.Getenv("TC_START_FIRST_CHILD") == "1" {
		err := run("p1", func(store.Profile, key.NodePrivate) *Engine {
			fmt.Println("identity")
			io.ReadFull(os.Stdin, make([]byte, 1))
			return NewEngine(func() Dialer { return &fakeDialer{dial: successful} })
		})
		if err != nil {
			os.Exit(2)
		}
		return
	}
	home, err := os.MkdirTemp("/tmp", "tc-life-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	dir := filepath.Join(home, "profiles", "p1")
	if err := AddProfile(dir, profileAddress()); err != nil {
		t.Fatal(err)
	}
	old, _ := PublicKey(dir)
	cmd := exec.Command(os.Args[0], "-test.run=^TestProfileStartupCompletesBeforeRemoval$", "daemon", "p1")
	cmd.Env = append(os.Environ(), "TC_START_FIRST_CHILD=1", "HERDR_TAILCAT_HOME="+home)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	if line, err := bufio.NewReader(out).ReadString(10); err != nil || line != "identity\n" {
		t.Fatal("startup barrier", err, line)
	}
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.RemoveProfile(context.Background(), dir, func() error {
			close(stopped)
			if _, running := runstate.Read(dir, "daemon"); !running {
				return fmt.Errorf("startup did not publish live state before removal")
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				return err
			}
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			select {
			case err := <-waited:
				return err
			case <-time.After(2 * time.Second):
				return fmt.Errorf("test daemon did not stop")
			}
		})
	}()
	select {
	case <-stopped:
		t.Error("removal bypassed startup lifecycle lock")
	case <-time.After(200 * time.Millisecond):
	}
	io.WriteString(in, "x")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("removal stuck")
	}
	if err := AddProfile(dir, profileAddress()); err != nil {
		t.Fatal(err)
	}
	fresh, _ := PublicKey(dir)
	if fresh == old {
		t.Fatal("new import reused old identity")
	}
	if _, err := os.Stat(filepath.Join(dir, "daemon.sock")); !os.IsNotExist(err) {
		t.Fatal("old socket survived recreation", err)
	}
}
