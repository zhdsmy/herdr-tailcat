package main

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestJSONArgsScope(t *testing.T) {
	setupCLI(t)
	for _, args := range [][]string{{"server", "export", "ls"}, {"profile", "ls"}} {
		for pos := 1; pos <= len(args); pos++ {
			argv := append([]string{}, args[:pos]...)
			argv = append(argv, "--json")
			argv = append(argv, args[pos:]...)
			clean, mode := jsonArgs(argv)
			if !mode || !reflect.DeepEqual(clean, args) {
				t.Fatalf("argument scope: %v", argv)
			}
			var out bytes.Buffer
			if e := runMode(clean, mode, strings.NewReader(""), &out); e != nil || !strings.Contains(out.String(), "\"ok\":true") {
				t.Fatal(e, out.String())
			}
		}
	}
	for _, args := range [][]string{{"via", "p1", "sh -c 'hc --json connect'"}, {"daemon", "p1", "--json"}, {"serve", "x", "--json"}} {
		clean, mode := jsonArgs(args)
		if mode || !reflect.DeepEqual(clean, args) {
			t.Fatal("transport args changed")
		}
	}
	for _, cmd := range []string{"via", "serve", "daemon"} {
		args, mode := jsonArgs([]string{cmd, "--json"})
		var out bytes.Buffer
		if !mode || runMode(args, mode, strings.NewReader(""), &out) == nil || !strings.Contains(out.String(), "\"kind\":\"unsupported\"") {
			t.Fatal("foreground JSON", cmd, out.String())
		}
	}
}

func TestViaJSONWordExit255(t *testing.T) {
	if os.Getenv("TC_ARGS_CHILD") == "1" {
		os.Args = []string{"herdr-tailcat", "via", "p1", "sh -c 'hc --json connect'"}
		main()
		return
	}
	setupCLI(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestViaJSONWordExit255$")
	cmd.Env = append(os.Environ(), "TC_ARGS_CHILD=1")
	output, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 255 || !strings.Contains(string(output), "unsupported core command") {
		t.Fatalf("wrong via result: %v %q", err, output)
	}
}
