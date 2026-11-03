package serve

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/allow"
	"github.com/zhdsmy/herdr-tailcat/internal/line"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"tailscale.com/types/key"
)

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
func AllowAdd(home, text string) error {
	var k key.NodePublic
	if k.UnmarshalText([]byte(text)) != nil || k.IsZero() {
		return errors.New("invalid client public key")
	}
	p := filepath.Join(home, "server", "allowed_clients")
	b, e := os.ReadFile(p)
	if e != nil && !os.IsNotExist(e) {
		return errors.New("cannot read allowed_clients")
	}
	for _, s := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(s) == k.String() {
			return errors.New("client key already allowed")
		}
	}
	f, e := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	separator := ""
	if len(b) > 0 && b[len(b)-1] != '\n' {
		separator = "\n"
	}
	_, e = f.WriteString(separator + k.String() + "\n")
	return e
}
func ExportAdd(home, s string) error {
	if !line.ValidName(s) {
		return errors.New("invalid export session")
	}
	p := filepath.Join(home, "server", "config.json")
	var cfg store.ServerConfig
	if e := store.ReadJSON(p, &cfg); e != nil {
		return e
	}
	for _, x := range cfg.Exports {
		if x == s {
			return errors.New("session already exported")
		}
	}
	cfg.Exports = append(cfg.Exports, s)
	return store.WriteJSON(p, cfg)
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
