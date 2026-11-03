package line

import (
	"os"
	"strings"
	"testing"
)

func TestActualFixtures(t *testing.T) {
	for _, version := range []string{"v0.1.16", "v0.1.17"} {
		for _, name := range []string{"default", "work"} {
			b, e := os.ReadFile("../../testdata/lines/" + version + "/" + name + ".txt")
			if e != nil {
				t.Fatal(e)
			}
			text := strings.TrimSuffix(string(b), "\n")
			q := Parse(text)
			if q.Kind != Connect {
				t.Fatalf("fixture rejected: %s", name)
			}
			var s *string
			if name == "work" {
				s = &name
			}
			if text != Template(s) {
				t.Fatal("source-derived template differs from real capture")
			}
			for _, bad := range []string{strings.Replace(text, "sh -c", "sh  -c", 1), strings.Replace(text, "'", "\"", 1), text + "; rm -rf /", strings.Replace(text, "connect'", "connect '", 1)} {
				if Parse(bad).Kind != Unsupported {
					t.Fatalf("accepted mutation %q", bad)
				}
			}
		}
	}
}
func TestSessionNames(t *testing.T) {
	for _, s := range []string{"", "a;b", "WORK", "t7", "t1b", "t9zzzzzzzzzz", "../work", strings.Repeat("a", 33)} {
		if ValidName(s) {
			t.Fatal(s)
		}
		if Parse(Template(&s)).Kind != Unsupported {
			t.Fatal("accepted invalid session", s)
		}
	}
	for _, s := range []string{"work", "tc-work", "t0", "t3po", "t1l", "a.b_1"} {
		if !ValidName(s) {
			t.Fatal(s)
		}
	}
}
func TestUnsupportedClasses(t *testing.T) {
	for _, s := range []string{"uname -sm", `cmd /d /c "echo herdr-core-windows"`, "sh -c 'set -e; d=$HOME/.local/bin; malicious'"} {
		if Parse(s).Reason != InstallError {
			t.Fatal(s)
		}
	}
	if Parse(`cmd /d /c "if exist anything"`).Reason == InstallError {
		t.Fatal("windows connection misclassified")
	}
	if Parse("echo ok").Kind != EchoOK {
		t.Fatal("diagnostic rejected")
	}
}
