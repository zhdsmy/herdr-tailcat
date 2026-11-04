package via

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/line"
	"github.com/zhdsmy/herdr-tailcat/internal/peer"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"github.com/zhdsmy/herdr-tailcat/internal/stream"
)

func Run(id, text string) error {
	return run(id, text, connectDaemon)
}

func run(id, text string, connect func(context.Context, string, string) (*net.UnixConn, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 19*time.Second)
	defer cancel()
	if !line.ValidName(id) {
		return errors.New("herdr-tailcat: invalid profile id")
	}
	parsed := line.Parse(text)
	if parsed.Kind == line.Unsupported {
		return errors.New(parsed.Reason)
	}
	home, e := store.Home()
	if e != nil {
		return e
	}
	dir := filepath.Join(home, "profiles", id)
	var traced sync.Once
	trace := func(reason string) {
		if os.Getenv("HERDR_TAILCAT_TRACE_EXIT") != "1" {
			return
		}
		traced.Do(func() {
			f, err := os.OpenFile(filepath.Join(dir, "via-exit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err == nil {
				defer f.Close()
				fmt.Fprintf(f, "pid=%d reason=%s\n", os.Getpid(), reason)
			}
		})
	}
	parent := os.Getppid()
	peer.ParentDeath()
	if os.Getppid() != parent {
		return errors.New("herdr-tailcat: parent exited")
	}
	c, e := connect(ctx, dir, id)
	if e != nil {
		return e
	}
	defer c.Close()
	if _, e = peer.UID(c); e != nil {
		return e
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-stop:
				trace("signal")
				c.Close()
				return
			case <-ticker.C:
				if os.Getppid() != parent {
					trace("parent-changed")
					c.Close()
					return
				}
			}
		}
	}()
	until, _ := ctx.Deadline()
	_ = c.SetDeadline(until)
	op := "connect"
	if parsed.Kind == line.EchoOK {
		op = "ping"
	}
	if e = proto.Write(c, proto.LocalRequest{Request: proto.Request{Version: proto.Version, Op: op, Session: parsed.Session}, DeadlineMS: until.UnixMilli()}); e != nil {
		return errors.New("herdr-tailcat: daemon request failed")
	}
	r := bufio.NewReaderSize(c, proto.MaxLine)
	var reply proto.Response
	if e = proto.Read(r, &reply); e != nil || reply.Version != proto.Version {
		return errors.New("herdr-tailcat: daemon handshake failed")
	}
	if !reply.OK {
		return errors.New(reply.Error)
	}
	if parsed.Kind == line.EchoOK {
		fmt.Fprintln(os.Stdout, "ok")
		return nil
	}
	_ = c.SetDeadline(time.Time{})
	input := make(chan error, 1)
	output := make(chan error, 1)
	go func() {
		_, e := io.Copy(c, os.Stdin)
		if e == nil {
			trace("stdin-eof")
		} else {
			trace("stdin-error")
		}
		stream.HalfClose(c)
		input <- e
	}()
	go func() { _, e := io.Copy(os.Stdout, r); output <- e }()
	select {
	case e = <-output:
		return e
	case e = <-input:
		if e != nil {
			return errors.New("herdr-tailcat: core input closed")
		}
		select {
		case e = <-output:
			return e
		case <-time.After(5 * time.Second):
			return nil
		}
	}
}
func connectDaemon(ctx context.Context, dir, id string) (*net.UnixConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Stable startup precedes lifecycle. Never await a child while holding
	// lifecycle: the child must acquire it before loading any identity.
	startup, e := store.Lock(ctx, store.ProfileLockPath(dir, "startup"))
	if e != nil {
		return nil, errors.New("herdr-tailcat: daemon startup lock unavailable or timed out")
	}
	defer startup.Close()
	lifecycle, e := store.ProfileLock(ctx, dir)
	if e != nil {
		return nil, errors.New("herdr-tailcat: profile lifecycle lock unavailable or timed out")
	}
	defer lifecycle.Close()
	if _, e = os.Stat(filepath.Join(dir, "profile.json")); e != nil {
		return nil, errors.New("herdr-tailcat: profile not found or inaccessible")
	}
	dial := func() (*net.UnixConn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "daemon.sock"))
		if err != nil {
			return nil, err
		}
		return c.(*net.UnixConn), nil
	}
	if c, e := dial(); e == nil {
		return c, nil
	}
	binary, e := os.Executable()
	if e != nil {
		return nil, e
	}
	logfile, e := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	defer logfile.Close()
	cmd := exec.Command(binary, "daemon", id)
	cmd.Env = append(os.Environ(), "HERDR_TAILCAT_HOME="+filepath.Dir(filepath.Dir(dir)))
	cmd.Stdout = logfile
	cmd.Stderr = logfile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if ctx.Err() != nil {
		return nil, errors.New("herdr-tailcat: daemon startup timed out")
	}
	if e = cmd.Start(); e != nil {
		return nil, errors.New("herdr-tailcat: cannot start profile daemon")
	}
	_ = cmd.Process.Release()
	lifecycle.Close()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c, e := dial(); e == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("herdr-tailcat: daemon startup timed out (check profile daemon.log)")
		case <-ticker.C:
		}
	}
}
