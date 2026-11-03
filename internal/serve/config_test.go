package serve

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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

func writeServer(t *testing.T, home string, exports []string) {
	t.Helper()
	identity := tailcat.NewPrivateKey()
	identity.Public.Region = []*tailcfg.DERPRegion{{RegionID: 1}}
	if err := store.WriteJSON(filepath.Join(home, "server", "server.key"), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(home, "server", "config.json"), store.ServerConfig{HerdrCore: "/not-executed/herdr-core", Exports: exports, MaxConns: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "server", "allowed_clients"), []byte(key.NewNode().Public().String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExportsRejectOversizedHandshake(t *testing.T) {
	home := t.TempDir()
	exports := make([]string, 128)
	for i := range exports {
		exports[i] = fmt.Sprintf("e%03d", i) + strings.Repeat("a", 28)
		if len(exports[i]) != 32 || !strings.HasPrefix(exports[i], "e") {
			t.Fatal(exports[i])
		}
	}
	writeServer(t, home, exports)
	_, _, _, err := Load(home)
	if err == nil || !strings.Contains(err.Error(), "handshake reply would exceed 4 KiB") {
		t.Fatal(err)
	}
}

func TestExportsNearLimitRoundTripPing(t *testing.T) {
	old := version.Build
	version.Build = "v-export-boundary"
	defer func() { version.Build = old }()
	var fit []string
	for n := 1; n < 200; n++ {
		next := append(append([]string{}, fit...), fmt.Sprintf("s%03d", n)+strings.Repeat("b", 28))
		if err := exportsFit(next); err != nil {
			break
		}
		fit = next
	}
	if len(fit) < 2 {
		t.Fatal("boundary not found", len(fit))
	}
	overflow := fmt.Sprintf("s%03d", len(fit)+1) + strings.Repeat("b", 28)
	if err := exportsFit(append(append([]string{}, fit...), overflow)); err == nil {
		t.Fatal("one more export still fit")
	}
	size, err := replySize(fit)
	if err != nil || size+replyMargin > proto.MaxLine {
		t.Fatal(size, err)
	}
	home := t.TempDir()
	writeServer(t, home, fit)
	cfg, _, _, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer a.Close()
	go Handle(context.Background(), b, cfg, redact.Logger(io.Discard), proto.Timeout)
	if err := proto.Write(a, proto.Request{Version: proto.Version, Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	var reply proto.Response
	if err := proto.Read(bufio.NewReaderSize(a, proto.MaxLine), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.OK || reply.Server == nil || len(reply.Server.Exports) != len(fit) {
		t.Fatal(reply.OK, len(fit))
	}
}

func TestExportAddRejectsOversizedList(t *testing.T) {
	home := t.TempDir()
	writeServer(t, home, nil)
	var added []string
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("n%03d", i) + strings.Repeat("c", 28)
		err := ExportAdd(home, name)
		if err != nil {
			if !strings.Contains(err.Error(), "handshake reply would exceed 4 KiB") {
				t.Fatal(err)
			}
			if _, _, _, loadErr := Load(home); loadErr != nil {
				t.Fatal("rejected add changed a loadable config", loadErr)
			}
			return
		}
		added = append(added, name)
	}
	t.Fatal("never rejected", len(added))
}

func TestConcurrentExportAddKeepsEveryName(t *testing.T) {
	home := t.TempDir()
	writeServer(t, home, []string{"base000" + strings.Repeat("d", 25)})
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ExportAdd(home, fmt.Sprintf("x%02d", i)+strings.Repeat("e", 29))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var cfg store.ServerConfig
	if err := store.ReadJSON(filepath.Join(home, "server", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Exports) != n+1 {
		t.Fatal(len(cfg.Exports))
	}

	seen := make(map[string]bool)
	for _, name := range cfg.Exports {
		if seen[name] {
			t.Fatal("duplicate export", name)
		}
		seen[name] = true
	}
	for i := range n {
		if !seen[fmt.Sprintf("x%02d", i)+strings.Repeat("e", 29)] {
			t.Fatal("successful export lost", i)
		}
	}
}

func TestConcurrentAllowAddOneSuccess(t *testing.T) {
	home := t.TempDir()
	writeServer(t, home, []string{"tc-work"})
	pub := key.NewNode().Public().String()
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- AllowAdd(home, pub)
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
			continue
		}
		if err.Error() != "client key already allowed" {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatal(ok)
	}
	if _, _, _, err := Load(home); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAddRemoveMatchesSerialOrder(t *testing.T) {
	home := t.TempDir()
	writeServer(t, home, []string{"keep"})
	type op struct {
		add  bool
		name string
	}
	ops := []op{{true, "alpha"}, {true, "bravo"}, {false, "alpha"}, {true, "cairo"}, {false, "bravo"}, {true, "delta"}, {false, "missing"}}
	var wg sync.WaitGroup
	start := make(chan struct{})
	got := make([]error, len(ops))
	for i, item := range ops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if item.add {
				got[i] = ExportAdd(home, item.name)
			} else {
				got[i] = ExportRemove(home, item.name)
			}
		}()
	}
	close(start)
	wg.Wait()
	var cfg store.ServerConfig
	if err := store.ReadJSON(filepath.Join(home, "server", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{"keep": false}
	for _, name := range cfg.Exports {
		if have[name] {
			t.Fatal("duplicate", name)
		}
		have[name] = true
	}

	// Find a serial ordering with the same successes/errors and final disk state.
	var serial func(map[string]bool, uint) bool
	serial = func(state map[string]bool, used uint) bool {
		if used == (1<<len(ops))-1 {
			for k, v := range state {
				if have[k] != v {
					return false
				}
			}
			for k, v := range have {
				if state[k] != v {
					return false
				}
			}
			return true
		}
		for i, item := range ops {
			if used&(1<<i) != 0 {
				continue
			}
			expectedSuccess := state[item.name] != item.add
			if (got[i] == nil) != expectedSuccess {
				continue
			}
			next := make(map[string]bool)
			for k, v := range state {
				next[k] = v
			}
			if expectedSuccess {
				next[item.name] = item.add
			}
			if serial(next, used|(1<<i)) {
				return true
			}
		}
		return false
	}
	if !serial(map[string]bool{"keep": true}, 0) {
		t.Fatal("no matching serial ordering", got, cfg.Exports)
	}

}

func TestConfigLockAcrossProcesses(t *testing.T) {
	if os.Getenv("HERDR_TAILCAT_LOCK_CHILD") == "1" {
		home := os.Getenv("HERDR_TAILCAT_LOCK_HOME")
		err := ExportAdd(home, os.Getenv("HERDR_TAILCAT_LOCK_NAME"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	home := t.TempDir()
	writeServer(t, home, []string{"base"})
	release := make(chan struct{})
	entered := make(chan struct{})
	go func() {
		_ = withConfigLock(home, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("lock was not acquired")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConfigLockAcrossProcesses$", "-test.v")
	cmd.Env = append(os.Environ(), "HERDR_TAILCAT_LOCK_CHILD=1", "HERDR_TAILCAT_LOCK_HOME="+home, "HERDR_TAILCAT_LOCK_NAME=child")
	done := make(chan error, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		if err != nil {
			done <- errors.New(string(out))
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		t.Fatal("child did not wait for the lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not finish after unlock")
	}
	var cfg store.ServerConfig
	if err := store.ReadJSON(filepath.Join(home, "server", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Exports) != 2 || cfg.Exports[1] != "child" {
		t.Fatal(cfg.Exports)
	}
	info, err := os.Stat(filepath.Join(home, "server", "config.lock"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

func TestAllowAddRemoveSerialOutcomes(t *testing.T) {
	home := t.TempDir()
	writeServer(t, home, []string{"tc-work"})
	pub := key.NewNode().Public().String()
	for range 20 {
		// Start absent. Add then remove leaves absent; remove then add returns
		// not-found from remove and leaves exactly one key. No other outcome is valid.
		_, err := AllowRemove(home, pub)
		if err != nil && !errors.Is(err, ErrNotAllowed) {
			t.Fatal(err)
		}
		gate := make(chan struct{})
		added := make(chan error, 1)
		removed := make(chan error, 1)
		go func() { <-gate; added <- AllowAdd(home, pub) }()
		go func() { <-gate; _, err := AllowRemove(home, pub); removed <- err }()
		close(gate)
		if err := <-added; err != nil {
			t.Fatal(err)
		}
		rmErr := <-removed
		cfg, keys, _, err := Load(home)
		if err != nil || len(cfg.Exports) != 1 {
			t.Fatal(err)
		}
		found := false
		for _, k := range keys {
			if k.String() == pub {
				found = true
			}
		}
		if rmErr == nil && found || errors.Is(rmErr, ErrNotAllowed) && !found {
			t.Fatal("nonserial result", rmErr, found)
		}
		if rmErr != nil && !errors.Is(rmErr, ErrNotAllowed) {
			t.Fatal(rmErr)
		}
	}
}
