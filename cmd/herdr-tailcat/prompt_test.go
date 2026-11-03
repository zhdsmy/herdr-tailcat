package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/zhdsmy/herdr-tailcat/internal/serve"
	"golang.org/x/term"
)

func TestPromptChild(t *testing.T) {
	if os.Getenv("TC_PROMPT_CHILD") != "1" {
		return
	}
	os.Args = []string{"herdr-tailcat", "profile", "add", "imported", "--prompt"}
	main()
	os.Exit(0)
}

type ttyCapture struct {
	sync.Mutex
	data []byte
}

func (c *ttyCapture) Write(b []byte) (int, error) {
	c.Lock()
	defer c.Unlock()
	c.data = append(c.data, b...)
	return len(b), nil
}
func (c *ttyCapture) bytes() []byte { c.Lock(); defer c.Unlock(); return bytes.Clone(c.data) }
func waitPromptText(t *testing.T, c *ttyCapture, text string) {
	t.Helper()
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		if bytes.Contains(c.bytes(), []byte(text)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prompt did not reach expected stage")
}

func assertNoInput(t *testing.T, out, input []byte) {
	t.Helper()
	if len(input) < 16 {
		t.Fatal("fixture too short")
	}
	for i := 0; i+16 <= len(input); i++ {
		if bytes.Contains(out, input[i:i+16]) {
			t.Fatal("input leaked to PTY output")
		}
	}
}
func TestPromptPTY(t *testing.T) {
	for _, test := range []string{"valid", "invalid", "oversize", "ctrl-c", "sigterm", "sighup", "sigint", "sigquit", "sigtstp", "ctrl-d-z"} {
		t.Run(test, func(t *testing.T) {
			home := setupCLI(t)
			address, e := serve.Address(home, true)
			if e != nil {
				t.Fatal(e)
			}
			master, slave, e := pty.Open()
			if e != nil {
				t.Fatal(e)
			}
			defer master.Close()
			defer slave.Close()
			old, e := term.GetState(int(slave.Fd()))
			if e != nil {
				t.Fatal(e)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestPromptChild$")
			cmd.Env = append(os.Environ(), "TC_PROMPT_CHILD=1")
			cmd.Stdin = slave
			cmd.Stdout = slave
			cmd.Stderr = slave
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			done := false
			defer func() {
				if !done {
					cmd.Process.Kill()
					<-exited
				}
			}()
			capture := new(ttyCapture)
			collected := make(chan struct{})
			go func() { io.Copy(capture, master); close(collected) }()
			waitPromptText(t, capture, "input is hidden")
			raw, e := term.GetState(int(slave.Fd()))
			if e != nil || reflect.DeepEqual(old, raw) {
				t.Fatal("TTY did not become raw")
			}
			input := []byte(address)
			switch test {
			case "valid":
				fmt.Fprintf(master, "\x1b[200~%s\x1b[201~\r", input)
			case "ctrl-d-z":
				fmt.Fprintf(master, "\x04\x1a\x1c\x13\x11\x1b[200~%s\x1b[201~\r", input)
			case "invalid":
				input = []byte("invalid-address-sensitive-fixture-should-never-echo")
				master.Write(append(bytes.Clone(input), '\r'))
			case "oversize":
				input = []byte(strings.Repeat("x", 16385))
				master.Write(append(bytes.Clone(input), '\r'))
			case "ctrl-c":
				master.Write(append([]byte(address[:24]), 3))
			default:
				master.Write([]byte(address[:24]))
				sig := syscall.SIGTERM
				if test == "sighup" {
					sig = syscall.SIGHUP
				}
				if test == "sigint" {
					sig = syscall.SIGINT
				}
				if test == "sigquit" {
					sig = syscall.SIGQUIT
				}
				if test == "sigtstp" {
					sig = syscall.SIGTSTP
				}
				// This is our exact child PID, created immediately above, never a core.
				if e = cmd.Process.Signal(sig); e != nil {
					t.Fatal(e)
				}
			}
			if test == "valid" || test == "invalid" || test == "oversize" || test == "ctrl-d-z" {
				waitPromptText(t, capture, "Press Return")
				master.Write([]byte{'\r'})
			}
			select {
			case e = <-exited:
				done = true
			case <-time.After(8 * time.Second):
				t.Fatal("prompt child did not exit")
			}
			good := test == "valid" || test == "ctrl-d-z"
			if (e == nil) != good {
				t.Fatal("unexpected prompt exit", e)
			}
			current, e := term.GetState(int(slave.Fd()))
			if e != nil || !reflect.DeepEqual(old, current) {
				t.Fatal("termios was not restored", e)
			}
			slave.Close()
			select {
			case <-collected:
			case <-time.After(time.Second):
				master.Close()
				<-collected
			}
			assertNoInput(t, capture.bytes(), input)
			if exists(filepath.Join(home, "profiles", "imported", "profile.json")) != good {
				t.Fatal("unexpected profile creation")
			}
			if good && (!bytes.Contains(capture.bytes(), []byte("Client public key: nodekey:")) || !bytes.Contains(capture.bytes(), []byte("restart serve"))) {
				t.Fatal("success instructions missing")
			}
		})
	}
}
func TestPromptRejectsPipeAndJSON(t *testing.T) {
	setupCLI(t)
	var out bytes.Buffer
	if e := runMode([]string{"profile", "add", "imported", "--prompt"}, false, strings.NewReader("secret-not-tty"), &out); e == nil || asCLIError(e).Kind != "invalid_arguments" {
		t.Fatal("accepted non TTY")
	}
	if out.Len() != 0 {
		t.Fatal("read or printed pipe input")
	}
	if e := runMode([]string{"profile", "add", "imported", "--prompt"}, true, strings.NewReader("secret-not-tty"), &out); e == nil || !strings.Contains(out.String(), "invalid_arguments") {
		t.Fatal("accepted JSON prompt")
	}
}

// A user-generated SIGPIPE is ignored by Go on Darwin (runtime/signal_unix.go,
// issue 33384). A closed stdout pipe tests the actual failure we must handle.
func TestPromptBrokenStdoutRestoresTTY(t *testing.T) {
	setupCLI(t)
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	old, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	defer writer.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPromptChild$")
	cmd.Env = append(os.Environ(), "TC_PROMPT_CHILD=1")
	cmd.Stdin = slave
	cmd.Stdout = writer
	cmd.Stderr = slave
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err = <-exited:
		if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 {
			t.Fatal("unexpected broken-stdout exit", err)
		}
	case <-time.After(8 * time.Second):
		cmd.Process.Kill()
		<-exited
		t.Fatal("broken stdout did not terminate prompt")
	}
	current, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(old, current) {
		t.Fatal("broken stdout left TTY raw", err)
	}
}
