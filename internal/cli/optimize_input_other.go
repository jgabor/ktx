//go:build !unix

package cli

import (
	"fmt"
	"os"
)

// Retain ordinary file loading on unsupported non-Unix platforms. Only the
// native Unix path supplies the nonblocking-open guarantee; the shared reader
// still validates the opened descriptor before reading on every platform.
func openOptimizeInput(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a readable regular file")
	}
	return os.Open(path)
}
