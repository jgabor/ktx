package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jgabor/ktx/internal/binarycap"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/version"
)

type binaryCapabilitiesArgs struct {
	expectedSHA256 string
}

type binaryComparison struct {
	ExpectedSHA256 string `json:"expected_sha256"`
	Status         string `json:"status"`
}

type binaryCapabilitiesResponse struct {
	binarycap.Identity
	Comparison *binaryComparison `json:"comparison,omitempty"`
}

var currentBinaryCapabilities = func() (binarycap.Identity, error) {
	return binarycap.Current(version.Version, dbpkg.MaxSupportedSchema())
}

func init() {
	Register("binary-capabilities", RegisterOptions{
		Description:   "report DB-free executable identity and schema capability",
		FormatDefault: FormatJSON,
		ParseArgs:     parseBinaryCapabilitiesArgs,
		Help:          printBinaryCapabilitiesHelp,
	}, runBinaryCapabilities)
}

func parseBinaryCapabilitiesArgs(args []string) ([]string, any, error) {
	parsed := binaryCapabilitiesArgs{}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != "--expect-sha256" {
			rest = append(rest, args[i])
			continue
		}
		if parsed.expectedSHA256 != "" {
			return nil, nil, fmt.Errorf("--expect-sha256 may be supplied only once")
		}
		if i+1 == len(args) {
			return nil, nil, fmt.Errorf("--expect-sha256 requires a 64-character lowercase hexadecimal digest")
		}
		i++
		parsed.expectedSHA256 = args[i]
		if !binarycap.ValidDigest(parsed.expectedSHA256) {
			return nil, nil, fmt.Errorf("invalid --expect-sha256 %q: want exactly 64 lowercase hexadecimal characters", parsed.expectedSHA256)
		}
	}
	return rest, parsed, nil
}

func runBinaryCapabilities(ctx *DispatchContext) (any, error) {
	identity, err := currentBinaryCapabilities()
	if err != nil {
		failure := internalError("binary identity is unverifiable: %v; reinstall the executable at a readable stable path and retry it by absolute path", err)
		failure.Extra = map[string]any{"correction": "reinstall the executable at a readable stable path and retry it by absolute path"}
		return nil, failure
	}
	parsed, _ := ctx.ParsedArgs.(binaryCapabilitiesArgs)
	response := binaryCapabilitiesResponse{Identity: identity}
	if parsed.expectedSHA256 != "" {
		if identity.ExecutableSHA256 != parsed.expectedSHA256 {
			failure := conflictError("binary digest mismatch: install or select the verified executable and retry using its canonical absolute path")
			failure.Extra = map[string]any{
				"expected_sha256": parsed.expectedSHA256,
				"actual_sha256":   identity.ExecutableSHA256,
				"executable_path": identity.ExecutablePath,
				"correction":      "install or select the verified executable and retry using its canonical absolute path",
			}
			return nil, failure
		}
		response.Comparison = &binaryComparison{ExpectedSHA256: parsed.expectedSHA256, Status: "equal"}
	}

	if ctx.Format == FormatText {
		renderBinaryCapabilitiesText(ctx.Stdout, response)
		return response, nil
	}
	if err := json.NewEncoder(ctx.Stdout).Encode(response); err != nil {
		return nil, internalError("binary-capabilities: encode response: %v", err)
	}
	return response, nil
}

func renderBinaryCapabilitiesText(w io.Writer, response binaryCapabilitiesResponse) {
	fmt.Fprintf(w, "schema: %s\n", response.Schema)
	fmt.Fprintf(w, "executable path: %s\n", response.ExecutablePath)
	fmt.Fprintf(w, "executable sha256: %s\n", response.ExecutableSHA256)
	fmt.Fprintf(w, "build version: %s\n", response.BuildVersion)
	fmt.Fprintf(w, "maximum database schema: %d\n", response.MaxDatabaseSchema)
	if response.Comparison != nil {
		fmt.Fprintf(w, "comparison: %s (%s)\n", response.Comparison.Status, response.Comparison.ExpectedSHA256)
	}
}

func printBinaryCapabilitiesHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx binary-capabilities — report DB-free executable identity")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx binary-capabilities [--expect-sha256 DIGEST] [--format json|text]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Returns canonical executable path, executable SHA-256, build version, and maximum supported database schema.")
	fmt.Fprintln(w, "--expect-sha256 exits 0 when equal, 3 on mismatch, and accepts exactly 64 lowercase hexadecimal characters.")
	fmt.Fprintln(w, "Identity failures exit 4; reinstall at a readable stable path and retry by absolute path.")
}
