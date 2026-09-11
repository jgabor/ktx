//go:build !linux

package experiment

import (
	"fmt"
	"os"
)

func openArchiveRegular(_ *os.Root, _ string) (*os.File, error) {
	return nil, fmt.Errorf("archive inspection requires Linux")
}

func openArchiveNode(_ *os.Root, _ string, _ bool) (*os.File, error) {
	return nil, fmt.Errorf("archive inspection requires Linux")
}

func (a *archive) databaseName(_ *os.File) (string, error) {
	return "", fmt.Errorf("archive inspection requires Linux")
}
