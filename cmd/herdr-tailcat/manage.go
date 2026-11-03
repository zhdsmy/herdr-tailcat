package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"tailscale.com/types/key"
)

type cliError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func (e *cliError) Error() string { return e.Message }
func fail(kind, message, hint string) error {
	return &cliError{kind, redact.Clean(message), redact.Clean(hint)}
}
func asCLIError(err error) *cliError {
	var e *cliError
	if errors.As(err, &e) {
		return &cliError{e.Kind, redact.Clean(e.Message), redact.Clean(e.Hint)}
	}
	return &cliError{"io_error", redact.OneLine(err), "Check the private configuration and file permissions."}
}
func usage(message string) error { return fail("invalid_arguments", message, "") }
func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if e := f.Parse(args); e != nil {
		return usage(e.Error())
	}
	if f.NArg() != 0 {
		return usage("unexpected arguments")
	}
	return nil
}
func ptr(s string) *string { return &s }
func manage(ctx context.Context, args []string, in io.Reader) (map[string]any, *string, error) {
	if len(args) == 0 {
		return nil, nil, usage("usage: herdr-tailcat <server|profile>")
	}
	if args[0] == "serve" || args[0] == "daemon" || args[0] == "via" {
		return nil, nil, fail("unsupported", "Foreground transports do not provide a JSON stream; use server status or profile test.", "")
	}
	if len(args) < 2 {
		return nil, nil, usage("missing subcommand")
	}
	// Generating service text must not create an uninitialized home.
	if args[0] == "server" && args[1] == "install-service" {
		return serviceInstall(args[2:])
	}
	if args[0] != "server" && args[0] != "profile" {
		return nil, nil, usage("unknown command")
	}
	home, err := store.Home()
	if err != nil {
		return nil, nil, err
	}
	var identity tailcat.PrivateKey
	if store.ReadJSON(filepath.Join(home, "server", "server.key"), &identity) == nil {
		b, _ := identity.Private.MarshalText()
		redact.Add(string(b), strings.TrimPrefix(string(b), "priv"+"key:"))
		b, _ = identity.Public.PresharedKey.MarshalText()
		redact.Add(string(b), strings.TrimPrefix(string(b), "psk:"))
	}
	if args[0] == "server" {
		return serverManage(ctx, home, args[1:])
	}
	return profileManage(ctx, home, args[1:], in)
}
func profileDir(home, id string) (string, error) {
	if !validName(id) {
		return "", usage("invalid profile id")
	}
	dir := filepath.Join(home, "profiles", id)
	var p store.Profile
	if store.ReadJSON(filepath.Join(dir, "profile.json"), &p) != nil {
		return "", fail("profile_not_found", "profile does not exist", "")
	}
	// Register address and PSK forms before processing any external diagnostics.
	redact.Add(p.Address)
	if ci, e := tailcat.ParseAddr(tailcat.Addr(p.Address)); e == nil {
		b, _ := ci.PresharedKey.MarshalText()
		redact.Add(string(b), strings.TrimPrefix(string(b), "psk:"))
	}
	var private key.NodePrivate
	if store.ReadJSON(filepath.Join(dir, "client.key"), &private) == nil {
		b, _ := private.MarshalText()
		redact.Add(string(b), strings.TrimPrefix(string(b), "priv"+"key:"))
	}
	return dir, nil
}
func exists(path string) bool { _, e := os.Stat(path); return e == nil }
