// Package runstate ties private status files to a live process and held flock.
package runstate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"golang.org/x/sys/unix"
)

type State struct {
	PID      int    `json:"pid"`
	Started  string `json:"started_at"`
	Identity string `json:"identity"`
	Binary   string `json:"binary"`
	Digest   string `json:"config_digest,omitempty"`
	Streams  int64  `json:"streams"`
}

func Identity(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-p", fmtPID(pid), "-o", "lstart=,command=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	b, err := cmd.Output()
	if err != nil {
		return "", errors.New("process identity unavailable")
	}
	return strings.TrimSpace(string(b)), nil
}

func Acquire(dir, name string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("already running")
	}
	return f, nil
}

func New() (State, error) {
	identity, err := Identity(os.Getpid())
	if err != nil {
		return State{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return State{}, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	return State{PID: os.Getpid(), Started: time.Now().UTC().Format(time.RFC3339Nano), Identity: identity, Binary: binary}, err
}

func Read(dir, name string) (State, bool) {
	var s State
	if store.ReadJSON(filepath.Join(dir, name+".state.json"), &s) != nil || s.PID <= 1 {
		return s, false
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_RDWR, 0600)
	if err != nil {
		return s, false
	}
	defer f.Close()
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		return s, false
	}
	if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		return s, false
	}
	identity, err := Identity(s.PID)
	return s, err == nil && identity == s.Identity
}

func Write(dir, name string, s State) error {
	return store.WriteJSON(filepath.Join(dir, name+".state.json"), s)
}

func fmtPID(pid int) string { return strconv.Itoa(pid) }
