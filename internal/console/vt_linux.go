//go:build linux

package console

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The virtual terminal ioctls of linux/vt.h.
const (
	vtGetState   = 0x5603
	vtActivate   = 0x5606
	vtWaitActive = 0x5607
)

// vtStat is struct vt_stat.
type vtStat struct {
	active, signal, state uint16
}

// Foreground brings the virtual terminal tty is on to the screen and returns
// a function that brings back the one that was there before. The helper calls
// it on its own terminal.
func Foreground(tty *os.File) (restore func(), err error) {
	link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", tty.Fd()))
	if err != nil {
		return nil, err
	}
	var n int
	if _, err := fmt.Sscanf(link, "/dev/tty%d", &n); err != nil || n < 1 {
		return nil, fmt.Errorf("%s is not a virtual terminal", link)
	}
	fd := int(tty.Fd())
	var st vtStat
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vtGetState, uintptr(unsafe.Pointer(&st))); errno != 0 {
		return nil, fmt.Errorf("read the active terminal: %w", errno)
	}
	if err := unix.IoctlSetInt(fd, vtActivate, n); err != nil {
		return nil, fmt.Errorf("switch to tty%d: %w", n, err)
	}
	_ = unix.IoctlSetInt(fd, vtWaitActive, n)
	before := int(st.active)
	return func() {
		if before != n && before > 0 {
			_ = unix.IoctlSetInt(fd, vtActivate, before)
		}
	}, nil
}
