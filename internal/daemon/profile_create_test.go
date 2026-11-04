package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zhdsmy/herdr-tailcat/internal/store"
	"tailscale.com/tailcfg"
)

func profileAddress() string {
	k := tailcat.NewPrivateKey()
	k.Public.Region = []*tailcfg.DERPRegion{{RegionID: 1}}
	return string(k.Public.Addr())
}
func TestProfileCreationAcrossProcesses(t *testing.T) {
	if mode := os.Getenv("TC_CREATE_CHILD"); mode != "" {
		text, _ := bufio.NewReader(os.Stdin).ReadString(10)
		write := func(path string, v any) error {
			if filepath.Base(path) == "client.key" {
				fmt.Println("writing")
				if mode == "first" {
					io.ReadFull(os.Stdin, make([]byte, 1))
				}
			}
			return store.WriteJSON(path, v)
		}
		err := addProfile(context.Background(), os.Getenv("TC_CREATE_DIR"), text, write)
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
				fmt.Println("exists")
				return
			}
			os.Exit(2)
		}
		pub, err := PublicKey(os.Getenv("TC_CREATE_DIR"))
		if err != nil {
			os.Exit(3)
		}
		fmt.Println("ok " + pub)
		return
	}
	dir := filepath.Join(t.TempDir(), "profiles", "same")
	type child struct {
		cmd *exec.Cmd
		in  io.WriteCloser
		out *bufio.Reader
	}
	start := func(mode, text string) *child {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProfileCreationAcrossProcesses$")
		cmd.Env = append(os.Environ(), "TC_CREATE_CHILD="+mode, "TC_CREATE_DIR="+dir)
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		io.WriteString(in, text+"\n")
		t.Cleanup(func() {
			in.Close()
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		return &child{cmd, in, bufio.NewReader(out)}
	}
	atext, btext := profileAddress(), profileAddress()
	first := start("first", atext)
	if line, err := first.out.ReadString(10); err != nil || line != "writing\n" {
		t.Fatal("first barrier", err, line)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := AddProfileContext(ctx, filepath.Join(filepath.Dir(dir), "other"), profileAddress()); err != nil {
		t.Error("different id blocked by same-id import", err)
	}
	cancel()
	second := start("second", btext)
	next := make(chan string, 1)
	go func() { line, _ := second.out.ReadString(10); next <- line }()
	// The first process is held inside its identity write. A second creator must
	// not reach that write before the first commits. This is a bounded lock wait,
	// rather than a sleep used to arrange a race.
	var early string
	select {
	case early = <-next:
	case <-time.After(500 * time.Millisecond):
	}
	io.WriteString(first.in, "x")
	line, err := first.out.ReadString(10)
	if err != nil || !strings.HasPrefix(line, "ok ") {
		t.Fatal("winner", err, line)
	}
	want := strings.TrimSpace(strings.TrimPrefix(line, "ok "))
	if err := first.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if early == "" {
		select {
		case early = <-next:
		case <-time.After(4 * time.Second):
			t.Fatal("second stuck")
		}
	}
	if early != "exists\n" {
		t.Error("second creator entered committed identity write", early)
	}
	io.Copy(io.Discard, second.out)
	if err := second.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	got, err := PublicKey(dir)
	if err != nil || got != want {
		t.Error("winner public key changed", err)
	}
	var p store.Profile
	if err := store.ReadJSON(filepath.Join(dir, "profile.json"), &p); err != nil || p.Address != atext {
		t.Error("mixed or overwritten profile", err)
	}
}
func TestProfileSecondWriteFailureRecoverable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profiles", "p1")
	text := profileAddress()
	err := addProfile(context.Background(), dir, text, func(path string, v any) error {
		if filepath.Base(path) == "profile.json" {
			return fmt.Errorf("injected write failure")
		}
		return store.WriteJSON(path, v)
	})
	if err == nil {
		t.Fatal("failure not injected")
	}
	if _, err := PublicKey(dir); err == nil {
		t.Error("partial identity visible after failed import")
	}
	if err := AddProfile(dir, text); err != nil {
		t.Fatal("retry", err)
	}
	if _, err := PublicKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := AddProfile(filepath.Join(filepath.Dir(dir), "p2"), profileAddress()); err != nil {
		t.Fatal("different id", err)
	}
}
