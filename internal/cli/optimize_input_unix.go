//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// NONBLOCK prevents a FIFO (including a raced pathname replacement) from
// hanging before optimizeRead can check the opened descriptor's regular type.
func openOptimizeInput(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
