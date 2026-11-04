package serve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/allow"
	"github.com/zhdsmy/herdr-tailcat/internal/line"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"github.com/zhdsmy/herdr-tailcat/internal/version"
	"tailscale.com/types/key"
)

// replyMargin leaves room for a longer injected version string. The check uses
// the real success reply, so the margin is extra headroom rather than the size itself.
const replyMargin = 256

func Init(home string) error {
	dir := filepath.Join(home, "server")
	p := filepath.Join(dir, "server.key")
	if _, e := os.Stat(p); e == nil {
		return errors.New("server is already initialized")
	}
	core, e := exec.LookPath("herdr-core")
	if e != nil {
		return errors.New("install herdr-core before initializing the server")
	}
	core, e = filepath.Abs(core)
	if e != nil {
		return e
	}
	k := tailcat.NewPrivateKey()
	k.Public.RegionID = -1 // Expand selects a relay only when explicitly requested.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e = k.Public.Expand(ctx, tailcat.ExpandForServer); e != nil {
		return errors.New("could not resolve a DERP region for server initialization")
	}
	if e = store.WriteJSON(p, k); e != nil {
		return e
	}
	return store.WriteJSON(filepath.Join(dir, "config.json"), store.ServerConfig{HerdrCore: core, Exports: []string{}, MaxConns: 16, ClientIdleTimeout: "90s"})
}
func Address(home string, full bool) (string, error) {
	var k tailcat.PrivateKey
	if e := store.ReadJSON(filepath.Join(home, "server", "server.key"), &k); e != nil {
		return "", errors.New("cannot read server identity")
	}
	ci := k.Public
	if len(ci.Region) != 1 || ci.Region[0] == nil {
		return "", errors.New("server identity lacks a fixed DERP region")
	}
	if !full {
		ci.RegionID = ci.Region[0].RegionID
		ci.Region = nil
	}
	return string(ci.Addr()), nil
}

// withConfigLock serializes allow and export edits across processes.
// The same exclusive lock covers the read, the check, and the rewrite.
func withConfigLock(ctx context.Context, home string, fn func() error) error {
	f, err := store.Lock(ctx, filepath.Join(home, "server", "config.lock"))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
func AllowAdd(ctx context.Context, home, text string) error {
	var k key.NodePublic
	if k.UnmarshalText([]byte(text)) != nil || k.IsZero() {
		return errors.New("invalid client public key")
	}
	return withConfigLock(ctx, home, func() error {
		keys, found, e := editAllow(home, k, true)
		if e != nil {
			return e
		}
		if found {
			return errors.New("client key already allowed")
		}
		return writeAllow(home, append(keys, k))
	})
}
func AllowRemove(ctx context.Context, home, text string) (emptied bool, err error) {
	var k key.NodePublic
	if k.UnmarshalText([]byte(text)) != nil || k.IsZero() {
		return false, errors.New("invalid client public key")
	}
	err = withConfigLock(ctx, home, func() error {
		keys, found, e := editAllow(home, k, true)
		if e != nil {
			return e
		}
		if !found {
			return ErrNotAllowed
		}
		out := make([]key.NodePublic, 0, len(keys)-1)
		for _, x := range keys {
			if x != k {
				out = append(out, x)
			}
		}
		if e = writeAllow(home, out); e != nil {
			return e
		}
		emptied = len(out) == 0
		return nil
	})
	return emptied, err
}
func editAllow(home string, k key.NodePublic, emptyOK bool) ([]key.NodePublic, bool, error) {
	keys, e := allow.Read(filepath.Join(home, "server", "allowed_clients"), emptyOK)
	if e != nil {
		return nil, false, e
	}
	for _, x := range keys {
		if x == k {
			return keys, true, nil
		}
	}
	return keys, false, nil
}
func writeAllow(home string, keys []key.NodePublic) error {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k.String())
		b.WriteByte('\n')
	}
	return store.WriteFile(filepath.Join(home, "server", "allowed_clients"), []byte(b.String()))
}
func ExportAdd(ctx context.Context, home, s string) error {
	if !line.ValidName(s) {
		return errors.New("invalid export session")
	}
	return withConfigLock(ctx, home, func() error {
		cfg, e := readExports(home)
		if e != nil {
			return e
		}
		for _, x := range cfg.Exports {
			if x == s {
				return errors.New("session already exported")
			}
		}
		cfg.Exports = append(cfg.Exports, s)
		if e = exportsFit(cfg.Exports); e != nil {
			return e
		}
		return store.WriteJSON(filepath.Join(home, "server", "config.json"), cfg)
	})
}
func ExportRemove(ctx context.Context, home, s string) error {
	if !line.ValidName(s) {
		return errors.New("invalid export session")
	}
	return withConfigLock(ctx, home, func() error {
		cfg, e := readExports(home)
		if e != nil {
			return e
		}
		out := make([]string, 0, len(cfg.Exports))
		found := false
		for _, x := range cfg.Exports {
			if x == s {
				found = true
			} else {
				out = append(out, x)
			}
		}
		if !found {
			return ErrNotExported
		}
		cfg.Exports = out
		return store.WriteJSON(filepath.Join(home, "server", "config.json"), cfg)
	})
}
func readExports(home string) (store.ServerConfig, error) {
	var cfg store.ServerConfig
	e := store.ReadJSON(filepath.Join(home, "server", "config.json"), &cfg)
	return cfg, e
}

// ErrNotAllowed and ErrNotExported identify missing removal targets.
var ErrNotAllowed = errors.New("client key is not allowed")
var ErrNotExported = errors.New("session is not exported")

// exportsFit rejects a list whose success reply cannot fit in one 4 KiB line.
// The encoded value is the same Response ping and connect write. replyMargin
// is reserved so a longer version string still fits without changing the protocol.
func exportsFit(exports []string) error {
	n, e := replySize(exports)
	if e != nil {
		return e
	}
	if n+replyMargin > proto.MaxLine {
		return errors.New("too many exports: handshake reply would exceed 4 KiB")
	}
	return nil
}
func replySize(exports []string) (int, error) {
	b, e := json.Marshal(proto.Response{Version: proto.Version, OK: true, Server: &proto.Info{Version: version.String(), Exports: exports}})
	if e != nil {
		return 0, e
	}
	return len(b) + 1, nil
}
func Load(home string) (store.ServerConfig, []key.NodePublic, tailcat.PrivateKey, error) {
	var cfg store.ServerConfig
	var k tailcat.PrivateKey
	keys, e := allow.Load(filepath.Join(home, "server", "allowed_clients"))
	if e != nil {
		return cfg, nil, k, e
	}
	if e = store.ReadJSON(filepath.Join(home, "server", "config.json"), &cfg); e != nil {
		return cfg, nil, k, errors.New("cannot read server config")
	}
	if !filepath.IsAbs(cfg.HerdrCore) {
		return cfg, nil, k, errors.New("herdr_core must be an absolute path")
	}
	if _, e = clientIdle(cfg); e != nil {
		return cfg, nil, k, e
	}
	if len(cfg.Exports) == 0 {
		return cfg, nil, k, errors.New("at least one session must be explicitly exported")
	}
	seen := map[string]bool{}
	for _, s := range cfg.Exports {
		if !line.ValidName(s) || seen[s] {
			return cfg, nil, k, errors.New("invalid or duplicate export session")
		}
		seen[s] = true
	}
	if e = exportsFit(cfg.Exports); e != nil {
		return cfg, nil, k, e
	}
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 16
	}
	if cfg.MaxConns < 1 || cfg.MaxConns > 4096 {
		return cfg, nil, k, errors.New("invalid max_conns")
	}
	for name := range cfg.Env {
		if name == "HC_SESSION" || name == "HC_SOCKET" || strings.ContainsAny(name, "=\x00") {
			return cfg, nil, k, errors.New("server env must not override HC_SESSION or HC_SOCKET")
		}
	}
	if e = store.ReadJSON(filepath.Join(home, "server", "server.key"), &k); e != nil {
		return cfg, nil, k, errors.New("cannot read server identity")
	}
	if k.Private.IsZero() || k.Public.PresharedKey.IsZero() || k.Public.ServerDiscoPublic.IsZero() || len(k.Public.Region) != 1 || k.Public.Region[0] == nil || k.Public.Region[0].RegionID == 0 || k.Public.ServerPublic.NodePublic != k.Private.Public() {
		return cfg, nil, k, errors.New("invalid server identity or missing fixed DERP region")
	}
	return cfg, keys, k, nil
}
