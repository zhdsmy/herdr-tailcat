package version

import "testing"

func TestBuildOverride(t *testing.T) {
	old := Build
	defer func() { Build = old }()
	Build = "v2.3.4-test"
	if String() != Build {
		t.Fatal("build injection ignored")
	}
	Build = ""
	if String() == "" {
		t.Fatal("missing provenance")
	}
}
