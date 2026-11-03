package allow

import (
	"os"
	"path/filepath"
	"testing"

	"tailscale.com/types/key"
)

func TestFailClosed(t *testing.T) {
	k := key.NewNode().Public().String()
	for name, text := range map[string]string{"missing": "", "empty": "", "comments": "# only\n", "invalid": k + "\nno-key\n", "duplicate": k + "\n" + k + "\n", "unreadable": k} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "allowed_clients")
			if name != "missing" {
				if e := os.WriteFile(p, []byte(text), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if name == "unreadable" {
				os.Chmod(p, 0000)
			}
			if _, e := Load(p); e == nil {
				t.Fatal("must reject")
			}
		})
	}
	p := filepath.Join(t.TempDir(), "allowed_clients")
	os.WriteFile(p, []byte("# clients\n"+k+"\n"), 0600)
	got, e := Load(p)
	if e != nil || len(got) != 1 {
		t.Fatal(e)
	}
}
