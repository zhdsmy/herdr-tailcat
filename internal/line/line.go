// Package line recognizes only the byte-exact connection template in hc v0.1.16–0.1.17.
package line

import "strings"

const prefix = `sh -c 'p=$(command -v herdr-core) || p=$HOME/.local/bin/herdr-core; [ -x "$p" ] || { echo "herdr-core is not installed here" >&2; exit 127; }; `
const suffix = `exec "$p" connect'`
const InstallError = "herdr-tailcat: remote install/update is not supported; update herdr-core on the server yourself"

type Kind int

const (
	Unsupported Kind = iota
	Connect
	EchoOK
)

type Result struct {
	Kind    Kind
	Session *string
	Reason  string
}

// ValidName mirrors addr::check_name, including base-30 terminal-id shapes.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 32 || !alphaNum(s[0]) {
		return false
	}
	for _, c := range []byte(s) {
		if !alphaNum(c) && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	if len(s) > 1 && s[0] == 't' && s[1] >= '1' && s[1] <= '9' {
		id := true
		for _, c := range []byte(s[1:]) {
			if !strings.ContainsRune("0123456789bcdfghjkmnpqrstvwxyz", rune(c)) {
				id = false
			}
		}
		if id {
			return false
		}
	}
	return true
}
func alphaNum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
func Template(session *string) string {
	if session == nil {
		return prefix + suffix
	}
	return prefix + "HC_SESSION=" + *session + " " + suffix
}
func Parse(s string) Result {
	if s == "echo ok" {
		return Result{Kind: EchoOK}
	}
	if s == Template(nil) {
		return Result{Kind: Connect}
	}
	if rest, ok := strings.CutPrefix(s, prefix+"HC_SESSION="); ok {
		if name, ok := strings.CutSuffix(rest, " "+suffix); ok && ValidName(name) {
			return Result{Kind: Connect, Session: &name}
		}
	}
	reason := "herdr-tailcat: unsupported core command (supported template: hc v0.1.16–0.1.17)"
	switch {
	case s == "uname -sm", s == `cmd /d /c "echo herdr-core-windows"`, strings.HasPrefix(s, "sh -c 'set -e; d=$HOME/.local/bin"):
		reason = InstallError
	case strings.HasPrefix(s, `cmd /d /c "if exist `):
		reason = "herdr-tailcat: Windows core connections are not supported"
	}
	return Result{Kind: Unsupported, Reason: reason}
}
