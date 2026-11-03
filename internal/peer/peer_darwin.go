package peer

import "golang.org/x/sys/unix"

func socketUID(fd int) (uint32, error) {
	c, e := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if e != nil {
		return 0, e
	}
	return c.Uid, nil
}
func ParentDeath() {}

func socketPID(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}
