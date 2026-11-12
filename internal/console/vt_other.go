//go:build !linux

package console

import (
	"errors"
	"os"
)

// Foreground is Linux only: the helper runs on the target.
func Foreground(tty *os.File) (restore func(), err error) {
	return nil, errors.New("virtual terminals are Linux only")
}
