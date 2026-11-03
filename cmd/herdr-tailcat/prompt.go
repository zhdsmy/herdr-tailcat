package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/daemon"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var errPromptReported = errors.New("interactive profile import failed")
var errPromptCancel = errors.New("cancelled")

func wantsPrompt(args []string) bool {
	return len(args) > 1 && args[0] == "profile" && args[1] == "add" && slices.Contains(args[2:], "--prompt")
}

// rawInput never sends received bytes to any writer. Poll keeps signal handling
// and the five-minute bound independent of the TTY's blocking read behaviour.
type rawInput struct {
	fd      int
	signals <-chan os.Signal
}

func (r rawInput) next(deadline time.Time) (byte, error) {
	for time.Now().Before(deadline) {
		select {
		case <-r.signals:
			return 0, errPromptCancel
		default:
		}
		fds := []unix.PollFd{{Fd: int32(r.fd), Events: unix.POLLIN}}
		n, e := unix.Poll(fds, 50)
		if e == unix.EINTR {
			continue
		}
		if e != nil {
			return 0, io.EOF
		}
		if n == 0 {
			continue
		}
		var b [1]byte
		n, e = unix.Read(r.fd, b[:])
		if e == unix.EINTR {
			continue
		}
		if e != nil || n != 1 {
			return 0, io.EOF
		}
		if b[0] == 3 {
			return 0, errPromptCancel
		}
		return b[0], nil
	}
	return 0, errors.New("input timed out")
}
func (r rawInput) line(limit int) ([]byte, error) {
	deadline := time.Now().Add(5 * time.Minute)
	b := make([]byte, 0, 256)
	esc := ""
	for {
		c, e := r.next(deadline)
		if e != nil {
			clear(b)
			return nil, e
		}
		if esc != "" || c == 27 {
			esc += string(c)
			if esc == "\x1b[200~" || esc == "\x1b[201~" {
				esc = ""
				continue
			}
			if strings.HasPrefix("\x1b[200~", esc) || strings.HasPrefix("\x1b[201~", esc) {
				continue
			}
			clear(b)
			return nil, errors.New("unsupported input sequence")
		}
		if c == '\r' || c == '\n' {
			return b, nil
		}
		// In raw mode Ctrl-D/Z/backslash and flow-control bytes cannot signal or
		// suspend the process. Only Ctrl-C aborts; backspace edits without echo.
		if c == 127 || c == 8 {
			if len(b) > 0 {
				b[len(b)-1] = 0
				b = b[:len(b)-1]
			}
			continue
		}
		if c < 32 {
			continue
		}
		if limit == 0 {
			continue
		}
		if len(b) == limit {
			clear(b)
			return nil, errors.New("address input exceeds 16 KiB")
		}
		b = append(b, c)
	}
}

func promptProfile(args []string, in io.Reader, out io.Writer) error {
	if len(args) != 4 || args[3] != "--prompt" || !validName(args[2]) {
		return usage("usage: profile add <id> --prompt")
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return usage("--prompt requires a TTY on stdin")
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTSTP, syscall.SIGPIPE)
	defer signal.Stop(signals)
	fd := int(f.Fd())
	old, e := term.MakeRaw(fd)
	if e != nil {
		return usage("could not disable terminal echo")
	}
	// Restore before returning to main, including failures, cancellation and panic.
	defer term.Restore(fd, old)
	r := rawInput{fd, signals}
	if _, e = fmt.Fprint(out, "Paste the server address, then press Return (input is hidden):\r\n"); e != nil {
		return errPromptReported
	}
	b, e := r.line(16384)
	defer clear(b)
	if errors.Is(e, errPromptCancel) {
		fmt.Fprint(out, "Cancelled.\r\n")
		return errPromptReported
	}
	var public string
	if e == nil {
		var home string
		home, e = store.Home()
		if e == nil {
			dir := filepath.Join(home, "profiles", args[2])
			e = daemon.AddProfile(dir, string(b))
			if e == nil {
				public, e = daemon.PublicKey(dir)
			}
		}
	}
	clear(b)
	if e != nil {
		// Never forward errors containing external input, paths or terminal bytes.
		message := "Could not import profile; check the address and file permissions."
		switch {
		case strings.Contains(e.Error(), "exceeds"):
			message = "Address input exceeds 16 KiB."
		case strings.Contains(e.Error(), "already exists"):
			message = "Profile already exists."
		case strings.Contains(e.Error(), "invalid tailcat"):
			message = "Invalid server address."
		case strings.Contains(e.Error(), "disco key"):
			message = "Server address lacks a separate disco key."
		case strings.Contains(e.Error(), "preshared key"):
			message = "Server address lacks a PSK."
		case strings.Contains(e.Error(), "timed out"):
			message = "Input timed out."
		}
		fmt.Fprint(out, message+"\r\n")
	} else {
		fmt.Fprintf(out, "Profile added.\r\nClient public key: %s\r\nOn the server, run: herdr-tailcat server allow add %s and restart serve.\r\n", public, public)
	}
	fmt.Fprint(out, "Press Return to close (automatic close after 5 minutes).\r\n")
	_, waitErr := r.line(0)
	if e != nil || waitErr != nil {
		return errPromptReported
	}
	return nil
}
