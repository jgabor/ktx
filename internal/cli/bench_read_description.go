package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jgabor/ktx/internal/experiment"
)

func runReadDescriptionTurns(args []string, followup, directory string, stdout, stderr io.Writer) error {
	version, err := exec.Command("opencode", "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "1.18.5" {
		return errors.New("read-description treatment requires OpenCode 1.18.5")
	}
	plugin, err := readInstalledBenchPlugin()
	if err != nil {
		return err
	}
	if !plugin.sameCommitPackage() {
		return errors.New("read-description treatment requires the OpenCode package from the binary's commit as the only plugin")
	}
	turns := 1
	if followup != "" {
		turns = 2
	}
	session := ""
	for turn := 1; turn <= turns; turn++ {
		path := filepath.Join(directory, fmt.Sprintf("turn-%d.jsonl", turn))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		commandArgs := args
		if turn == 2 {
			commandArgs = append(append([]string{}, args[:len(args)-1]...), "--session", session, followup)
		}
		command := exec.Command("opencode", commandArgs...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, io.MultiWriter(file, stdout), stderr
		err = command.Run()
		if err == nil {
			_, err = file.Seek(0, io.SeekStart)
			if err == nil {
				var observed string
				observed, err = experiment.RuntimeTurnSession(file)
				if err == nil && session != "" && observed != session {
					err = errors.New("follow-up switched runtime sessions")
				}
				session = observed
			}
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
	}
	return nil
}
