package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"tailscale.com/types/key"
)

func TestConfigDeadline(t *testing.T) {
	if os.Getenv("TC_CONFIG_LOCK_CHILD") == "1" {
		f, err := os.OpenFile(os.Getenv("TC_CONFIG_LOCK"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			os.Exit(2)
		}
		defer f.Close()
		if unix.Flock(int(f.Fd()), unix.LOCK_EX) != nil {
			os.Exit(3)
		}
		fmt.Println("locked")
		io.ReadFull(os.Stdin, make([]byte, 1))
		return
	}
	for _, op := range [][]string{{"allow", "add"}, {"allow", "rm"}, {"export", "add"}, {"export", "rm"}} {
		t.Run(op[0]+op[1], func(t *testing.T) {
			home := setupCLI(t)
			paths := []string{filepath.Join(home, "server", "config.json"), filepath.Join(home, "server", "allowed_clients")}
			before := make([][]byte, len(paths))
			for i, p := range paths {
				before[i], _ = os.ReadFile(p)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestConfigDeadline$")
			cmd.Env = append(os.Environ(), "TC_CONFIG_LOCK_CHILD=1", "TC_CONFIG_LOCK="+filepath.Join(home, "server", "config.lock"))
			in, _ := cmd.StdinPipe()
			out, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if line, err := bufio.NewReader(out).ReadString(10); err != nil || line != "locked\n" {
				t.Fatal("child lock", err, line)
			}
			released := false
			release := func() {
				if !released {
					released = true
					in.Close()
					if err := cmd.Wait(); err != nil {
						t.Error(err)
					}
				}
			}
			defer release()
			value := "tc-new"
			if op[0] == "allow" {
				value = key.NewNode().Public().String()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, _, err := serverManage(ctx, home, append(op, value)); done <- err }()
			<-ctx.Done()
			var err error
			select {
			case err = <-done:
			case <-time.After(500 * time.Millisecond):
				release()
				<-done
				t.Fatal("config operation ignored deadline")
			}
			if err == nil || asCLIError(err).Kind != "timeout" {
				t.Fatal("want timeout", err)
			}
			for i, p := range paths {
				after, _ := os.ReadFile(p)
				if !bytes.Equal(before[i], after) {
					t.Fatal("changed while locked")
				}
			}
			release()
			for i, p := range paths {
				after, _ := os.ReadFile(p)
				if !bytes.Equal(before[i], after) {
					t.Fatal("late mutation after deadline")
				}
			}
		})
	}
}
