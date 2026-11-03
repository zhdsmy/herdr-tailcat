package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/line"
	"github.com/zhdsmy/herdr-tailcat/internal/peer"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/runstate"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"github.com/zhdsmy/herdr-tailcat/internal/stream"
	"tailscale.com/types/key"
)

func AddProfile(dir, text string) error {
	if _, e := os.Stat(filepath.Join(dir, "profile.json")); e == nil {
		return errors.New("profile already exists")
	}
	text = strings.TrimSpace(text)
	ci, e := tailcat.ParseAddr(tailcat.Addr(text))
	if e != nil {
		return errors.New("invalid tailcat address")
	}
	if ci.ServerDiscoPublic.IsZero() {
		return errors.New("legacy tailcat address lacks a separate disco key; generate a new server address")
	}
	if ci.PresharedKey.IsZero() {
		return errors.New("tailcat address must contain a preshared key")
	}
	if e = store.WriteJSON(filepath.Join(dir, "client.key"), key.NewNode()); e != nil {
		return e
	}
	return store.WriteJSON(filepath.Join(dir, "profile.json"), store.Profile{Address: text})
}
func PublicKey(dir string) (string, error) {
	var k key.NodePrivate
	if store.ReadJSON(filepath.Join(dir, "client.key"), &k) != nil || k.IsZero() {
		return "", errors.New("cannot read profile identity")
	}
	return k.Public().String(), nil
}

type tailDial struct{ c *tailcat.Client }

func (t *tailDial) Dial(ctx context.Context) (net.Conn, error) {
	return t.c.DialTCPPort(ctx, proto.Port)
}
func (t *tailDial) Ping(ctx context.Context) error { _, e := t.c.Ping(ctx); return e }
func (t *tailDial) Close() error                   { return t.c.Close() }
func Run(id string) error {
	if !line.ValidName(id) {
		return errors.New("invalid profile id")
	}
	home, e := store.Home()
	if e != nil {
		return e
	}
	dir := filepath.Join(home, "profiles", id)
	lock, e := runstate.Acquire(dir, "daemon")
	if e != nil {
		return errors.New("profile daemon is already running")
	}
	defer lock.Close()
	var p store.Profile
	var k key.NodePrivate
	if store.ReadJSON(filepath.Join(dir, "profile.json"), &p) != nil || store.ReadJSON(filepath.Join(dir, "client.key"), &k) != nil || k.IsZero() {
		return errors.New("cannot read profile or identity")
	}
	ci, e := tailcat.ParseAddr(tailcat.Addr(p.Address))
	if e != nil {
		return errors.New("invalid profile address")
	}
	psk, _ := ci.PresharedKey.MarshalText()
	redact.Add(p.Address, string(psk), strings.TrimPrefix(string(psk), "psk:"))
	var created atomic.Uint64
	engine := NewEngine(func() Dialer {
		if n := created.Add(1); n > 1 {
			fmt.Fprintf(os.Stderr, "client rebuilt generation=%d\n", n-1)
		}
		return &tailDial{&tailcat.Client{Server: tailcat.Addr(p.Address), Key: k, Logf: func(string, ...any) {}}}
	})
	defer engine.Close()
	path := filepath.Join(dir, "daemon.sock")
	_ = os.Remove(path)
	ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return errors.New("cannot listen on profile daemon socket")
	}
	defer ln.Close()
	defer os.Remove(path)
	if e = os.Chmod(path, 0600); e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	idle := 10 * time.Minute
	if s := os.Getenv("HERDR_TAILCAT_IDLE"); s != "" {
		v, err := time.ParseDuration(s)
		if err != nil || v <= 0 {
			return errors.New("invalid HERDR_TAILCAT_IDLE")
		}
		idle = v
	}
	state, e := runstate.New()
	if e != nil {
		return e
	}
	if e = runstate.Write(dir, "daemon", state); e != nil {
		return e
	}
	return runLocal(ctx, cancel, ln, engine, idle)
}

func runLocal(ctx context.Context, cancel context.CancelFunc, ln *net.UnixListener, engine *Engine, idle time.Duration) error {
	var active atomic.Int64
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	go func() {
		tick := time.NewTicker(min(idle/2, time.Second))
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				ln.Close()
				return
			case <-tick.C:
				if active.Load() == 0 && time.Since(time.Unix(0, last.Load())) >= idle {
					cancel()
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			break
		}
		active.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer active.Add(-1)
			defer func() { last.Store(time.Now().UnixNano()) }()
			defer c.Close()
			if _, e := peer.UID(c); e != nil {
				return
			}
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					c.Close()
				case <-done:
				}
			}()
			handle(ctx, c, engine)
		}()
	}
	engine.Close()
	wg.Wait()
	return nil
}
func handle(parent context.Context, c net.Conn, engine *Engine) {
	r := bufio.NewReaderSize(c, proto.MaxLine)
	_ = c.SetReadDeadline(time.Now().Add(proto.Timeout))
	var local proto.LocalRequest
	e := proto.Read(r, &local)
	_ = c.SetReadDeadline(time.Time{})
	q := local.Request
	if e == nil {
		e = proto.Validate(q)
	}
	if e != nil {
		proto.Fail(c, e)
		return
	}
	until := time.Now().Add(18 * time.Second)
	if local.DeadlineMS > 0 && time.UnixMilli(local.DeadlineMS).Before(until) {
		until = time.UnixMilli(local.DeadlineMS)
	}
	ctx, cancel := context.WithDeadline(parent, until)
	defer cancel()
	remote, e := engine.Dial(ctx)
	if e != nil {
		proto.Fail(c, ErrNoAnswer)
		return
	}
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(time.Until(deadline(ctx))))
	if e = proto.Write(remote, q); e != nil {
		proto.Fail(c, ErrNoAnswer)
		return
	}
	rr := bufio.NewReaderSize(remote, proto.MaxLine)
	var reply proto.Response
	if e = proto.Read(rr, &reply); e != nil || reply.Version != proto.Version {
		if os.Getenv("HERDR_TAILCAT_TRACE_EXIT") == "1" {
			fmt.Fprintln(os.Stderr, "server handshake failed:", redact.OneLine(e))
		}
		proto.Fail(c, errors.New("tailcat: invalid server handshake"))
		return
	}
	if e = proto.Write(c, reply); e != nil || !reply.OK || q.Op == "ping" {
		return
	}
	_ = remote.SetDeadline(time.Time{})
	stream.Relay(c, remote, r, rr)
}
func deadline(ctx context.Context) time.Time { d, _ := ctx.Deadline(); return d }
