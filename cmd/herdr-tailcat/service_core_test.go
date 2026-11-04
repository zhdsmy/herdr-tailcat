package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

func TestServiceCoreProtectedPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	home := filepath.Join(root, "tailcat")
	t.Setenv("HERDR_TAILCAT_HOME", home)
	for _, name := range []string{"Documents", "Desktop", "Downloads"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		core := filepath.Join(dir, "herdr-core")
		if err := os.WriteFile(core, nil, 0700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, name+"-core")
		if err := os.Symlink(core, alias); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{core, alias} {
			if err := store.WriteJSON(filepath.Join(home, "server", "config.json"), store.ServerConfig{HerdrCore: path}); err != nil {
				t.Fatal(err)
			}
			for _, dry := range []bool{false, true} {
				args := []string{"--os", "darwin", "--plist-dir", t.TempDir(), "--label", "dev.herdr.tailcat.test.core"}
				if dry {
					args = append(args, "--dry-run")
				}
				_, _, err := serviceInstall(args)
				if err == nil || asCLIError(err).Kind != "invalid_arguments" {
					t.Error("protected core accepted", name, dry, err)
				}
			}
			if _, _, err := serviceInstall([]string{"--os", "linux", "--dry-run"}); err != nil {
				t.Fatal("Linux rejected by macOS policy", err)
			}
		}
	}
	safe := filepath.Join(root, "bin", "herdr-core")
	if err := store.WriteFile(safe, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(home, "server", "config.json"), store.ServerConfig{HerdrCore: safe}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := serviceInstall([]string{"--os", "darwin", "--dry-run"}); err != nil {
		t.Fatal("safe core rejected", err)
	}
}
