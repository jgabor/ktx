package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	databaseAccessAuditEnv    = "KTX_VERIFY_DB_AUDIT"
	forbiddenDatabasePathsEnv = "KTX_VERIFY_FORBIDDEN_DB_PATHS"
	verificationRootEnv       = "KTX_VERIFY_ROOT"
	maxAuditPathBytes         = 2048
)

type databaseAccessRecord struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Forbidden bool   `json:"forbidden"`
}

// AuditPathInspection records a database-path inspection before the caller
// performs it. It is a no-op outside the verification environment.
func AuditPathInspection(path string) error {
	return auditDatabaseAccess("inspect", path)
}

func auditDatabaseAccess(operation, path string) error {
	normalized := lexicalPath(path)
	root, rootBound := os.LookupEnv(verificationRootEnv)
	outsideRoot := rootBound && !pathWithinVerificationRoot(root, normalized)
	auditPath := os.Getenv(databaseAccessAuditEnv)
	if auditPath == "" {
		if outsideRoot {
			return fmt.Errorf("db: verification blocked database outside disposable root %q", normalized)
		}
		return nil
	}
	forbidden, err := forbiddenDatabasePaths()
	if err != nil {
		return fmt.Errorf("db: access audit configuration: %w", err)
	}
	blocked := outsideRoot
	for _, candidate := range forbidden {
		if normalized == lexicalPath(candidate) {
			blocked = true
			break
		}
	}
	recordPath := normalized
	if len(recordPath) > maxAuditPathBytes {
		digest := sha256.Sum256([]byte(recordPath))
		recordPath = recordPath[:maxAuditPathBytes] + "...#sha256=" + hex.EncodeToString(digest[:])
	}
	line, err := json.Marshal(databaseAccessRecord{Operation: operation, Path: recordPath, Forbidden: blocked})
	if err != nil {
		return fmt.Errorf("db: encode access audit: %w", err)
	}
	line = append(line, '\n')
	file, err := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("db: open access audit: %w", err)
	}
	_, writeErr := file.Write(line)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("db: write access audit: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("db: close access audit: %w", closeErr)
	}
	if blocked {
		if outsideRoot {
			return fmt.Errorf("db: verification blocked database outside disposable root %q", recordPath)
		}
		return fmt.Errorf("db: verification blocked forbidden database %q", recordPath)
	}
	return nil
}

func forbiddenDatabasePaths() ([]string, error) {
	raw := os.Getenv(forbiddenDatabasePathsEnv)
	if raw == "" {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		return nil, err
	}
	for _, path := range paths {
		if path == "" {
			return nil, errors.New("forbidden database path is empty")
		}
	}
	return paths, nil
}

func lexicalPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func pathWithinVerificationRoot(root, path string) bool {
	if root == "" {
		return false
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootPath, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
