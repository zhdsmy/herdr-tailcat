package serve

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"github.com/zhdsmy/herdr-tailcat/internal/version"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestNoStartOnInvalidAllowList(t *testing.T) {
	pub := key.NewNode().Public().String()
	for name, text := range map[string]string{"missing": "", "empty": "", "comments": "# nobody\n", "bad": pub + "\ninvalid\n", "duplicate": pub + "\n" + pub, "unreadable": pub} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			identity := tailcat.NewPrivateKey()
			identity.Public.Region = []*tailcfg.DERPRegion{{RegionID: 1}}
			if e := store.WriteJSON(filepath.Join(home, "server", "server.key"), identity); e != nil {
				t.Fatal(e)
			}
			if e := store.WriteJSON(filepath.Join(home, "server", "config.json"), store.ServerConfig{HerdrCore: "/not-executed/herdr-core", Exports: []string{"tc-work"}}); e != nil {
				t.Fatal(e)
			}
			p := filepath.Join(home, "server", "allowed_clients")
			os.MkdirAll(filepath.Dir(p), 0700)
			if name != "missing" {
				os.WriteFile(p, []byte(text), 0600)
			}
			if name == "unreadable" {
				os.Chmod(p, 0)
			}
			started := false
			_, _, err := Prepare(home, redact.Logger(io.Discard), func(*tailcat.Server) error { started = true; return nil })
			if err == nil || started {
				t.Fatal("Start called before policy validation")
			}
		})
	}
}

func TestValidAllowListReachesStart(t *testing.T) {
	home := t.TempDir()
	identity := tailcat.NewPrivateKey()
	identity.Public.Region = []*tailcfg.DERPRegion{{RegionID: 1}}
	store.WriteJSON(filepath.Join(home, "server", "server.key"), identity)
	store.WriteJSON(filepath.Join(home, "server", "config.json"), store.ServerConfig{HerdrCore: "/not-executed/herdr-core", Exports: []string{"tc-work"}})
	os.WriteFile(filepath.Join(home, "server", "allowed_clients"), []byte(key.NewNode().Public().String()+"\n"), 0600)
	calls := 0
	_, _, e := Prepare(home, redact.Logger(io.Discard), func(s *tailcat.Server) error {
		calls++
		if len(s.ServedTCPPorts) != 1 || s.ServedTCPPorts[0].First != proto.Port || s.ServedTCPPorts[0].Last != proto.Port {
			t.Fatal("packet filter must admit only 6470")
		}
		if len(s.AllowedClients) != 1 {
			t.Fatal("missing policy")
		}
		return nil
	})
	if e != nil || calls != 1 {
		t.Fatal("valid configuration never reached Start", e, calls)
	}
}

func TestConcurrentOverload(t *testing.T) {
	sem := make(chan struct{}, 1)
	first, peer := net.Pipe()
	defer first.Close()
	defer peer.Close()
	if !admit(first, sem) {
		t.Fatal("first connection rejected")
	}
	const n = 12
	done := make(chan bool, n)
	var readers sync.WaitGroup
	defer readers.Wait()
	for range n {
		a, b := net.Pipe()
		go func() { done <- admit(b, sem) }()
		readers.Add(1)
		go func() {
			defer readers.Done()
			defer a.Close()
			a.SetReadDeadline(time.Now().Add(2 * time.Second))
			var reply proto.Response
			if err := proto.Read(bufio.NewReaderSize(a, proto.MaxLine), &reply); err != nil {
				t.Error(err)
				return
			}
			if reply.OK || reply.Error != "server busy" {
				t.Error(reply)
			}
		}()
	}
	for range n {
		if <-done {
			t.Fatal("overload admitted")
		}
	}
	<-sem
}

func TestPingReportsBuildVersion(t *testing.T) {
	old := version.Build
	version.Build = "v-test-injected"
	defer func() { version.Build = old }()
	a, b := net.Pipe()
	defer a.Close()
	go Handle(context.Background(), b, store.ServerConfig{}, redact.Logger(io.Discard), proto.Timeout)
	proto.Write(a, proto.Request{Version: proto.Version, Op: "ping"})
	var reply proto.Response
	if err := proto.Read(bufio.NewReader(a), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Server == nil || reply.Server.Version != "v-test-injected" {
		t.Fatal(reply)
	}
}
func TestHandshakeRejections(t *testing.T) {
	for name, input := range map[string]string{"version": `{"tailcat_herdr":2,"op":"connect"}`, "op": `{"tailcat_herdr":1,"op":"shell"}`, "unknown-field": `{"tailcat_herdr":1,"op":"ping","command":"x"}`, "session": `{"tailcat_herdr":1,"op":"connect","session":"tc-nope"}`, "null-default": `{"tailcat_herdr":1,"op":"connect","session":null}`, "oversized": strings.Repeat("x", 4096)} {
		t.Run(name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			finished := make(chan struct{})
			go func() {
				Handle(context.Background(), b, store.ServerConfig{Exports: []string{"tc-work"}}, redact.Logger(io.Discard), proto.Timeout)
				close(finished)
			}()
			a.SetDeadline(time.Now().Add(2 * time.Second))
			go func() { io.WriteString(a, input+"\n") }()
			var reply proto.Response
			if err := proto.Read(bufio.NewReaderSize(a, proto.MaxLine), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.OK || reply.Error == "" {
				t.Fatal(reply)
			}
			if name == "session" || name == "null-default" {
				if !strings.Contains(reply.Error, "not exported") {
					t.Fatal(reply)
				}
			}
			<-finished
		})
	}
}
func TestHandshakeTenSecondTimeout(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	start := time.Now()
	go Handle(context.Background(), b, store.ServerConfig{}, redact.Logger(io.Discard), proto.Timeout)
	a.SetReadDeadline(start.Add(12 * time.Second))
	var reply proto.Response
	if err := proto.Read(bufio.NewReaderSize(a, proto.MaxLine), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.OK || time.Since(start) < 9*time.Second {
		t.Fatal("timeout not enforced")
	}
}
func TestChildEnvironmentCannotChooseSessionOrSocket(t *testing.T) {
	env := ChildEnv([]string{"HC_SESSION=evil", "HC_SOCKET=/real.sock", "XDG_STATE_HOME=/scratch"}, map[string]string{"HC_SESSION": "evil2", "HC_SOCKET": "/other.sock", "XDG_CONFIG_HOME": "/scratch/config"})
	for _, s := range env {
		if strings.HasPrefix(s, "HC_SESSION=") || strings.HasPrefix(s, "HC_SOCKET=") {
			t.Fatal(s)
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), "XDG_STATE_HOME=/scratch") {
		t.Fatal("lost XDG isolation")
	}
}
