// Command ktx is the runtime-agnostic CLI that makes coding agents
// measurable and cheaper. One Go binary, no library import, no daemon.
//
// main is a thin wrapper around internal/cli.Run; all command logic lives
// in internal/cli so it stays testable without touching os.Args.
package main

import (
	"os"

	"github.com/jgabor/ktx/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
