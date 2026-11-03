package main

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zhdsmy/herdr-tailcat/internal/allow"
	"github.com/zhdsmy/herdr-tailcat/internal/runstate"
	"github.com/zhdsmy/herdr-tailcat/internal/serve"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"github.com/zhdsmy/herdr-tailcat/internal/version"
	"tailscale.com/types/key"
)

func fingerprint(s string) string {
	s = strings.TrimPrefix(s, "nodekey:")
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func serverManage(ctx context.Context, home string, args []string) (map[string]any, *string, error) {
	result := map[string]any{}
	switch args[0] {
	case "init":
		if len(args) != 1 {
			return nil, nil, usage("usage: server init")
		}
		if exists(filepath.Join(home, "server", "server.key")) {
			return nil, nil, fail("already_exists", "server is already initialized", "")
		}
		if e := serve.Init(home); e != nil {
			return nil, nil, e
		}
		return result, nil, nil
	case "show-address":
		f := flags("show-address")
		full := f.Bool("full", false, "")
		if e := parse(f, args[1:]); e != nil {
			return nil, nil, e
		}
		a, e := serve.Address(home, *full)
		return map[string]any{"address": a}, ptr(a), e
	case "status":
		f := flags("status")
		label := f.String("label", "dev.herdr.tailcat", "")
		dir := f.String("plist-dir", "", "")
		if e := parse(f, args[1:]); e != nil {
			return nil, nil, e
		}
		initialized := exists(filepath.Join(home, "server", "server.key")) && exists(filepath.Join(home, "server", "config.json"))
		state, running := runstate.Read(filepath.Join(home, "server"), "serve")
		exports := []string{}
		count := 0
		restart := false
		if initialized {
			cfg, keys, e := serve.DiskConfig(home)
			if e != nil {
				return nil, nil, e
			}
			exports = cfg.Exports
			count = len(keys)
			restart = running && state.Digest != serve.Digest(cfg, keys)
		}
		pid := 0
		streams := int64(0)
		started := ""
		if running {
			pid = state.PID
			streams = state.Streams
			started = state.Started
		}
		result = map[string]any{"initialized": initialized, "running": running, "pid": pid, "started_at": started, "version": version.String(), "exports": exports, "allowed_clients": count, "streams": streams, "restart_required": restart}
		if runtime.GOOS == "darwin" {
			v, e := serviceControl(ctx, "status", *label, *dir)
			if e != nil {
				return nil, nil, e
			}
			result["service"] = v
		}
		return result, nil, nil
	case "service":
		return serviceManage(ctx, args[1:])
	case "allow":
		if len(args) < 2 {
			return nil, nil, usage("usage: server allow add|rm|ls")
		}
		path := filepath.Join(home, "server", "allowed_clients")
		if args[1] == "ls" {
			if len(args) != 2 {
				return nil, nil, usage("usage: server allow ls")
			}
			keys, e := allow.Read(path, true)
			if e != nil {
				return nil, nil, e
			}
			clients := []map[string]string{}
			for _, k := range keys {
				clients = append(clients, map[string]string{"public_key": k.String(), "fingerprint": fingerprint(k.String())})
			}
			return map[string]any{"clients": clients}, nil, nil
		}
		if len(args) != 3 {
			return nil, nil, usage("usage: server allow add|rm <nodekey>")
		}
		var k key.NodePublic
		if k.UnmarshalText([]byte(args[2])) != nil || k.IsZero() {
			return nil, nil, usage("invalid client public key")
		}
		if args[1] == "add" {
			return result, nil, serve.AllowAdd(home, args[2])
		}
		if args[1] != "rm" {
			return nil, nil, usage("unknown allow command")
		}
		keys, e := allow.Read(path, true)
		if e != nil {
			return nil, nil, e
		}
		out := ""
		found := false
		for _, x := range keys {
			if x == k {
				found = true
			} else {
				out += x.String() + "\n"
			}
		}
		if !found {
			return nil, nil, fail("not_found", "client key is not allowed", "")
		}
		if e = store.WriteFile(path, []byte(out)); e != nil {
			return nil, nil, e
		}
		result["warning"] = ""
		if out == "" {
			result["warning"] = "Whitelist is empty; serve will refuse to start until a client is allowed."
		}
		return result, nil, nil
	case "export":
		if len(args) < 2 {
			return nil, nil, usage("usage: server export add|rm|ls")
		}
		path := filepath.Join(home, "server", "config.json")
		var cfg store.ServerConfig
		if e := store.ReadJSON(path, &cfg); e != nil {
			return nil, nil, e
		}
		if args[1] == "ls" && len(args) == 2 {
			return map[string]any{"exports": cfg.Exports}, nil, nil
		}
		if len(args) != 3 || !validName(args[2]) {
			return nil, nil, usage("usage: server export add|rm <session>")
		}
		if args[1] == "add" {
			return result, nil, serve.ExportAdd(home, args[2])
		}
		if args[1] != "rm" {
			return nil, nil, usage("unknown export command")
		}
		out := []string{}
		found := false
		for _, x := range cfg.Exports {
			if x == args[2] {
				found = true
			} else {
				out = append(out, x)
			}
		}
		if !found {
			return nil, nil, fail("not_found", "session is not exported", "")
		}
		cfg.Exports = out
		return result, nil, store.WriteJSON(path, cfg)
	default:
		return nil, nil, usage("unknown server command")
	}
}
