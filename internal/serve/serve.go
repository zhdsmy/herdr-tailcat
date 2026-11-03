package serve

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/line"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/runstate"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"github.com/zhdsmy/herdr-tailcat/internal/stream"
	"github.com/zhdsmy/herdr-tailcat/internal/version"
	"tailscale.com/wgengine/filter"
)

// Prepare validates every policy before the injected Start is ever invoked.
func Prepare(home string, logger *log.Logger, start func(*tailcat.Server) error) (*tailcat.Server, store.ServerConfig, error) {
	cfg, allowed, k, e := Load(home)
	if e != nil {
		return nil, cfg, e
	}
	psk, _ := k.Public.PresharedKey.MarshalText()
	redact.Add(string(psk), strings.TrimPrefix(string(psk), "psk:"))
	s := &tailcat.Server{Key: k.Private, PresharedKey: k.Public.PresharedKey, Region: k.Public.Region[0], AllowedClients: allowed, ServedTCPPorts: []filter.PortRange{{First: proto.Port, Last: proto.Port}}, Logf: func(string, ...any) {}}
	if start != nil {
		if e = start(s); e != nil {
			return nil, cfg, errors.New("tailcat server startup failed")
		}
	}
	return s, cfg, nil
}
func Run() error {
	home, e := store.Home()
	if e != nil {
		return e
	}
	dir := filepath.Join(home, "server")
	lock, e := runstate.Acquire(dir, "serve")
	if e != nil {
		return errors.New("已有 serve 在运行 (cannot acquire serve lock)")
	}
	defer lock.Close()
	f, e := os.OpenFile(filepath.Join(home, "serve.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	logger := redact.Logger(f)
	s, cfg, e := Prepare(home, logger, nil)
	if e != nil {
		return e
	}
	signals, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sem := make(chan struct{}, cfg.MaxConns)
	idle, _ := clientIdle(cfg) // Load already validated this setting.
	streams := newManager(idle, logger)
	var wg sync.WaitGroup
	var lifecycle sync.Mutex
	closing := false
	s.OnTCP = func(port uint16) func(net.Conn) {
		if port != proto.Port {
			return nil
		}
		return func(c net.Conn) {
			lifecycle.Lock()
			if closing {
				lifecycle.Unlock()
				c.Close()
				return
			}
			wg.Add(1)
			lifecycle.Unlock()
			defer wg.Done()
			if !admit(c, sem) {
				return
			}
			defer func() { <-sem }()
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					c.Close()
				case <-done:
				}
			}()
			peerKey := ""
			for _, entry := range s.PeerEnv(c.LocalAddr(), c.RemoteAddr()) {
				if strings.HasPrefix(entry, "TAILCAT_PEER_KEY=") {
					peerKey = strings.TrimPrefix(entry, "TAILCAT_PEER_KEY=")
					v := strings.TrimPrefix(entry, "TAILCAT_PEER_KEY=nodekey:")
					if len(v) >= 8 {
						logger.Printf("accepted peer %s", v[:8])
					}
				}
			}
			streams.handle(ctx, c, cfg, peerKey, proto.Timeout)
		}
	}
	if e = s.Start(); e != nil {
		return errors.New("tailcat server startup failed (see network and DERP availability)")
	}
	logger.Print("serve ready")
	state, e := runstate.New()
	if e != nil {
		s.Close()
		return e
	}
	state.Digest = Digest(cfg, s.AllowedClients)
	if e = runstate.Write(dir, "serve", state); e != nil {
		s.Close()
		return e
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
running:
	for {
		select {
		case <-signals.Done():
			break running
		case <-tick.C:
			state.Streams = streams.active.Load()
			if err := runstate.Write(dir, "serve", state); err != nil {
				logger.Print("cannot update serve state")
			}
		}
	}
	lifecycle.Lock()
	closing = true
	lifecycle.Unlock()
	cancel()
	shutdown(&wg, s.DrainTCP, s.Close)
	return nil
}

// Keep the network stack alive until handlers have closed their streams and
// given FINs time to leave the process. An absent peer must not block shutdown.
func shutdown(wg *sync.WaitGroup, drain func(context.Context) error, closeStack func() error) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = drain(ctx) // TIME-WAIT or an absent peer can consume the drain budget.
	_ = closeStack()
}
func ChildEnv(base []string, extra map[string]string) []string {
	m := map[string]string{}
	for _, s := range base {
		n, v, ok := strings.Cut(s, "=")
		if ok && n != "HC_SESSION" && n != "HC_SOCKET" {
			m[n] = v
		}
	}
	for n, v := range extra {
		if n != "HC_SESSION" && n != "HC_SOCKET" {
			m[n] = v
		}
	}
	out := make([]string, 0, len(m))
	for n, v := range m {
		out = append(out, n+"="+v)
	}
	return out
}
func Handle(ctx context.Context, c net.Conn, cfg store.ServerConfig, logger *log.Logger, timeout time.Duration) {
	idle, err := clientIdle(cfg)
	if err != nil {
		proto.Fail(c, err)
		c.Close()
		return
	}
	newManager(idle, logger).handle(ctx, c, cfg, "", timeout)
}
func (m *manager) handle(parent context.Context, c net.Conn, cfg store.ServerConfig, peerKey string, timeout time.Duration) {
	logger := m.logger
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer c.Close()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-finished:
		}
	}()
	r := bufio.NewReaderSize(c, proto.MaxLine)
	q, e := proto.ReadRequest(c, r, timeout)
	if e != nil {
		c.SetWriteDeadline(time.Now().Add(time.Second))
		proto.Fail(c, e)
		return
	}
	info := &proto.Info{Version: version.String(), Exports: cfg.Exports}
	if q.Op == "ping" {
		if os.Getenv("HERDR_TAILCAT_TRACE_EXIT") == "1" {
			logger.Print("ping received")
		}
		_ = proto.Write(c, proto.Response{Version: proto.Version, OK: true, Server: info})
		return
	}
	session := "default"
	if q.Session != nil {
		session = *q.Session
	}
	found := false
	for _, s := range cfg.Exports {
		if s == session {
			found = true
		}
	}
	if !line.ValidName(session) || !found {
		proto.Fail(c, fmt.Errorf("session %q is not exported by this server", session))
		return
	}
	cmd := exec.Command(cfg.HerdrCore, "connect", "--session", session)
	cmd.Env = ChildEnv(os.Environ(), cfg.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, e := cmd.StdinPipe()
	if e != nil {
		proto.Fail(c, errors.New("cannot create core input"))
		return
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		in.Close()
		proto.Fail(c, errors.New("cannot create core output"))
		return
	}
	errpipe, e := cmd.StderrPipe()
	if e != nil {
		in.Close()
		out.Close()
		proto.Fail(c, errors.New("cannot create core error stream"))
		return
	}
	if e = cmd.Start(); e != nil {
		in.Close()
		out.Close()
		errpipe.Close()
		proto.Fail(c, errors.New("cannot start configured herdr-core"))
		return
	}
	trace := func(format string, args ...any) {
		if os.Getenv("HERDR_TAILCAT_TRACE_EXIT") == "1" {
			logger.Printf("stream session=%s pid=%d "+format, append([]any{session, cmd.Process.Pid}, args...)...)
		}
	}
	trace("started")
	// Never pass raw, potentially secret-bearing child diagnostics to stdout.
	go func() {
		scan := bufio.NewScanner(io.LimitReader(errpipe, 64*1024))
		for scan.Scan() {
			logger.Print(scan.Text())
		}
		_, _ = io.Copy(io.Discard, errpipe)
	}()
	if e = proto.Write(c, proto.Response{Version: proto.Version, OK: true, Server: info}); e != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		in.Close()
		out.Close()
		_ = cmd.Wait()
		return
	}
	m.active.Add(1)
	defer m.active.Add(-1)
	release := m.install(peerKey, session, cancel)
	defer release()
	activity := &activity{last: time.Now()}
	go m.watch(ctx, cancel, activity, m.sequence.Add(1))
	done := make(chan int, 2)
	go func() {
		n, err := io.Copy(in, activeReader{r, activity})
		trace("client-read-ended bytes=%d error=%v", n, err)
		in.Close()
		done <- 0
	}()
	go func() {
		n, err := io.Copy(c, out)
		trace("server-write-ended bytes=%d error=%v", n, err)
		stream.HalfClose(c)
		done <- 1
	}()
	select {
	case which := <-done:
		trace("closing first-direction=%d", which)
		if which == 0 {
			select {
			case <-done:
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	case <-ctx.Done():
		trace("closing context")
	}
	c.Close()
	in.Close()
	// connect is a stream shim; the real core detaches itself when autostarted.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
		trace("reaped")
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
	}
}

// admit bounds active streams and gives overload a bounded, actionable reply.
func admit(c net.Conn, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	default:
		defer c.Close()
		_ = c.SetWriteDeadline(time.Now().Add(time.Second))
		proto.Fail(c, errors.New("server busy"))
		return false
	}
}
