//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package logging

import (
	"os"

	"golang.org/x/sys/unix"
)

func openNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func rejectPublicPermissions(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.ErrPermission
	}
	return nil
}
