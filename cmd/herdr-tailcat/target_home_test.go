package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

func TestMain(m *testing.M) {
	if os.Getenv("TC_TARGET_HOME_PROBE") == "1" {
		h, err := store.Home()
		if err != nil {
			os.Exit(2)
		}
		fmt.Print(h)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func legacyTarget(t *testing.T, id string) string {
	t.Helper()
	b, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err = filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	b, err = filepath.Abs(b)
	if err != nil {
		t.Fatal(err)
	}
	return shellQuote(b) + " via " + shellQuote(id)
}
func TestDefaultTargetUnchanged(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	d := filepath.Join(h, ".local", "state", "herdr-tailcat")
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(h, "alias")
	if err := os.Symlink(d, alias); err != nil {
		t.Fatal(err)
	}
	t.Chdir(h)
	for _, override := range []string{"", d, alias, filepath.Join(".local", "state", "herdr-tailcat")} {
		t.Setenv("HERDR_TAILCAT_HOME", override)
		got, err := profileTarget("p1")
		if err != nil {
			t.Fatal(err)
		}
		if got != legacyTarget(t, "p1") {
			t.Fatal("default target differs byte-for-byte from legacy format")
		}
	}
}
func TestTargetBindsCanonicalHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Chdir(root)
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b space'quote")
	t.Setenv("HERDR_TAILCAT_HOME", a)
	ta, err := profileTarget("p1")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_TAILCAT_HOME", b)
	tb, err := profileTarget("p1")
	if err != nil {
		t.Fatal(err)
	}
	if ta == tb {
		t.Error("different homes share a target")
	}
	if err := os.MkdirAll(b, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a core permanently inheriting A, executing the saved B target.
	cmd := exec.Command("/bin/sh", "-c", tb+" probe-line")
	cmd.Env = append(os.Environ(), "HERDR_TAILCAT_HOME="+a, "TC_TARGET_HOME_PROBE=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != canonical {
		t.Error("saved target read core's inherited home instead of B")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(b, alias); err != nil {
		t.Fatal(err)
	}
	for _, override := range []string{alias, filepath.Base(b)} {
		t.Setenv("HERDR_TAILCAT_HOME", override)
		got, err := profileTarget("p1")
		if err != nil || got != tb {
			t.Fatal("noncanonical target", err)
		}
	}
	if matches(machine{Via: true, Target: ta}, tb) || matches(machine{Via: true, Target: legacyTarget(t, "p1")}, tb) {
		t.Fatal("foreign or unbound target owned by B")
	}
}
func TestMixedHomeTargetsFailClosed(t *testing.T) {
	home := setupCLI(t)
	t.Setenv("HOME", t.TempDir())
	b, err := profileTarget("p1")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_TAILCAT_HOME", filepath.Join(t.TempDir(), "a"))
	a, err := profileTarget("p1")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_TAILCAT_HOME", home)
	body := asString(map[string]any{"machines": []machine{{Name: "legacy", Via: true, Target: legacyTarget(t, "p1")}, {Name: "a", Via: true, Target: a}, {Name: "b", Via: true, Target: b}}})
	if err := os.WriteFile(os.Getenv("HC_FAKE_LS"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	r, _, err := profileManage(context.Background(), home, []string{"ls"}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	own := r["profiles"].([]map[string]any)[0]["machines"].([]map[string]any)
	if len(own) != 1 || own[0]["name"] != "b" {
		t.Error("mixed list includes unproved ownership")
	}
	for _, name := range []string{"a", "legacy"} {
		_, _, err := profileManage(context.Background(), home, []string{"unregister", "p1", "--name", name}, nil)
		if err == nil || asCLIError(err).Kind != "target_mismatch" {
			t.Error("foreign unregister accepted", name, err)
		}
	}
	_, _, err = profileManage(context.Background(), home, []string{"rm", "p1", "--force"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(os.Getenv("HC_FAKE_CALLS"))
	if strings.Contains(string(calls), "machine rm a\n") || strings.Contains(string(calls), "machine rm legacy\n") {
		t.Fatal("force removed unproved target")
	}
	if !strings.Contains(string(calls), "machine rm b\n") {
		t.Fatal("force failed to remove owned target")
	}
}

func TestProfileListCompleteIdentity(t *testing.T) {
	home := setupCLI(t)
	get := func() string {
		r, _, err := profileManage(context.Background(), home, []string{"ls"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		p := r["profiles"].([]map[string]any)[0]
		identity, _ := p["profile_identity"].(string)
		if len(identity) != 64 {
			t.Error("missing complete public profile identity")
		}
		return identity
	}
	first := get()
	// Reimport under the same id with a new client and server identity.
	if err := os.RemoveAll(filepath.Join(home, "profiles", "p1")); err != nil {
		t.Fatal(err)
	}
	writeIdentity(t, home, *tailcat.NewPrivateKey())
	if next := get(); first == next {
		t.Error("same id replacement kept identity")
	}
}

func TestRegisterPersistsHomeTarget(t *testing.T) {
	home := setupCLI(t)
	t.Setenv("HOME", t.TempDir())
	pingFixture(t, home)
	capture := filepath.Join(t.TempDir(), "target")
	t.Setenv("TC_TARGET_CAPTURE", capture)
	fake := filepath.Join(strings.Split(os.Getenv("PATH"), ":")[0], "hc")
	script := "#!/bin/sh\nif [ \"$3\" = add ]; then printf '%s' \"$5\" > \"$TC_TARGET_CAPTURE\"; printf '{\"name\":\"box\"}'; else printf '{\"machines\":[]}'; fi\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := profileManage(context.Background(), home, []string{"register", "p1", "--name", "box", "--session", "tc-work"}, nil); err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "a")
	cmd := exec.Command("/bin/sh", "-c", string(target)+" probe-line")
	cmd.Env = append(os.Environ(), "HERDR_TAILCAT_HOME="+other, "TC_TARGET_HOME_PROBE=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := store.CanonicalPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != canonical {
		t.Fatal("saved registration used inherited core namespace")
	}
	var p store.Profile
	if err := store.ReadJSON(filepath.Join(home, "profiles", "p1", "profile.json"), &p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(target), p.Address) {
		t.Fatal("address entered target")
	}
}
