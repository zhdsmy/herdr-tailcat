package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Lock waits cancellably for an exclusive cross-process lock. The caller owns
// the returned file and releases the lock by closing it. Cancellation bounds
// lock acquisition and the start of a mutation, not synchronous filesystem I/O.
func Lock(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := privateParent(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { f.Close(); return nil, err }
	if err := f.Chmod(0600); err != nil {
		return fail(err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return fail(err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fail(ctx.Err())
		case <-timer.C:
		}
	}
}

// ProfileLock lives outside profiles/<id>, so removing/recreating that directory
// cannot split the lifecycle lock across inodes. Autostart takes startup,
// then lifecycle; a daemon takes lifecycle, then daemon (held for its lifetime).
// Removal takes lifecycle and waits for daemon to exit. A parent releases
// lifecycle before awaiting its child; a child never takes startup.
func ProfileLock(ctx context.Context, dir string) (*os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return Lock(ctx, ProfileLockPath(dir, "lifecycle"))
}
func ProfileLockPath(dir, name string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(dir)), "profile-locks", filepath.Base(dir)+"."+name+".lock")
}

// RemoveProfile serializes deletion with identity creation and daemon startup.
func RemoveProfile(ctx context.Context, dir string, stop func() error) error {
	lock, err := ProfileLock(ctx, dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err = os.Stat(filepath.Join(dir, "profile.json")); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err := stop(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
