package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockCancellationAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f, err := Lock(ctx, path); !errors.Is(err, context.Canceled) || f != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("canceled acquisition created lock")
	}
	first, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if f, err := Lock(ctx, path); !errors.Is(err, context.DeadlineExceeded) || f != nil {
		t.Fatal(err)
	}
	first.Close()
	next, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("lock permissions", err)
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if f, err := Lock(context.Background(), path); err == nil || errors.Is(err, context.DeadlineExceeded) {
		if f != nil {
			f.Close()
		}
		t.Fatal("permission failure treated as busy", err)
	}
}
func TestProfileLockSurvivesDirectoryReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profiles", "p1")
	if err := WriteJSON(filepath.Join(dir, "profile.json"), Profile{}); err != nil {
		t.Fatal(err)
	}
	f, err := ProfileLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := RemoveProfile(context.Background(), dir, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(filepath.Join(dir, "profile.json"), Profile{}); err != nil {
		t.Fatal(err)
	}
	f, err = ProfileLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lifecycle lock inode changed", err)
	}
}
