package cli

import (
	"os"
	"path/filepath"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func sourceDatabaseDestinationError(command, failureClass, sourcePath string) *Error {
	err := usageError("%s: --output resolves to the source database %q; refusing to replace source evidence", command, sourcePath)
	err.Extra = map[string]any{
		"failure_class": failureClass,
		"correction":    "use --output - for stdout or choose a file that is distinct from the source database",
	}
	return err
}

func destinationAliasesSource(sourcePath, outputPath string) (bool, error) {
	if outputPath == "-" {
		return false, nil
	}
	if err := dbpkg.AuditPathInspection(sourcePath); err != nil {
		return false, err
	}
	sourceAbsolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return false, err
	}
	outputAbsolute, err := filepath.Abs(outputPath)
	if err != nil {
		return false, err
	}
	if sourceAbsolute == outputAbsolute {
		return true, nil
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return false, err
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return os.SameFile(sourceInfo, outputInfo), nil
}
