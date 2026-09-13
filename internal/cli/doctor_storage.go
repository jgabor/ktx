package cli

import (
	"fmt"
	"os"
)

// doctorStorageChecks observes only the three named files, after database
// inspection has passed its access boundary. Availability is informational and
// does not change health status. Each stat is independent, not a SQLite snapshot.
func doctorStorageChecks(path string) []doctorCheck {
	checks := make([]doctorCheck, 0, 4)
	for _, file := range []struct{ name, suffix string }{
		{"database_file_size", ""},
		{"database_wal_file_size", "-wal"},
		{"database_shm_file_size", "-shm"},
	} {
		name := path + file.suffix
		info, err := os.Stat(name)
		var detail string
		switch {
		case os.IsNotExist(err):
			detail = fmt.Sprintf("absent: %q; file size not observed", name)
		case err != nil:
			detail = fmt.Sprintf("unavailable: %q; %v", name, err)
		case !info.Mode().IsRegular():
			detail = fmt.Sprintf("unavailable: %q; not a regular file", name)
		default:
			detail = fmt.Sprintf("observed: %q; file size %d bytes", name, info.Size())
		}
		checks = append(checks, doctorCheck{Name: file.name, Status: doctorStatusOK, Detail: detail})
	}
	return append(checks, doctorCheck{
		Name: "database_storage_scope", Status: doctorStatusOK,
		Detail: "informational file sizes; independently observed and may change concurrently, not an atomic SQLite snapshot, logical payload totals, allocated physical storage, or reclaimable space; run `ktx db report` separately with the same database selection for record totals and logical payload bytes; no aggregate queries or cleanup run by doctor",
	})
}
