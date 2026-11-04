package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/daemon"
	"github.com/zhdsmy/herdr-tailcat/internal/peer"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/runstate"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"github.com/zhdsmy/herdr-tailcat/internal/via"
)

func profilePing(ctx context.Context, home, id string, limit time.Duration) (*proto.Info, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	info, rtt, e := via.Ping(ctx, home, id)
	if e != nil {
		return nil, 0, fail("no_answer", e.Error(), "Check that serve is online and this client public key is allowed.")
	}
	for i, s := range info.Exports {
		info.Exports[i] = redact.Clean(s)
	}
	info.Version = redact.Clean(info.Version)
	return info, rtt, nil
}
func profileManage(ctx context.Context, home string, args []string, in io.Reader) (map[string]any, *string, error) {
	result := map[string]any{}
	if args[0] == "ls" {
		if len(args) != 1 {
			return nil, nil, usage("usage: profile ls")
		}
		ms, e := machines(ctx)
		if e != nil {
			return nil, nil, e
		}
		entries, e := os.ReadDir(filepath.Join(home, "profiles"))
		if e != nil && !os.IsNotExist(e) {
			return nil, nil, e
		}
		profiles := []map[string]any{}
		for _, entry := range entries {
			if !entry.IsDir() || !validName(entry.Name()) {
				continue
			}
			id := entry.Name()
			dir, e := profileDir(home, id)
			if e != nil {
				return nil, nil, e
			}
			view, e := func() (map[string]any, error) {
				lock, e := store.ProfileLock(ctx, dir)
				if e != nil {
					return nil, e
				}
				defer lock.Close()
				var p store.Profile
				if e = store.ReadJSON(filepath.Join(dir, "profile.json"), &p); e != nil {
					return nil, e
				}
				ci, e := tailcat.ParseAddr(tailcat.Addr(p.Address))
				if e != nil {
					return nil, fail("invalid_config", "invalid profile address", "")
				}
				pk, e := daemon.PublicKey(dir)
				if e != nil {
					return nil, e
				}
				target, e := profileTarget(id)
				if e != nil {
					return nil, e
				}
				views := []map[string]any{}
				for _, m := range matching(ms, target) {
					views = append(views, m.view())
				}
				_, running := runstate.Read(dir, "daemon")
				return map[string]any{"id": id, "profile_identity": fmt.Sprintf("%x", sha256.Sum256([]byte(pk+"\x00"+ci.ServerPublic.NodePublic.String()+"\x00"+ci.ServerDiscoPublic.String()))), "client_fingerprint": fingerprint(pk), "server_fingerprint": fingerprint(ci.ServerPublic.NodePublic.String()), "daemon_running": running, "machines": views}, nil
			}()
			if e != nil {
				return nil, nil, e
			}
			profiles = append(profiles, view)
		}
		return map[string]any{"profiles": profiles}, nil, nil
	}
	if len(args) < 2 {
		return nil, nil, usage("missing profile id")
	}
	id := args[1]
	if !validName(id) {
		return nil, nil, usage("invalid profile id")
	}
	if args[0] == "add" {
		if len(args) != 2 {
			return nil, nil, usage("usage: profile add <id> (address from stdin)")
		}
		dir := filepath.Join(home, "profiles", id)
		if exists(filepath.Join(dir, "profile.json")) {
			return nil, nil, fail("already_exists", "profile already exists", "")
		}
		b, e := io.ReadAll(io.LimitReader(in, 16385))
		if e != nil {
			return nil, nil, e
		}
		if len(b) > 16384 {
			return nil, nil, usage("address input exceeds 16 KiB")
		}
		if e = daemon.AddProfileContext(ctx, dir, string(b)); e != nil {
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return nil, nil, e
			}
			if errors.Is(e, daemon.ErrProfileExists) {
				return nil, nil, fail("already_exists", "profile already exists", "")
			}
			return nil, nil, fail("invalid_address", e.Error(), "")
		}
		return result, nil, nil
	}
	dir, e := profileDir(home, id)
	if e != nil {
		return nil, nil, e
	}
	switch args[0] {
	case "pubkey":
		if len(args) != 2 {
			return nil, nil, usage("usage: profile pubkey <id>")
		}
		k, e := daemon.PublicKey(dir)
		return map[string]any{"public_key": k, "fingerprint": fingerprint(k)}, ptr(k), e
	case "test":
		if len(args) != 2 {
			return nil, nil, usage("usage: profile test <id>")
		}
		info, rtt, e := profilePing(ctx, home, id, 18*time.Second)
		if e != nil {
			return nil, nil, e
		}
		return map[string]any{"rtt_ms": float64(rtt.Microseconds()) / 1000, "server": info}, nil, nil
	case "register":
		f := flags("profile register")
		name := f.String("name", "", "")
		session := f.String("session", "", "")
		temp := f.Bool("temp", false, "")
		if e = parse(f, args[2:]); e != nil {
			return nil, nil, e
		}
		if !validName(*name) || (*session != "" && !validName(*session)) {
			return nil, nil, usage("register requires --name and valid name/session")
		}
		info, _, e := profilePing(ctx, home, id, 4*time.Second)
		if e != nil {
			return nil, nil, e
		}
		requested := *session
		if requested == "" {
			requested = "default"
		}
		found := false
		for _, s := range info.Exports {
			if s == requested {
				found = true
			}
		}
		if !found {
			return nil, nil, fail("session_not_exported", "requested session is not exported", "Available sessions: "+strings.Join(info.Exports, ", "))
		}
		target, e := profileTarget(id)
		if e != nil {
			return nil, nil, e
		}
		a := []string{"machine", "add", "--via", target, "--name", *name}
		if *session != "" {
			a = append(a, "--session", *session)
		}
		if *temp {
			a = append(a, "--temp")
		}
		b, e := callCore(ctx, a...)
		if e != nil {
			return nil, nil, e
		}
		var m machine
		if json.Unmarshal(b, &m) != nil {
			return nil, nil, fail("core_error", "unexpected machine result shape", "")
		}
		return map[string]any{"machine": m.view()}, nil, nil
	case "unregister":
		f := flags("profile unregister")
		name := f.String("name", "", "")
		if e = parse(f, args[2:]); e != nil {
			return nil, nil, e
		}
		if !validName(*name) {
			return nil, nil, usage("unregister requires a valid --name")
		}
		target, e := profileTarget(id)
		if e != nil {
			return nil, nil, e
		}
		ms, e := machines(ctx)
		if e != nil {
			return nil, nil, e
		}
		for _, m := range ms {
			if m.Name == *name {
				if !matches(m, target) {
					return nil, nil, fail("target_mismatch", "machine does not belong to this profile", "")
				}
				_, e = callCore(ctx, "machine", "rm", *name)
				return result, nil, e
			}
		}
		return nil, nil, fail("not_found", "machine not found", "")
	case "rm":
		f := flags("profile rm")
		force := f.Bool("force", false, "")
		if e = parse(f, args[2:]); e != nil {
			return nil, nil, e
		}
		e = store.RemoveProfile(ctx, dir, func() error {
			target, e := profileTarget(id)
			if e != nil {
				return e
			}
			ms, e := machines(ctx)
			if e != nil {
				return e
			}
			owned := matching(ms, target)
			if len(owned) > 0 && !*force {
				names := []string{}
				for _, m := range owned {
					names = append(names, redact.Clean(m.Name))
				}
				return fail("profile_in_use", "profile has registered machines", strings.Join(names, ", ")+"; unregister them or use --force")
			}
			for _, m := range owned {
				if _, e = callCore(ctx, "machine", "rm", m.Name); e != nil {
					return e
				}
			}
			if e = stopDaemon(ctx, dir); e != nil {
				return e
			}
			return nil
		})
		return result, nil, e
	default:
		return nil, nil, usage("unknown profile command")
	}
}
func stopDaemon(ctx context.Context, dir string) error {
	state, running := runstate.Read(dir, "daemon")
	if !running {
		// A busy lock without a verifiable state is never safe to signal/delete.
		lock, e := runstate.Acquire(dir, "daemon")
		if e != nil {
			return fail("ownership_unverified", "cannot verify profile daemon ownership", "")
		}
		lock.Close()
		return nil
	}
	// Require live identity + held lock + same executable + kernel socket peer PID.
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	binary, e = filepath.EvalSymlinks(binary)
	if e != nil {
		return e
	}
	if binary != state.Binary || !strings.HasSuffix(state.Identity, " daemon "+filepath.Base(dir)) {
		return fail("ownership_unverified", "daemon executable differs from this tool", "")
	}
	c, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "daemon.sock"), Net: "unix"})
	if e != nil {
		return fail("ownership_unverified", "cannot verify daemon socket", "")
	}
	defer c.Close()
	if _, e = peer.UID(c); e != nil {
		return e
	}
	pid, e := peer.PID(c)
	if e != nil || pid != state.PID {
		return fail("ownership_unverified", "daemon socket PID does not match state", "")
	}
	latest, ok := runstate.Read(dir, "daemon")
	if !ok || latest.Identity != state.Identity || latest.PID != pid {
		return fail("ownership_unverified", "daemon identity changed", "")
	}
	if e = syscall.Kill(pid, syscall.SIGTERM); e != nil {
		return fmt.Errorf("cannot stop owned daemon: %w", e)
	}
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		lock, e := runstate.Acquire(dir, "daemon")
		if e == nil {
			lock.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fail("timeout", "daemon stop timed out", "")
		case <-time.After(25 * time.Millisecond):
		}
	}
	return fail("timeout", "daemon did not stop within 3s", "")
}
