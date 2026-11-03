package serve

import (
	"testing"

	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"tailscale.com/types/key"
)

func TestDigestTracksConfigAndPolicy(t *testing.T) {
	cfg := store.ServerConfig{HerdrCore: "/bin/core", Exports: []string{"tc-work"}}
	keys := []key.NodePublic{key.NewNode().Public()}
	a := Digest(cfg, keys)
	cfg.MaxConns = 16
	cfg.ClientIdleTimeout = "90s"
	if a != Digest(cfg, keys) {
		t.Fatal("defaults change digest")
	}
	cfg.Exports = append(cfg.Exports, "tc-other")
	if a == Digest(cfg, keys) {
		t.Fatal("export changes not detected")
	}
	cfg.Exports = cfg.Exports[:1]
	if a == Digest(cfg, nil) {
		t.Fatal("empty whitelist not detected")
	}
}
