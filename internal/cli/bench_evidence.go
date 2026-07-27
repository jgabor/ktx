package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jgabor/ktx/internal/cache/reuse"
)

func runBenchEvidence(args []string, opts Options, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "preflight" && args[0] != "gemini-preflight") {
		printError(stderr, usageError("ktx bench evidence: expected 'preflight' or 'gemini-preflight'"), opts.Format)
		return ExitUsage
	}
	if args[0] == "gemini-preflight" {
		return runBenchGeminiPreflight(args[1:], opts, stdout, stderr)
	}
	fs := flag.NewFlagSet("ktx bench evidence preflight", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var specPath, outputPath string
	fs.StringVar(&specPath, "spec", "", "proposal JSON path")
	fs.StringVar(&outputPath, "output", "", "retained preflight report path")
	if err := fs.Parse(args[1:]); err != nil {
		printError(stderr, usageError("ktx bench evidence preflight: %v", err), opts.Format)
		return ExitUsage
	}
	if specPath == "" || fs.NArg() != 0 {
		printError(stderr, usageError("ktx bench evidence preflight: --spec is required and positional arguments are not accepted"), opts.Format)
		return ExitUsage
	}
	proposal, err := reuse.LoadProposal(specPath)
	if err != nil {
		printError(stderr, usageError("ktx bench evidence preflight: %v", err), opts.Format)
		return ExitUsage
	}
	report, err := reuse.Preflight(proposal, reuse.LocalEnvironment{})
	if err != nil {
		printError(stderr, usageError("ktx bench evidence preflight: %v", err), opts.Format)
		return ExitUsage
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		printError(stderr, internalError("ktx bench evidence preflight: encode report: %v", err), opts.Format)
		return ExitInternal
	}
	encoded = append(encoded, '\n')
	if outputPath != "" {
		if err := writePreflightReport(outputPath, encoded); err != nil {
			printError(stderr, internalError("ktx bench evidence preflight: write report: %v", err), opts.Format)
			return ExitInternal
		}
	}
	if opts.Format == FormatText {
		fmt.Fprintf(stdout, "ready=%t network_activity=%s billable_requests=%d\n", report.Ready, report.NetworkActivity, report.BillableRequests)
		for _, entry := range report.Capabilities {
			fmt.Fprintf(stdout, "%s source=%s version=%s provider=%s model=%s variant=%s access=%s requests=%d attempts=%d spend_usd=%.6f wall_time=%s artifact=%s redaction=%q unavailable=%q\n",
				entry.Name, entry.Source, entry.Version, entry.Provider, entry.Model, entry.Variant,
				entry.Access.State, entry.NativeRequestMaximum, entry.AttemptMaximum, entry.SpendMaximumUSD,
				entry.WallTimeMaximum, entry.ArtifactDestination, entry.RedactionPolicy, entry.Access.UnavailableCondition)
		}
		return ExitOK
	}
	_, _ = stdout.Write(encoded)
	return ExitOK
}

func runBenchGeminiPreflight(args []string, opts Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ktx bench evidence gemini-preflight", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var specPath, outputPath string
	fs.StringVar(&specPath, "spec", "", "Gemini proposal JSON path")
	fs.StringVar(&outputPath, "output", "", "retained preflight report path")
	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx bench evidence gemini-preflight: %v", err), opts.Format)
		return ExitUsage
	}
	if specPath == "" || fs.NArg() != 0 {
		printError(stderr, usageError("ktx bench evidence gemini-preflight: --spec is required and positional arguments are not accepted"), opts.Format)
		return ExitUsage
	}
	proposal, err := reuse.LoadGeminiProposal(specPath)
	if err != nil {
		printError(stderr, usageError("ktx bench evidence gemini-preflight: %v", err), opts.Format)
		return ExitUsage
	}
	report, err := reuse.GeminiPreflight(proposal, reuse.LocalEnvironment{})
	if err != nil {
		printError(stderr, usageError("ktx bench evidence gemini-preflight: %v", err), opts.Format)
		return ExitUsage
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		printError(stderr, internalError("ktx bench evidence gemini-preflight: encode report: %v", err), opts.Format)
		return ExitInternal
	}
	encoded = append(encoded, '\n')
	if outputPath != "" {
		if err := writePreflightReport(outputPath, encoded); err != nil {
			printError(stderr, internalError("ktx bench evidence gemini-preflight: write report: %v", err), opts.Format)
			return ExitInternal
		}
	}
	if opts.Format == FormatText {
		fmt.Fprintf(stdout, "ready=%t network_activity=%s billable_requests=%d\n", report.Ready, report.NetworkActivity, report.BillableRequests)
		for _, entry := range report.Capabilities {
			fmt.Fprintf(stdout, "%s source=%s version=%s provider=%s model=%s access=%s requests=%d attempts=%d spend_usd=%.6f wall_time=%s artifact=%s redaction=%q unavailable=%q\n",
				entry.Name, entry.Source, entry.Version, entry.Provider, entry.Model,
				entry.Access.State, entry.NativeRequestMaximum, entry.AttemptMaximum, entry.SpendMaximumUSD,
				entry.WallTimeMaximum, entry.ArtifactDestination, entry.RedactionPolicy, entry.Access.UnavailableCondition)
		}
		return ExitOK
	}
	_, _ = stdout.Write(encoded)
	return ExitOK
}

func writePreflightReport(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".preflight-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
