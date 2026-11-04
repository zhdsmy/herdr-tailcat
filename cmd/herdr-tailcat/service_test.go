package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceBinaryRejectsProtectedLocations(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"Documents", "Desktop", "Downloads"} {
		dir := filepath.Join(home, name, "project", "bin")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(dir, "herdr-tailcat")
		if err := os.WriteFile(binary, []byte("test fixture; never executed"), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, name+"-link")
		if err := os.Symlink(binary, link); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{binary, link} {
			_, err := serviceBinary("darwin", path, home)
			var cli *cliError
			if !errors.As(err, &cli) || cli.Kind != "invalid_arguments" {
				t.Fatalf("protected path accepted: %s: %v", path, err)
			}
		}
		if _, err := serviceBinary("linux", binary, home); err != nil {
			t.Fatal("macOS rule applied to Linux", err)
		}
	}
	for _, name := range []string{".local/bin", "Documents-backup", "Desktop-other", "Downloads-old"} {
		dir := filepath.Join(home, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(dir, "herdr-tailcat")
		if err := os.WriteFile(binary, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := serviceBinary("darwin", binary, home); err != nil {
			t.Fatal("safe path rejected", err)
		}
	}
}

func TestServiceBinaryResolvesProtectedDirectoryAndHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	actual := filepath.Join(root, "relocated-documents")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, filepath.Join(home, "Documents")); err != nil {
		t.Fatal(err)
	}
	homeLink := filepath.Join(root, "home-link")
	if err := os.Symlink(home, homeLink); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(actual, "herdr-tailcat")
	if err := os.WriteFile(binary, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceBinary("darwin", binary, homeLink); err == nil {
		t.Fatal("relocated protected directory accepted")
	}
}

// The test binary runs from the Go build cache, never a protected folder.
func TestServiceInstallDefaultsToLaunchAgents(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	home := filepath.Join(root, "tailcat")
	t.Setenv("HERDR_TAILCAT_HOME", home)
	t.Setenv("HC_SESSION", "tc-service-test")
	for _, key := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	if err := os.MkdirAll(filepath.Join(home, "server"), 0700); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(root, "bin", "herdr-core")
	if err := os.MkdirAll(filepath.Dir(core), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core, nil, 0700); err != nil {
		t.Fatal(err)
	}
	config := "{\"herdr_core\":\"" + core + "\",\"exports\":[\"default\"]}"
	if err := os.WriteFile(filepath.Join(home, "server", "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	label := "dev.herdr-tailcat.test.default-dir"
	if err := run([]string{"server", "install-service", "--label", label, "--os", "darwin"}); err != nil {
		t.Fatal("install-service without --plist-dir failed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Library", "LaunchAgents", label+".plist")); err != nil {
		t.Fatal("plist not written to ~/Library/LaunchAgents", err)
	}
}

func TestServiceDryRunDoesNotCreateHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "absent")
	t.Setenv("HERDR_TAILCAT_HOME", home)
	t.Setenv("HC_SESSION", "tc-service-test")
	for _, key := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	if err := run([]string{"server", "install-service", "--label", "dev.herdr-tailcat.test.dry", "--dry-run", "--os", "darwin"}); err == nil {
		t.Fatal("uninitialized server should fail without writes")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("dry-run created service home", err)
	}
}
