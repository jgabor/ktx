//go:build !linux

package cli

import (
	"fmt"
	"os"
)

type optimizeTargetMutation struct{}

func beginOptimizeTarget(string, bool) (*optimizeTargetMutation, error) {
	return nil, fmt.Errorf("configuration mutation requires Linux")
}
func checkOptimizeTarget(string) error {
	return fmt.Errorf("configuration mutation requires Linux")
}
func (*optimizeTargetMutation) close() {}
func (*optimizeTargetMutation) read() ([]byte, os.FileInfo, error) {
	return nil, nil, fmt.Errorf("configuration mutation requires Linux")
}
func (*optimizeTargetMutation) publish([]byte, os.FileMode, int, optimizeMutationHooks) error {
	return fmt.Errorf("configuration mutation requires Linux")
}
func (*optimizeTargetMutation) remove(optimizeMutationHooks) error {
	return fmt.Errorf("configuration mutation requires Linux")
}
func optimizeFileGroup(os.FileInfo) int            { return -1 }
func optimizeNewGroup(*optimizeTargetMutation) int { return -1 }
