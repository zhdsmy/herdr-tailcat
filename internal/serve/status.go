package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/zhdsmy/herdr-tailcat/internal/allow"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"tailscale.com/types/key"
)

func Digest(cfg store.ServerConfig, keys []key.NodePublic) string {
	// Match Load's default normalization while allowing an empty disk whitelist.
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 16
	}
	if cfg.ClientIdleTimeout == "" {
		cfg.ClientIdleTimeout = "90s"
	}
	b, _ := json.Marshal(struct {
		Config store.ServerConfig
		Keys   []key.NodePublic
	}{cfg, keys})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func DiskConfig(home string) (store.ServerConfig, []key.NodePublic, error) {
	var cfg store.ServerConfig
	if err := store.ReadJSON(filepath.Join(home, "server", "config.json"), &cfg); err != nil {
		return cfg, nil, err
	}
	keys, err := allow.Read(filepath.Join(home, "server", "allowed_clients"), true)
	return cfg, keys, err
}
