package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialReadinessFailureReleasesResources(t *testing.T) {
	for name, open := range map[string]func(string) (*DB, error){"open": Open, "existing": OpenExisting} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rejected.db")
			seed, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			actual := MaxSupportedSchema() + 7
			if _, err := seed.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", actual)); err != nil {
				seed.Close()
				t.Fatal(err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				database, err := open(path)
				if database != nil {
					database.Close()
					t.Fatal("failed readiness returned a database")
				}
				var newer *SchemaTooNewError
				if !errors.As(err, &newer) || newer.Actual != actual || newer.Maximum != MaxSupportedSchema() {
					t.Fatalf("readiness error = %v", err)
				}
				// Inspect only this disposable target's descriptors, not a
				// process-wide count that unrelated parallel tests can change.
				fds, err := os.ReadDir("/proc/self/fd")
				if err != nil {
					t.Fatal(err)
				}
				for _, fd := range fds {
					target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if target == path || strings.HasPrefix(target, path+"-") {
						t.Errorf("failed readiness retained descriptor %s for %s", fd.Name(), target)
					}
				}
				coordinator, err := newWriterCoordinator(path)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), startupRetryDeadline)
				err = coordinator.withLock(ctx, false, func() error { return nil })
				cancel()
				if err != nil {
					t.Fatalf("readiness retained coordinator lock: %v", err)
				}
			}
		})
	}
}
