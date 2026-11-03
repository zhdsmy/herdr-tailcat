package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var validLabel = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func serviceManage(ctx context.Context, args []string) (map[string]any, *string, error) {
	if len(args) == 0 {
		return nil, nil, usage("usage: server service start|stop|status")
	}
	f := flags("service")
	label := f.String("label", "dev.herdr.tailcat", "")
	dir := f.String("plist-dir", "", "")
	if e := parse(f, args[1:]); e != nil {
		return nil, nil, e
	}
	v, e := serviceControl(ctx, args[0], *label, *dir)
	return v, nil, e
}
func serviceControl(ctx context.Context, action, label, dir string) (map[string]any, error) {
	if runtime.GOOS != "darwin" {
		return nil, fail("unsupported", "server service is supported only on macOS", "")
	}
	if !validLabel.MatchString(label) {
		return nil, usage("invalid service label")
	}
	if action != "start" && action != "stop" && action != "status" {
		return nil, usage("unknown service action")
	}
	// status and stop do not need to read a plist (including the default directory).
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + label
	if action == "start" {
		if dir == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				return nil, e
			}
			dir = filepath.Join(home, "Library", "LaunchAgents")
		}
		path, e := filepath.Abs(filepath.Join(dir, label+".plist"))
		if e != nil {
			return nil, e
		}
		if !exists(path) {
			return nil, fail("not_found", "generated service plist does not exist", "")
		}
		if e = launch(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), path); e != nil {
			return nil, e
		}
	} else if action == "stop" {
		if e := launch(ctx, "bootout", target); e != nil {
			return nil, e
		}
		// launchctl bootout may return before print stops seeing the old job.
		for i := 0; i < 100; i++ {
			loaded, _, e := loadedService(ctx, target)
			if e != nil {
				return nil, e
			}
			if !loaded {
				break
			}
			select {
			case <-ctx.Done():
				return nil, fail("timeout", "service stop timed out", "")
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	loaded, pid, e := loadedService(ctx, target)
	if e != nil {
		return nil, e
	}
	if action == "stop" && loaded {
		return nil, fail("service_error", "service is still loaded", "")
	}
	return map[string]any{"loaded": loaded, "pid": pid, "label": label}, nil
}
func launch(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	b, e := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fail("timeout", "launchctl timed out", "")
	}
	if e != nil {
		return fail("service_error", "launchctl failed: "+strings.TrimSpace(string(b)), "")
	}
	return nil
}

var pidLine = regexp.MustCompile(`(?m)^	* *pid = ([0-9]+)	* *$`)

func loadedService(ctx context.Context, target string) (bool, int, error) {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "print", target)
	b, e := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return false, 0, fail("timeout", "launchctl status timed out", "")
	}
	if e != nil {
		var x *exec.ExitError
		if errors.As(e, &x) && (strings.Contains(string(b), "Could not find service") || strings.Contains(string(b), "service not found")) {
			return false, 0, nil
		}
		return false, 0, fail("service_error", "launchctl print failed: "+strings.TrimSpace(string(b)), "")
	}
	pid := 0
	if m := pidLine.FindSubmatch(b); len(m) > 1 {
		pid, _ = strconv.Atoi(string(m[1]))
	}
	return true, pid, nil
}
