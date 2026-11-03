package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/line"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
)

func validName(s string) bool    { return line.ValidName(s) }
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func profileTarget(id string) (string, error) {
	b, e := os.Executable()
	if e != nil {
		return "", e
	}
	b, e = filepath.EvalSymlinks(b)
	if e != nil {
		return "", e
	}
	b, e = filepath.Abs(b)
	if e != nil {
		return "", e
	}
	return shellQuote(b) + " via " + shellQuote(id), nil
}

// Never copy core target strings into result objects or errors.
type machine struct {
	Name    string  `json:"name"`
	ID      string  `json:"id"`
	Host    string  `json:"host"`
	Version string  `json:"version"`
	Session *string `json:"session"`
	Target  string  `json:"target"`
	Via     bool    `json:"via"`
	State   string  `json:"state"`
	Why     string  `json:"why"`
}

func (m machine) view() map[string]any {
	var session *string
	if m.Session != nil {
		s := redact.Clean(*m.Session)
		session = &s
	}
	return map[string]any{"name": redact.Clean(m.Name), "id": redact.Clean(m.ID), "host": redact.Clean(m.Host), "version": redact.Clean(m.Version), "session": session, "state": redact.Clean(m.State), "why": redact.Clean(m.Why)}
}
func matches(m machine, target string) bool { return m.Via && m.Target == target }

// Enumerate the current core's stable RPC kinds; unknown kinds become core_error.
var coreKinds = map[string]bool{}

func init() {
	for _, k := range strings.Fields("invalid_params not_found name_taken already_added unreachable io internal cancelled timeout protocol version_mismatch permission_denied unsupported conflict busy invalid_request method_not_found denied edit_failed hc_missing incompatible install_failed invalid_template live needs_framed needs_login needs_update not_text refused spawn_failed unsupported_generation parse_error usage machine_unknown unavailable needs_target config blocked status_mismatch finished unknown_method") {
		coreKinds[k] = true
	}
}
func callCore(ctx context.Context, args ...string) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, "hc", append([]string{"--json"}, args...)...)
	cmd.WaitDelay = 200 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fail("timeout", "command exceeded its time budget", "")
	}
	if err != nil {
		for _, b := range [][]byte{stderr.Bytes(), stdout.Bytes()} {
			var r struct {
				Error cliError `json:"error"`
			}
			if json.Unmarshal(b, &r) == nil && r.Error.Kind != "" {
				kind := "core_error"
				if coreKinds[r.Error.Kind] {
					kind = "core:" + r.Error.Kind
				}
				return nil, fail(kind, r.Error.Message, r.Error.Hint)
			}
		}
		var x *exec.Error
		if errors.As(err, &x) {
			return nil, fail("core_unavailable", "cannot execute hc", "Install herdr-core and add hc to PATH.")
		}
		return nil, fail("core_error", "hc failed without a valid JSON error", "")
	}
	if !json.Valid(stdout.Bytes()) {
		return nil, fail("core_error", "hc returned invalid JSON", "")
	}
	return stdout.Bytes(), nil
}
func machines(ctx context.Context) ([]machine, error) {
	b, e := callCore(ctx, "machine", "ls")
	if e != nil {
		return nil, e
	}
	var result struct {
		Machines []machine `json:"machines"`
	}
	if json.Unmarshal(b, &result) != nil || result.Machines == nil {
		return nil, fail("core_error", "unexpected machine list shape", "")
	}
	return result.Machines, nil
}
func matching(ms []machine, target string) []machine {
	out := []machine{}
	for _, m := range ms {
		if matches(m, target) {
			out = append(out, m)
		}
	}
	return out
}
