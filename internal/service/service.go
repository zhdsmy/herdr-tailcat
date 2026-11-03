// Package service generates service files. Loading a service is an explicit
// operator action, separate from generation.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type Options struct {
	OS, Label, Binary, Home string
	Core                    string
	ForceDERP               bool
	Env                     map[string]string
}

func (o Options) Logs() (string, string) {
	return filepath.Join(o.Home, "service.stdout.log"), filepath.Join(o.Home, "service.stderr.log")
}

func Render(o Options) ([]byte, error) {
	if o.Label == "" || strings.Trim(o.Label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_") != "" {
		return nil, errors.New("service label must use letters, digits, dot, hyphen or underscore")
	}
	if !filepath.IsAbs(o.Binary) || !filepath.IsAbs(o.Home) || !filepath.IsAbs(o.Core) {
		return nil, errors.New("service binary, core and home must be absolute paths")
	}
	env := map[string]string{"HERDR_TAILCAT_HOME": o.Home, "PATH": filepath.Dir(o.Core) + ":/usr/bin:/bin:/usr/sbin:/sbin"}
	if o.ForceDERP {
		env["TS_DEBUG_ALWAYS_USE_DERP"] = "true"
	}
	for _, k := range []string{"HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "HC_SESSION"} {
		if v, ok := o.Env[k]; ok {
			env[k] = v
		}
	}
	for _, k := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		v, ok := env[k]
		if !ok || (v != "" && !filepath.IsAbs(v)) {
			return nil, fmt.Errorf("service requires absolute %s (runtime may be empty)", k)
		}
		if v == "" && k != "XDG_RUNTIME_DIR" {
			return nil, fmt.Errorf("service requires %s", k)
		}
	}
	for _, v := range append([]string{o.Binary, o.Home}, values(env)...) {
		if strings.ContainsFunc(v, unicode.IsControl) {
			return nil, errors.New("service paths and environment cannot contain control characters")
		}
	}
	out, errlog := o.Logs()
	switch o.OS {
	case "darwin":
		var b bytes.Buffer
		b.WriteString(xml.Header + "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n")
		plist(&b, map[string]any{"Label": o.Label, "ProgramArguments": []string{o.Binary, "serve"}, "EnvironmentVariables": env,
			"RunAtLoad": true, "KeepAlive": map[string]any{"SuccessfulExit": false}, "AbandonProcessGroup": true,
			"ProcessType": "Background", "ExitTimeOut": 10, "StandardOutPath": out, "StandardErrorPath": errlog})
		b.WriteString("\n</plist>\n")
		return b.Bytes(), nil
	case "linux":
		var b strings.Builder
		b.WriteString("# core client.rs:181-184 attempts its own systemd user startup.\n# core/holder use a separate unit; do not kill the serve cgroup. Linux runtime untested.\n[Unit]\nDescription=Herdr Tailcat server\n\n[Service]\nType=simple\n")
		fmt.Fprintf(&b, "ExecStart=%s serve\nKillMode=process\nRestart=on-failure\nRestartSec=1\nUMask=0077\n", unitQuote(o.Binary, true))
		for _, k := range keys(env) {
			fmt.Fprintf(&b, "Environment=%s\n", unitQuote(k+"="+env[k], false))
		}
		b.WriteString("StandardOutput=journal\nStandardError=journal\n\n[Install]\nWantedBy=default.target\n")
		return []byte(b.String()), nil
	default:
		return nil, errors.New("services support macOS and Linux only")
	}
}

func keys[T any](m map[string]T) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
func values(m map[string]string) []string {
	var v []string
	for _, s := range m {
		v = append(v, s)
	}
	return v
}
func escaped(b *bytes.Buffer, s string) { _ = xml.EscapeText(b, []byte(s)) }
func plist(b *bytes.Buffer, v any) {
	switch v := v.(type) {
	case string:
		b.WriteString("<string>")
		escaped(b, v)
		b.WriteString("</string>\n")
	case bool:
		if v {
			b.WriteString("<true/>\n")
		} else {
			b.WriteString("<false/>\n")
		}
	case int:
		b.WriteString("<integer>" + strconv.Itoa(v) + "</integer>\n")
	case []string:
		b.WriteString("<array>\n")
		for _, s := range v {
			plist(b, s)
		}
		b.WriteString("</array>\n")
	case map[string]string:
		m := map[string]any{}
		for k, s := range v {
			m[k] = s
		}
		plist(b, m)
	case map[string]any:
		b.WriteString("<dict>\n")
		for _, k := range keys(v) {
			b.WriteString("<key>")
			escaped(b, k)
			b.WriteString("</key>")
			plist(b, v[k])
		}
		b.WriteString("</dict>\n")
	}
}
func unitQuote(s string, exec bool) string {
	s = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%").Replace(s)
	if exec {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	return "\"" + s + "\""
}

// WritePlist creates private logs before launchd can open them. It never loads
// a job or writes to another service directory unless the caller chose it.
func WritePlist(o Options, dir string, data []byte) (string, error) {
	if o.OS != "darwin" {
		return "", errors.New("Linux units are text-only; no installation")
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("plist directory must be absolute")
	}
	if err := os.MkdirAll(o.Home, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(o.Home, 0700); err != nil {
		return "", err
	}
	out, errlog := o.Logs()
	for _, p := range []string{out, errlog} {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		err = f.Chmod(0600)
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".plist-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err = errors.Join(werr, serr, cerr); err != nil {
		return "", err
	}
	p := filepath.Join(dir, o.Label+".plist")
	return p, os.Rename(f.Name(), p)
}
