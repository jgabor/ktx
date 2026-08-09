//go:build linux

package main

import (
	"fmt"
	"syscall"
)

func filesystemIdentityFor(path string) filesystemIdentity {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return filesystemIdentity{Type: "unavailable"}
	}
	return filesystemIdentity{Type: fmt.Sprintf("0x%x", uint64(stat.Type)), BlockSize: int64(stat.Bsize)}
}
