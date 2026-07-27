// Package binarycap identifies an executable without consulting application
// state or a database.
package binarycap

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const Schema = "ktx.binary-capabilities/v1"

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Identity is the stable binary capability response.
type Identity struct {
	Schema            string `json:"schema"`
	ExecutablePath    string `json:"executable_path"`
	ExecutableSHA256  string `json:"executable_sha256"`
	BuildVersion      string `json:"build_version"`
	MaxDatabaseSchema int    `json:"max_database_schema"`
}

// Current inspects the executable running this process.
func Current(buildVersion string, maxDatabaseSchema int) (Identity, error) {
	path, err := os.Executable()
	if err != nil {
		return Identity{}, fmt.Errorf("resolve running executable: %w", err)
	}
	return Inspect(path, buildVersion, maxDatabaseSchema)
}

// Inspect canonicalizes and hashes path, then binds the supplied build
// capabilities to those exact executable bytes.
func Inspect(path, buildVersion string, maxDatabaseSchema int) (Identity, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Identity{}, fmt.Errorf("make executable path absolute: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Identity{}, fmt.Errorf("canonicalize executable path: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return Identity{}, fmt.Errorf("make canonical executable path absolute: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Identity{}, fmt.Errorf("stat executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Identity{}, fmt.Errorf("stat executable: %q is not a regular file", canonical)
	}

	file, err := os.Open(canonical)
	if err != nil {
		return Identity{}, fmt.Errorf("open executable: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return Identity{}, fmt.Errorf("hash executable: %w", err)
	}
	if err := file.Close(); err != nil {
		return Identity{}, fmt.Errorf("close executable after hashing: %w", err)
	}

	return Identity{
		Schema: Schema, ExecutablePath: filepath.Clean(canonical),
		ExecutableSHA256: fmt.Sprintf("%x", hash.Sum(nil)),
		BuildVersion:     buildVersion, MaxDatabaseSchema: maxDatabaseSchema,
	}, nil
}

// ValidDigest reports whether value is canonical lowercase SHA-256 text.
func ValidDigest(value string) bool { return digestPattern.MatchString(value) }
