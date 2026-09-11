// This offline fixture seeds only the explicitly supplied disposable path.
package main

import (
	"os"

	"github.com/jgabor/ktx/internal/db"
)

func main() {
	if len(os.Args) != 2 {
		panic("require disposable database path")
	}
	d, err := db.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	_, err = d.EnsureCollectionInterval("collectionabcdef", []string{"run"}, "2026-08-01T00:00:00Z")
	closeErr := d.Close()
	if err != nil {
		panic(err)
	}
	if closeErr != nil {
		panic(closeErr)
	}
}
