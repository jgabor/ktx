package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jgabor/ktx/internal/experiment"
)

const maxManifestBytes = 1 << 20

type experimentListEntry struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func experimentRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".ktx", "experiments"), nil
}

func readManifest(path string) (experiment.Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return experiment.Manifest{}, errors.New("manifest missing")
		}
		return experiment.Manifest{}, errors.New("manifest unreadable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return experiment.Manifest{}, errors.New("manifest unreadable")
	}
	if len(data) > maxManifestBytes {
		return experiment.Manifest{}, errors.New("manifest exceeds 1 MiB")
	}
	var manifest experiment.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return experiment.Manifest{}, errors.New("malformed manifest")
	}
	if err := experiment.ValidateExperimentID(manifest.ExperimentID); err != nil {
		return experiment.Manifest{}, errors.New("manifest has invalid experiment id")
	}
	return manifest, nil
}

func scanExperiments() ([]experimentListEntry, map[string][]string, error) {
	root, err := experimentRoot()
	if err != nil {
		return nil, nil, err
	}
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []experimentListEntry{}, map[string][]string{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	entries := make([]experimentListEntry, 0, len(dirs))
	paths := make(map[string][]string)
	for _, dir := range dirs {
		if !dir.IsDir() || dir.Type()&os.ModeSymlink != 0 {
			continue
		}
		manifestPath := filepath.Join(root, dir.Name(), "manifest.json")
		manifest, err := readManifest(manifestPath)
		if err != nil {
			entries = append(entries, experimentListEntry{ID: dir.Name(), Status: "malformed", Detail: err.Error()})
			continue
		}
		entries = append(entries, experimentListEntry{ID: manifest.ExperimentID, Status: "ok"})
		paths[manifest.ExperimentID] = append(paths[manifest.ExperimentID], filepath.Dir(manifestPath))
	}
	for id, matches := range paths {
		if len(matches) > 1 {
			for i := range entries {
				if entries[i].ID == id {
					entries[i].Status = "duplicate"
				}
			}
		}
	}
	return entries, paths, nil
}

func resolveExperiment(id string) (string, error) {
	if err := experiment.ValidateExperimentID(id); err != nil {
		return "", err
	}
	_, paths, err := scanExperiments()
	if err != nil {
		return "", err
	}
	matches := paths[id]
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("experiment id not found: %s", id)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("duplicate experiment id: %s", id)
	}
}

func runBenchList(args []string, opts Options, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		printError(stderr, usageError("ktx bench list: no arguments accepted"), opts.Format)
		return ExitUsage
	}
	entries, _, err := scanExperiments()
	if err != nil {
		printError(stderr, internalError("ktx bench list: %v", err), opts.Format)
		return ExitInternal
	}
	return encodeExperimentJSON(stdout, entries)
}

func runBenchShow(args []string, opts Options, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		printError(stderr, usageError("ktx bench show: usage: ktx bench show <id>"), opts.Format)
		return ExitUsage
	}
	dir, err := resolveExperiment(args[0])
	if err != nil {
		printError(stderr, usageError("ktx bench show: %v", err), opts.Format)
		return ExitUsage
	}
	manifest, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		printError(stderr, internalError("ktx bench show: %v", err), opts.Format)
		return ExitInternal
	}
	return encodeExperimentJSON(stdout, manifest)
}

func runBenchSummarize(args []string, opts Options, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		printError(stderr, usageError("ktx bench summarize: usage: ktx bench summarize <id>"), opts.Format)
		return ExitUsage
	}
	dir, err := resolveExperiment(args[0])
	if err != nil {
		printError(stderr, usageError("ktx bench summarize: %v", err), opts.Format)
		return ExitUsage
	}
	regeneration, err := experiment.RegenerateSummary(dir)
	if err != nil {
		printError(stderr, internalError("ktx bench summarize: %v", err), opts.Format)
		return ExitInternal
	}
	return renderExperimentValue(stdout, regeneration.Summary, opts.Format, experiment.RenderSummary)
}
