package daemon

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/proto"
)

func TestLocalRequestUsesRemainingDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	entered := make(chan time.Duration, 1)
	e := NewEngine(func() Dialer {
		return &fakeDialer{dial: func(ctx context.Context) (net.Conn, error) {
			deadline, _ := ctx.Deadline()
			select {
			case entered <- time.Until(deadline):
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	})
	defer e.Close()
	go handle(context.Background(), b, e)
	start := time.Now()
	proto.Write(a, proto.LocalRequest{Request: proto.Request{Version: proto.Version, Op: "ping"}, DeadlineMS: start.Add(120 * time.Millisecond).UnixMilli()})
	a.SetReadDeadline(start.Add(time.Second))
	var reply proto.Response
	if err := proto.Read(bufio.NewReader(a), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.OK || time.Since(start) > time.Second {
		t.Fatal("remaining deadline ignored")
	}
	if remaining := <-entered; remaining > 125*time.Millisecond {
		t.Fatalf("dial budget=%s", remaining)
	}
}

func TestSIGTERMCancelsPendingDial(t *testing.T) {
	if os.Getenv("TC_DAEMON_HELPER") == "1" {
		dir := os.Getenv("HERDR_TAILCAT_HOME")
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "daemon.sock"), Net: "unix"})
		if err != nil {
			os.Exit(3)
		}
		e := NewEngine(func() Dialer {
			return &fakeDialer{dial: func(ctx context.Context) (net.Conn, error) {
				os.WriteFile(filepath.Join(dir, "dialing"), nil, 0600)
				<-ctx.Done()
				return nil, ctx.Err()
			}}
		})
		runLocal(ctx, cancel, ln, e, time.Minute)
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("/tmp", "tc-term-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSIGTERMCancelsPendingDial$", "-test.outputdir="+dir)
	cmd.Env = append(os.Environ(), "TC_DAEMON_HELPER=1", "HERDR_TAILCAT_HOME="+dir, "HC_SESSION=tc-unit", "XDG_STATE_HOME="+dir+"/s", "XDG_CONFIG_HOME="+dir+"/c", "XDG_RUNTIME_DIR="+dir+"/r")
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	var c net.Conn
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		c, err = net.Dial("unix", filepath.Join(dir, "daemon.sock"))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	proto.Write(c, proto.LocalRequest{Request: proto.Request{Version: proto.Version, Op: "ping"}, DeadlineMS: time.Now().Add(19 * time.Second).UnixMilli()})
	for time.Now().Before(until) {
		if _, err = os.Stat(filepath.Join(dir, "dialing")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("helper never entered dial")
	}
	start := time.Now()
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM delayed by pending dial")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("slow shutdown")
	}
}
