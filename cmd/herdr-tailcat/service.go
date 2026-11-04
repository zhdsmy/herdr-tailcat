package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zhdsmy/herdr-tailcat/internal/service"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
)

func serviceInstall(args []string) (map[string]any, *string, error) {
	flags := flag.NewFlagSet("server install-service", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	label := flags.String("label", "dev.herdr.tailcat", "service label")
	dir := flags.String("plist-dir", "", "output directory for macOS plist")
	dry := flags.Bool("dry-run", false, "print service text only")
	target := flags.String("os", runtime.GOOS, "darwin or linux (Linux is text only)")
	forceDERP := flags.Bool("force-derp", false, "force relay for this service")
	if err := flags.Parse(args); err != nil {
		return nil, nil, usage(err.Error())
	}
	if flags.NArg() != 0 {
		return nil, nil, usage("unexpected service arguments")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	home := os.Getenv("HERDR_TAILCAT_HOME")
	if home == "" {
		home = filepath.Join(userHome, ".local", "state", "herdr-tailcat")
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, nil, err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	binary, err = serviceBinary(*target, binary, userHome)
	if err != nil {
		return nil, nil, err
	}
	var cfg store.ServerConfig
	if err = store.ReadJSON(filepath.Join(home, "server", "config.json"), &cfg); err != nil {
		return nil, nil, usage("initialize server before generating service files")
	}
	if *target == "darwin" {
		cfg.HerdrCore, err = serviceBinary(*target, cfg.HerdrCore, userHome)
		if err != nil {
			return nil, nil, err
		}
	}
	env := map[string]string{"HOME": userHome,
		"XDG_STATE_HOME":  filepath.Join(userHome, ".local", "state"),
		"XDG_CONFIG_HOME": filepath.Join(userHome, ".config"), "XDG_RUNTIME_DIR": ""}
	for _, key := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "HC_SESSION"} {
		if value, ok := os.LookupEnv(key); ok && (value != "" || key == "XDG_RUNTIME_DIR") {
			env[key] = value
		}
	}
	options := service.Options{OS: *target, Label: *label, Binary: binary, Home: home, Core: cfg.HerdrCore, ForceDERP: *forceDERP, Env: env}
	text, err := service.Render(options)
	if err != nil {
		return nil, nil, err
	}
	if *dry || *target == "linux" {
		return map[string]any{"text": string(text), "os": *target}, ptr(string(text)), nil
	}
	// Same default as service start, so the two commands agree on the plist.
	if *dir == "" {
		*dir = filepath.Join(userHome, "Library", "LaunchAgents")
	}
	path, err := service.WritePlist(options, *dir, text)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"path": path, "os": *target}, ptr(path), nil
}

// Tailscale reads its own executable at startup. launchd cannot answer macOS
// privacy prompts, so reject binaries in the user's protected folders before
// generating a service, including dry-runs and symlinks into those folders.
func serviceBinary(target, binary, userHome string) (string, error) {
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return "", err
	}
	if target == "darwin" {
		home, err := filepath.EvalSymlinks(userHome)
		if err != nil {
			return "", err
		}
		for _, name := range []string{"Documents", "Desktop", "Downloads"} {
			protected := filepath.Join(home, name)
			locations := []string{protected, filepath.Join(userHome, name)}
			if real, err := filepath.EvalSymlinks(protected); err == nil && real != protected {
				locations = append(locations, real)
			}
			for _, location := range locations {
				for _, candidate := range []string{binary, resolved} {
					rel, err := filepath.Rel(location, candidate)
					if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						return "", usage("macOS services cannot execute binaries in ~/Documents, ~/Desktop or ~/Downloads; install herdr-tailcat and herdr-core outside these folders (for example ~/.local/bin), then generate the service again")
					}
				}
			}
		}
	}
	return resolved, nil
}
