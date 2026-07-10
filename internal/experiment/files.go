package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func directorySHA256(root string) (string, int, error) {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "", 0, err
	}
	sort.Strings(paths)
	digest := sha256.New()
	count := 0
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return "", 0, err
		}
		relative, _ := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", 0, err
			}
			fmt.Fprintf(digest, "link\x00%s\x00%s\x00", relative, target)
			count++
		} else if info.Mode().IsRegular() {
			fmt.Fprintf(digest, "file\x00%s\x00", relative)
			file, err := os.Open(path)
			if err != nil {
				return "", 0, err
			}
			_, copyErr := io.Copy(digest, file)
			closeErr := file.Close()
			if copyErr != nil {
				return "", 0, copyErr
			}
			if closeErr != nil {
				return "", 0, closeErr
			}
			digest.Write([]byte{0})
			count++
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), count, nil
}

func copyTree(source, destination string, skip func(string, fs.DirEntry) bool) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o755)
		}
		relative = filepath.ToSlash(relative)
		if skip != nil && skip(relative, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputErr, outputErr := input.Close(), output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputErr != nil {
			return inputErr
		}
		return outputErr
	})
}

func prepareWorkspace(repositoryRoot, workspace, mode string, corpusFiles []string) (map[string]any, error) {
	if err := os.RemoveAll(workspace); err != nil {
		return nil, err
	}
	switch mode {
	case "corpus":
		destination := filepath.Join(workspace, "tools", "bench", "corpus")
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return nil, err
		}
		copied := []string{}
		for _, name := range corpusFiles {
			if filepath.Base(name) != name || filepath.Ext(name) != ".txt" || name == "MANIFEST.json" {
				return nil, fmt.Errorf("refusing to mount non-fixture corpus file: %s", name)
			}
			source := filepath.Join(repositoryRoot, "tools", "bench", "corpus", name)
			if err := copyTreeFile(source, filepath.Join(destination, name)); err != nil {
				return nil, err
			}
			copied = append(copied, filepath.ToSlash(filepath.Join("tools", "bench", "corpus", name)))
		}
		hash, count, err := directorySHA256(workspace)
		return map[string]any{"sha256": hash, "file_count": count, "mode": mode, "files": copied}, err
	case "repo":
		skip := func(relative string, entry fs.DirEntry) bool {
			base := filepath.Base(relative)
			if base == ".git" || base == ".bench-data" || base == "__pycache__" {
				return true
			}
			answerKeys := []string{
				"docs/pipelines.md", "tools/bench/tasks", "tools/bench/corpus/MANIFEST.json",
				"internal/experiment/corpus.go", "internal/experiment/experiment_test.go", "internal/experiment/testdata",
			}
			for _, key := range answerKeys {
				if relative == key || strings.HasPrefix(relative, key+"/") {
					return true
				}
			}
			return false
		}
		if err := copyTree(repositoryRoot, workspace, skip); err != nil {
			return nil, err
		}
		hash, count, err := directorySHA256(workspace)
		return map[string]any{"sha256": hash, "file_count": count, "mode": mode, "files": nil}, err
	default:
		return nil, fmt.Errorf("unsupported workspace mode: %q", mode)
	}
}

func copyTreeFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
