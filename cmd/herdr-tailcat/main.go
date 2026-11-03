package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/daemon"
	"github.com/zhdsmy/herdr-tailcat/internal/redact"
	"github.com/zhdsmy/herdr-tailcat/internal/serve"
	"github.com/zhdsmy/herdr-tailcat/internal/via"
)

func main() {
	log.SetOutput(io.Discard)
	args, jsonMode := jsonArgs(os.Args[1:])
	err := runMode(args, jsonMode, os.Stdin, os.Stdout)
	if err != nil {
		if !jsonMode && !errors.Is(err, errPromptReported) {
			fmt.Fprintln(os.Stderr, redact.OneLine(err))
		}
		code := 1
		if len(args) > 0 && args[0] == "via" && !jsonMode {
			code = 255
		}
		os.Exit(code)
	}
}
func jsonArgs(args []string) ([]string, bool) {
	if len(args) == 0 || (args[0] != "server" && args[0] != "profile") {
		// A via line is one opaque argument, even if it contains --json.
		return args, len(args) > 1 && args[1] == "--json"
	}
	clean := []string{}
	mode := false
	for _, a := range args {
		if a == "--json" {
			mode = true
		} else {
			clean = append(clean, a)
		}
	}
	return clean, mode
}
func run(args []string) error {
	args, j := jsonArgs(args)
	return runMode(args, j, os.Stdin, os.Stdout)
}
func runMode(args []string, j bool, in io.Reader, out io.Writer) error {
	if wantsPrompt(args) && !j {
		return promptProfile(args, in, out)
	}
	if wantsPrompt(args) && j {
		err := usage("--prompt does not support --json")
		_ = json.NewEncoder(out).Encode(map[string]any{"ok": false, "error": asCLIError(err)})
		return err
	}
	if len(args) > 0 && (args[0] == "serve" || args[0] == "daemon" || args[0] == "via") && !j {
		switch args[0] {
		case "serve":
			if len(args) != 1 {
				return errors.New("usage: serve")
			}
			return serve.Run()
		case "daemon":
			if len(args) != 2 {
				return errors.New("usage: daemon <profile>")
			}
			return daemon.Run(args[1])
		case "via":
			if len(args) != 3 {
				return errors.New("usage: via <profile> <line>")
			}
			return via.Run(args[1], args[2])
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24500*time.Millisecond)
	defer cancel()
	result, plain, err := manage(ctx, args, in)
	if err != nil {
		if j {
			_ = json.NewEncoder(out).Encode(map[string]any{"ok": false, "error": asCLIError(err)})
		}
		return err
	}
	if j {
		if result == nil {
			result = map[string]any{}
		}
		// show-address intentionally has no envelope, so this is the only secret output.
		if !(len(args) > 1 && args[0] == "server" && args[1] == "show-address") {
			result["ok"] = true
		}
		return json.NewEncoder(out).Encode(result)
	}
	if plain != nil {
		_, err = fmt.Fprintln(out, *plain)
		return err
	}
	result["ok"] = true
	return json.NewEncoder(out).Encode(result)
}
