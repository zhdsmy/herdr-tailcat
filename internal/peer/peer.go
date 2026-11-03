package peer

import (
	"errors"
	"net"
	"os"
)

func UID(c *net.UnixConn) (uint32, error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, e
	}
	var uid uint32
	var err error
	e = raw.Control(func(fd uintptr) { uid, err = socketUID(int(fd)) })
	if e != nil {
		return 0, e
	}
	if err != nil {
		return 0, err
	}
	if uid != uint32(os.Getuid()) {
		return uid, errors.New("daemon connection is not from this user")
	}
	return uid, nil
}

// PID identifies the process on the other side of an already UID-checked socket.
func PID(c *net.UnixConn) (int, error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, e
	}
	var pid int
	var inner error
	e = raw.Control(func(fd uintptr) { pid, inner = socketPID(int(fd)) })
	if e != nil {
		return 0, e
	}
	return pid, inner
}
