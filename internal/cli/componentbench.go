package cli

import "io"

// RunForComponentBench runs the production command path with injected streams.
// It is used only by the developer component benchmark.
func RunForComponentBench(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr)
}
