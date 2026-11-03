package service

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func options(t *testing.T) Options {
	t.Helper()
	home := t.TempDir()
	return Options{OS: "darwin", Label: "dev.herdr-tailcat.test.unit", Binary: "/opt/Herdr & Tools/herdr-tailcat", Core: "/opt/core/herdr-core", Home: home,
		Env: map[string]string{"XDG_STATE_HOME": home + "/state", "XDG_CONFIG_HOME": home + "/config", "XDG_RUNTIME_DIR": home + "/runtime", "SECRET": "must-not-appear"}}
}

func TestServiceDoesNotInheritDebugOrPath(t *testing.T) {
	for _, target := range []string{"darwin", "linux"} {
		o := options(t)
		o.OS = target
		o.Env["PATH"] = "/unrelated/very-long-secret-path"
		o.Env["TS_DEBUG_ALWAYS_USE_DERP"] = "true"
		o.Env["TS_DEBUG_OTHER"] = "debug-value"
		data, err := Render(o)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"TS_DEBUG", "unrelated", "debug-value"} {
			if strings.Contains(string(data), bad) {
				t.Fatal("inherited debug environment", bad)
			}
		}
		if !strings.Contains(string(data), "/opt/core:/usr/bin:/bin:/usr/sbin:/sbin") {
			t.Fatal("unexpected PATH")
		}
		o.ForceDERP = true
		data, err = Render(o)
		if err != nil || !strings.Contains(string(data), "TS_DEBUG_ALWAYS_USE_DERP") {
			t.Fatal("missing explicit DERP setting", err)
		}
	}
}
func TestLaunchdTextAndPrivateFiles(t *testing.T) {
	o := options(t)
	data, err := Render(o)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	text := string(data)
	for _, want := range []string{"/opt/Herdr &amp; Tools/herdr-tailcat", "<string>serve</string>", "HERDR_TAILCAT_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "SuccessfulExit", "<false/>", "AbandonProcessGroup"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing plist setting", want)
		}
	}
	if strings.Contains(text, "must-not-appear") {
		t.Fatal("unexpected environment copied")
	}
	dir := filepath.Join(t.TempDir(), "plists")
	path, err := WritePlist(o, dir, data)
	if err != nil {
		t.Fatal(err)
	}
	out, errlog := o.Logs()
	for _, p := range []string{path, out, errlog} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private service file required", p, info, err)
		}
	}
}
func TestLinuxIsProcessOnlyAndEscaped(t *testing.T) {
	o := options(t)
	o.OS = "linux"
	o.Binary = "/opt/Herdr $percent%/a" + string('"') + "b"
	data, err := Render(o)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"KillMode=process", "Restart=on-failure", "UMask=0077", "core client.rs:181-184", "separate unit", "ExecStart=" + unitQuote(o.Binary, true) + " serve"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing unit policy", want)
		}
	}
	if !strings.Contains(text, "$$percent%%") {
		t.Fatal("unit specifier or dollar expansion", text)
	}
	if _, err := WritePlist(o, t.TempDir(), data); err == nil {
		t.Fatal("Linux must remain text-only")
	}
}
func TestRejectUnsafeOptions(t *testing.T) {
	for _, change := range []func(*Options){
		func(o *Options) { o.Label = "../other" }, func(o *Options) { o.Binary = "relative" },
		func(o *Options) { o.Home = "relative" }, func(o *Options) { o.Env["XDG_STATE_HOME"] = "relative" },
		func(o *Options) { o.Env["XDG_CONFIG_HOME"] = "" }, func(o *Options) { o.Binary += string(byte(10)) },
	} {
		o := options(t)
		change(&o)
		if _, err := Render(o); err == nil {
			t.Fatal("unsafe options accepted")
		}
	}
}
