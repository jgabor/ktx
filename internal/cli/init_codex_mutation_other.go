//go:build !linux

package cli

import (
	"fmt"
	"os"
)

func lockCodexDirectory(*os.File) error {
	return fmt.Errorf("coordinated Codex hook updates require Linux")
}

func openCodexDocument(path string) (*os.File, error) { return os.Open(path) }
