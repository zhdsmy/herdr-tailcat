package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/daemon"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/runstate"
	"github.com/zhdsmy/herdr-tailcat/internal/serve"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func setupCLI(t *testing.T) string {
	t.Helper()
	dir, e := os.MkdirTemp("/tmp", "tc-json-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, k := range []string{"HERDR_TAILCAT_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(k, filepath.Join(dir, k))
	}
	t.Setenv("HC_SESSION", "tc-cli-contract")
	t.Setenv("HC_SOCKET", "")
	t.Setenv("HC_TERMINAL_ID", "")
	home := os.Getenv("HERDR_TAILCAT_HOME")
	os.MkdirAll(home, 0700)
	fake := filepath.Join(dir, "hc")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HC_FAKE_CALLS"
if [ "$3" = ls ]; then cat "$HC_FAKE_LS"; exit 0; fi
if [ -s "$HC_FAKE_ERR_FILE" ]; then cat "$HC_FAKE_ERR_FILE" >&2; exit 6; fi
if [ "$3" = add ]; then printf '%s\n' '{"name":"box","id":"core-id","host":"host","version":"0.1.16","session":"tc-work","target":"omit-me"}'; else printf '{}\n'; fi
`
	os.WriteFile(fake, []byte(script), 0700)
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("HC_FAKE_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("HC_FAKE_LS", filepath.Join(dir, "machines"))
	t.Setenv("HC_FAKE_ERR_FILE", filepath.Join(dir, "core-error"))
	t.Setenv("HC_FAKE_ERR", "")
	setCoreError(t, "")
	os.WriteFile(os.Getenv("HC_FAKE_LS"), []byte("{\"machines\":[]}"), 0600)
	k := tailcat.NewPrivateKey()
	k.Public.RegionID = -1
	// Full address embeds a fixed relay; no network lookup in CLI unit tests.
	// A minimal fixed DERP node is supplied by the same helper used in serve tests.
	writeIdentity(t, home, *k)
	return home
}
func setCoreError(t *testing.T, diagnostic string) {
	t.Helper()
	if err := store.WriteFile(os.Getenv("HC_FAKE_ERR_FILE"), []byte(diagnostic)); err != nil {
		t.Fatal(err)
	}
}
func writeIdentity(t *testing.T, home string, k tailcat.PrivateKey) {
	t.Helper()
	// Keep a complete full address fixture without calling server init/DERP.
	k.Public.Region = fixtureRegion()
	if e := store.WriteJSON(filepath.Join(home, "server", "server.key"), k); e != nil {
		t.Fatal(e)
	}
	store.WriteJSON(filepath.Join(home, "server", "config.json"), store.ServerConfig{HerdrCore: "/usr/bin/true", Exports: []string{"tc-work"}, MaxConns: 16, ClientIdleTimeout: "90s"})
	store.WriteFile(filepath.Join(home, "server", "allowed_clients"), []byte(key.NewNode().Public().String()+"\n"))
	addr, e := serve.Address(home, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = daemon.AddProfile(filepath.Join(home, "profiles", "p1"), addr); e != nil {
		t.Fatal(e)
	}
}
func invoke(t *testing.T, args ...string) (map[string]any, error, string) {
	t.Helper()
	var out bytes.Buffer
	err := runMode(args, true, strings.NewReader("invalid"), &out)
	var r map[string]any
	d := json.NewDecoder(&out)
	if e := d.Decode(&r); e != nil {
		t.Fatalf("%v JSON: %v %q", args, e, out.String())
	}
	if d.Decode(new(any)) == nil {
		t.Fatal("multiple JSON objects")
	}
	if err == nil && r["ok"] != true {
		t.Fatalf("missing ok: %v", r)
	}
	return r, err, asString(r)
}
func asString(v any) string { b, _ := json.Marshal(v); return string(b) }
func pingFixture(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, "profiles", "p1", "daemon.sock")
	ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, e := ln.AcceptUnix()
			if e != nil {
				return
			}
			func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(time.Second))
				var q proto.LocalRequest
				if proto.Read(bufio.NewReaderSize(c, proto.MaxLine), &q) == nil {
					proto.Write(c, proto.Response{Version: 1, OK: true, Server: &proto.Info{Version: "test", Exports: []string{"tc-work"}}})
				}
			}()
		}
	}()
}
func TestManagementArgumentsJSON(t *testing.T) {
	setupCLI(t)
	cases := [][]string{{}, {"unknown"}, {"server"}, {"profile"}, {"serve"}, {"daemon"}, {"via"}, {"server", "init", "extra"}, {"server", "show-address", "--bad"}, {"server", "allow"}, {"server", "allow", "ls", "extra"}, {"server", "allow", "add", "bad"}, {"server", "export"}, {"server", "export", "add", "bad/name"}, {"server", "status", "--bad"}, {"server", "install-service", "--bad"}, {"server", "service"}, {"server", "service", "status", "--label", "bad/label"}, {"profile", "ls", "extra"}, {"profile", "add", "bad/name"}, {"profile", "add", "p1", "extra"}, {"profile", "pubkey", "p1", "extra"}, {"profile", "test", "p1", "extra"}, {"profile", "register", "p1"}, {"profile", "unregister", "p1"}, {"profile", "rm", "p1", "--bad"}}
	for _, a := range cases {
		t.Run(strings.Join(a, "_"), func(t *testing.T) {
			r, e, _ := invoke(t, a...)
			if e == nil {
				t.Fatal("accepted invalid args")
			}
			er := r["error"].(map[string]any)
			if r["ok"] != false || len(er) != 3 || er["kind"] == "" {
				t.Fatal(r)
			}
		})
	}
}
func TestCoreErrorsAndLocalPreflight(t *testing.T) {
	home := setupCLI(t)
	pingFixture(t, home)
	for _, kind := range []string{"name_taken", "already_added", "usage", "unreachable"} {
		setCoreError(t, asString(map[string]any{"error": map[string]string{"kind": kind, "message": "original message", "hint": "original hint"}}))
		r, e, _ := invoke(t, "profile", "register", "p1", "--name", "box", "--session", "tc-work")
		if e == nil {
			t.Fatal("expected core failure")
		}
		er := r["error"].(map[string]any)
		if er["kind"] != "core:"+kind || er["message"] != "original message" || er["hint"] != "original hint" {
			t.Fatal(r)
		}
	}
	before, _ := os.ReadFile(os.Getenv("HC_FAKE_CALLS"))
	r, e, _ := invoke(t, "profile", "register", "p1", "--name", "box", "--session", "not-exported")
	if e == nil || r["error"].(map[string]any)["kind"] != "session_not_exported" {
		t.Fatal(r)
	}
	after, _ := os.ReadFile(os.Getenv("HC_FAKE_CALLS"))
	if !bytes.Equal(before, after) {
		t.Fatal("unexported session reached core")
	}
	setCoreError(t, "")
	r, e, _ = invoke(t, "profile", "register", "p1", "--name", "box", "--session", "tc-work", "--temp")
	if e != nil || r["machine"].(map[string]any)["target"] != nil {
		t.Fatal(r, e)
	}
}
func TestExactTargetIsolation(t *testing.T) {
	setupCLI(t)
	target, e := profileTarget("p1")
	if e != nil {
		t.Fatal(e)
	}
	entries := []machine{{Name: "box", Target: target, Via: true, State: "connected"}, {Name: "ssh", Target: target, Via: false}, {Name: "foreign", Target: target + "x", Via: true}}
	b, _ := json.Marshal(map[string]any{"machines": entries})
	os.WriteFile(os.Getenv("HC_FAKE_LS"), b, 0600)
	r, e, _ := invoke(t, "profile", "ls")
	if e != nil {
		t.Fatal(e)
	}
	ms := r["profiles"].([]any)[0].(map[string]any)["machines"].([]any)
	if len(ms) != 1 || ms[0].(map[string]any)["name"] != "box" {
		t.Fatal(r)
	}
	for _, name := range []string{"ssh", "foreign"} {
		r, e, _ = invoke(t, "profile", "unregister", "p1", "--name", name)
		if e == nil || r["error"].(map[string]any)["kind"] != "target_mismatch" {
			t.Fatal(r)
		}
	}
	r, e, _ = invoke(t, "profile", "rm", "p1")
	if e == nil || r["error"].(map[string]any)["kind"] != "profile_in_use" {
		t.Fatal(r)
	}
	r, e, _ = invoke(t, "profile", "unregister", "p1", "--name", "box")
	if e != nil {
		t.Fatal(r, e)
	}
}
func TestServerRestartRequired(t *testing.T) {
	home := setupCLI(t)
	dir := filepath.Join(home, "server")
	lock, e := runstate.Acquire(dir, "serve")
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	s, e := runstate.New()
	if e != nil {
		t.Fatal(e)
	}
	cfg, keys, e := serve.DiskConfig(home)
	if e != nil {
		t.Fatal(e)
	}
	s.Digest = serve.Digest(cfg, keys)
	runstate.Write(dir, "serve", s)
	r, e, _ := invoke(t, "server", "status", "--label", "dev.herdr-tailcat.test.contract", "--plist-dir", t.TempDir())
	if e != nil || r["restart_required"] != false || r["running"] != true {
		t.Fatal(r, e)
	}
	invoke(t, "server", "export", "add", "tc-other")
	r, e, _ = invoke(t, "server", "status", "--label", "dev.herdr-tailcat.test.contract", "--plist-dir", t.TempDir())
	if e != nil || r["restart_required"] != true {
		t.Fatal(r, e)
	}
	invoke(t, "server", "export", "rm", "tc-other")
	r, e, _ = invoke(t, "server", "allow", "ls")
	if e != nil {
		t.Fatal(e)
	}
	pk := r["clients"].([]any)[0].(map[string]any)["public_key"].(string)
	r, e, _ = invoke(t, "server", "allow", "rm", pk)
	if e != nil || r["warning"] == "" {
		t.Fatal(r, e)
	}
	if _, _, _, e = serve.Load(home); e == nil {
		t.Fatal("empty whitelist did not fail closed")
	}
}
func TestShellQuoteRoundTrip(t *testing.T) {
	for _, s := range []string{"a b", "a'b", "$(false)", "路径"} {
		out, e := exec.Command("/bin/sh", "-c", "printf '%s' "+shellQuote(s)).Output()
		if e != nil || string(out) != s {
			t.Fatalf("shell escaping: %v", e)
		}
	}
}

func TestUnverifiedDaemonNeverStopped(t *testing.T) {
	home := setupCLI(t)
	dir := filepath.Join(home, "profiles", "p1")
	lock, e := runstate.Acquire(dir, "daemon")
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if e = stopDaemon(context.Background(), dir); e == nil || asCLIError(e).Kind != "ownership_unverified" {
		t.Fatal(e)
	}
}

func fixtureRegion() []*tailcfg.DERPRegion {
	return []*tailcfg.DERPRegion{{RegionID: 1, RegionCode: "test", Nodes: []*tailcfg.DERPNode{{Name: "test", RegionID: 1, HostName: "example.invalid", DERPPort: 443}}}}
}

// A separate contract/secret sweep covers every management command, including
// errors returned by a hostile core. Fixture key material stays only in memory.
func TestJSONContractAndSecrets(t *testing.T) {
	home := setupCLI(t)
	pingFixture(t, home)
	doc, e := os.ReadFile("../../docs/cli-json.md")
	if e != nil {
		t.Fatal(e)
	}
	for kind := range coreKinds {
		if !strings.Contains(string(doc), kind) {
			t.Fatalf("undocumented core kind %s", kind)
		}
	}
	var identity tailcat.PrivateKey
	store.ReadJSON(filepath.Join(home, "server", "server.key"), &identity)
	private, _ := identity.Private.MarshalText()
	psk, _ := identity.Public.PresharedKey.MarshalText()
	var client key.NodePrivate
	store.ReadJSON(filepath.Join(home, "profiles", "p1", "client.key"), &client)
	clientText, _ := client.MarshalText()
	full, _ := serve.Address(home, true)
	short, _ := serve.Address(home, false)
	secrets := []string{full, short, string(private), strings.TrimPrefix(string(private), "priv"+"key:"), string(psk), strings.TrimPrefix(string(psk), "psk:"), string(clientText), strings.TrimPrefix(string(clientText), "priv"+"key:")}
	type contract struct {
		args   []string
		fields []string
	}
	tests := []contract{
		{[]string{"server", "status", "--label", "dev.herdr.tailcat.test.json", "--plist-dir", t.TempDir()}, []string{"initialized", "running", "pid", "started_at", "version", "exports", "allowed_clients", "streams", "restart_required"}},
		{[]string{"server", "allow", "ls"}, []string{"clients"}},
		{[]string{"server", "export", "ls"}, []string{"exports"}},
		{[]string{"profile", "pubkey", "p1"}, []string{"public_key", "fingerprint"}},
		{[]string{"profile", "ls"}, []string{"profiles"}},
		{[]string{"profile", "test", "p1"}, []string{"rtt_ms", "server"}},
		{[]string{"profile", "register", "p1", "--name", "box", "--session", "tc-work"}, []string{"machine"}},
		{[]string{"server", "install-service", "--dry-run", "--os", "darwin"}, []string{"text", "os"}},
		{[]string{"server", "service", "status", "--label", "dev.herdr.tailcat.test.json", "--plist-dir", t.TempDir()}, []string{"loaded", "pid", "label"}},
		{[]string{"server", "export", "add", "tc-extra"}, []string{}},
		{[]string{"server", "export", "rm", "tc-extra"}, []string{}},
		{[]string{"profile", "rm", "missing"}, nil},
		{[]string{"profile", "unregister", "p1", "--name", "missing"}, nil},
		{[]string{"server", "init"}, nil},
		{[]string{"profile", "add", "p1"}, nil},
		{[]string{"server", "service", "start", "--label", "dev.herdr.tailcat.test.json", "--plist-dir", t.TempDir()}, nil},
	}
	for _, tc := range tests {
		r, err, output := invoke(t, tc.args...)
		if tc.fields != nil && runtime.GOOS == "darwin" {
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			count := len(tc.fields) + 1
			if tc.args[0] == "server" && tc.args[1] == "status" {
				count++
			}
			if len(r) != count {
				t.Fatalf("unexpected fields %v: %v", tc.args, r)
			}
			for _, field := range tc.fields {
				if _, ok := r[field]; !ok {
					t.Fatal("missing field", field)
				}
				if !strings.Contains(string(doc), field) {
					t.Fatal("undocumented field", field)
				}
			}
		}
		for _, secret := range secrets {
			if secret != "" && strings.Contains(output, secret) {
				t.Fatalf("secret leaked by %v", tc.args)
			}
		}
	}
	target, _ := profileTarget("p1")
	ms := []machine{{Name: "box", Via: true, Target: target, Why: full + string(psk)}}
	b, _ := json.Marshal(map[string]any{"machines": ms})
	os.WriteFile(os.Getenv("HC_FAKE_LS"), b, 0600)
	_, _, listOut := invoke(t, "profile", "ls")
	for _, s := range secrets {
		if s != "" && strings.Contains(listOut, s) {
			t.Fatal("list leaked secret")
		}
	}
	for _, secret := range secrets {
		setCoreError(t, asString(map[string]any{"error": map[string]string{"kind": "unreachable", "message": secret, "hint": secret}}))
		r, e, out := invoke(t, "profile", "register", "p1", "--name", "box", "--session", "tc-work")
		if e == nil || r["error"].(map[string]any)["kind"] != "core:unreachable" {
			t.Fatal(r, e)
		}
		if strings.Contains(out, secret) {
			t.Fatal("core diagnostic leaked a secret")
		}
	}
	// Explicit secret retrieval has its own single-field schema.
	var out bytes.Buffer
	if e := runMode([]string{"server", "show-address", "--full"}, true, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	var addr map[string]string
	json.Unmarshal(out.Bytes(), &addr)
	if len(addr) != 1 || addr["address"] != full {
		t.Fatal("show-address contract")
	}
}

func TestCoreTimeoutIsBounded(t *testing.T) {
	setupCLI(t)
	path := strings.Split(os.Getenv("PATH"), ":")[0] + "/hc"
	if e := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0700); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := callCore(ctx, "machine", "ls")
	if e == nil || asCLIError(e).Kind != "timeout" || time.Since(start) > time.Second {
		t.Fatal("unbounded core command", e)
	}
}

func TestMalformedCoreListCannotAuthorizeRemoval(t *testing.T) {
	home := setupCLI(t)
	for _, body := range []string{"{}", "{\"machines\":null}", "[]"} {
		os.WriteFile(os.Getenv("HC_FAKE_LS"), []byte(body), 0600)
		r, e, _ := invoke(t, "profile", "rm", "p1", "--force")
		if e == nil || r["error"].(map[string]any)["kind"] != "core_error" {
			t.Fatal("malformed list accepted", r, e)
		}
		if !exists(filepath.Join(home, "profiles", "p1", "profile.json")) {
			t.Fatal("profile removed on unverified core list")
		}
	}
}

func TestServerRemovalNotFoundContract(t *testing.T) {
	setupCLI(t)
	for _, args := range [][]string{
		{"server", "allow", "rm", key.NewNode().Public().String()},
		{"server", "export", "rm", "tc-missing"},
	} {
		result, err, _ := invoke(t, args...)
		if err == nil || result["error"].(map[string]any)["kind"] != "not_found" {
			t.Fatal(result, err)
		}
	}
}
