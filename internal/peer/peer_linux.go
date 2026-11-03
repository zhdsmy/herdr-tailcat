package peer

import "golang.org/x/sys/unix"

func socketUID(fd int) (uint32, error) {
	c, e := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if e != nil {
		return 0, e
	}
	return c.Uid, nil
}
func ParentDeath() { _ = unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGTERM), 0, 0, 0) }

func socketPID(fd int) (int, error) {
	c, e := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if e != nil {
		return 0, e
	}
	return int(c.Pid), nil
}
