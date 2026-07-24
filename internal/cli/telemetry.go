package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/opencodetelemetry"
)

const (
	defaultTelemetryLimit = 100
	maxTelemetryLimit     = 1000
)

type telemetryRecordInput struct {
	KtxSessionID string   `json:"ktx_session_id"`
	SessionID    string   `json:"session_id"`
	MessageID    string   `json:"message_id"`
	PartID       string   `json:"part_id"`
	Ordinal      *uint64  `json:"ordinal"`
	Reason       string   `json:"reason"`
	Cost         *float64 `json:"cost"`
	Tokens       struct {
		Input      *int64 `json:"input"`
		Output     *int64 `json:"output"`
		Reasoning  *int64 `json:"reasoning"`
		CacheRead  *int64 `json:"cache_read"`
		CacheWrite *int64 `json:"cache_write"`
	} `json:"tokens"`
	Provider *string `json:"provider"`
	Model    *string `json:"model"`
}

type telemetryListResult struct {
	SessionID    string                   `json:"session_id"`
	Limit        int                      `json:"limit"`
	Observations []dbpkg.SessionTelemetry `json:"observations"`
}

func telemetryLogic(args []string, format string, stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer) (any, error) {
	if len(args) == 0 {
		return nil, usageError("ktx telemetry: missing subcommand (want record or list)")
	}
	switch args[0] {
	case "record":
		return nil, recordTelemetry(args[1:], stdin, db, stderr)
	case "list":
		return nil, listTelemetry(args[1:], format, db, stdout)
	default:
		return nil, usageError("ktx telemetry: unknown subcommand %q (want record or list)", args[0])
	}
}

func recordTelemetry(args []string, stdin io.Reader, db *dbpkg.DB, stderr io.Writer) error {
	fs := flag.NewFlagSet("ktx telemetry record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageError("ktx telemetry record: %v", err)
	}
	if len(fs.Args()) != 0 {
		return usageError("ktx telemetry record: unexpected argument %q", fs.Arg(0))
	}
	body, err := io.ReadAll(stdin)
	if err != nil {
		return internalError("ktx telemetry record: read stdin: %v", err)
	}
	if len(body) == 0 {
		return usageError("ktx telemetry record: no input on stdin")
	}
	var input telemetryRecordInput
	if err := json.Unmarshal(body, &input); err != nil {
		return usageError("ktx telemetry record: parse JSON: %v", err)
	}
	observation, err := input.observation()
	if err != nil {
		return usageError("ktx telemetry record: %v", err)
	}
	if err := opencodetelemetry.ValidateObservation(observation); err != nil {
		return usageError("ktx telemetry record: %v", err)
	}
	capturedAt := time.Now().UTC().Format(time.RFC3339Nano)
	sessionID, err := db.ResolveSessionID(input.KtxSessionID, capturedAt)
	if err == nil {
		_, err = db.PersistSessionTelemetry(sessionID, capturedAt, observation)
	}
	if err != nil {
		// Session telemetry is optional. Keep the failure visible to the operator,
		// but do not turn this side channel into a failed agent operation.
		fmt.Fprintf(stderr, "ktx: telemetry not recorded: %v\n", err)
	}
	return nil
}

func (input telemetryRecordInput) observation() (*opencodetelemetry.Observation, error) {
	missing := ""
	switch {
	case input.Ordinal == nil:
		missing = "ordinal"
	case input.Cost == nil:
		missing = "cost"
	case input.Tokens.Input == nil:
		missing = "tokens.input"
	case input.Tokens.Output == nil:
		missing = "tokens.output"
	case input.Tokens.Reasoning == nil:
		missing = "tokens.reasoning"
	case input.Tokens.CacheRead == nil:
		missing = "tokens.cache_read"
	case input.Tokens.CacheWrite == nil:
		missing = "tokens.cache_write"
	}
	if missing != "" {
		return nil, fmt.Errorf("missing required field: %s", missing)
	}
	return &opencodetelemetry.Observation{
		Key: opencodetelemetry.Key{
			SessionID: input.SessionID,
			MessageID: input.MessageID,
			PartID:    input.PartID,
		},
		Ordinal: *input.Ordinal,
		Reason:  input.Reason,
		Cost:    *input.Cost,
		Tokens: opencodetelemetry.Tokens{
			Input:      *input.Tokens.Input,
			Output:     *input.Tokens.Output,
			Reasoning:  *input.Tokens.Reasoning,
			CacheRead:  *input.Tokens.CacheRead,
			CacheWrite: *input.Tokens.CacheWrite,
		},
		Provider: input.Provider,
		Model:    input.Model,
	}, nil
}

func listTelemetry(args []string, format string, db *dbpkg.DB, stdout io.Writer) error {
	fs := flag.NewFlagSet("ktx telemetry list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var sessionID string
	limit := defaultTelemetryLimit
	fs.StringVar(&sessionID, "session", "", "ktx session ID")
	fs.IntVar(&limit, "limit", defaultTelemetryLimit, "maximum observations")
	if err := fs.Parse(args); err != nil {
		return usageError("ktx telemetry list: %v", err)
	}
	if len(fs.Args()) != 0 {
		return usageError("ktx telemetry list: unexpected argument %q", fs.Arg(0))
	}
	if sessionID == "" {
		return usageError("ktx telemetry list: --session is required")
	}
	if limit < 1 || limit > maxTelemetryLimit {
		return usageError("ktx telemetry list: --limit must be between 1 and %d", maxTelemetryLimit)
	}
	if format != FormatJSON && format != FormatText {
		return usageError("ktx telemetry list: invalid --format %q: want %q or %q", format, FormatJSON, FormatText)
	}
	exists, err := db.SessionExists(sessionID)
	if err != nil {
		return internalError("ktx telemetry list: inspect session: %v", err)
	}
	if !exists {
		return notFoundError("ktx telemetry list: session %q not found", sessionID)
	}
	observations, err := db.ListSessionTelemetry(sessionID, limit)
	if err != nil {
		return internalError("ktx telemetry list: %v", err)
	}
	result := telemetryListResult{SessionID: sessionID, Limit: limit, Observations: observations}
	if format == FormatText {
		printTelemetryText(stdout, observations)
		return nil
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return internalError("ktx telemetry list: encode JSON: %v", err)
	}
	return nil
}

func printTelemetryText(w io.Writer, observations []dbpkg.SessionTelemetry) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ORDINAL\tSOURCE SESSION\tMESSAGE\tPART\tCACHE READ\tCACHE WRITE\tPROVIDER\tMODEL")
	for _, observation := range observations {
		provider, model := "-", "-"
		if observation.Provider != nil {
			provider = *observation.Provider
		}
		if observation.Model != nil {
			model = *observation.Model
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			observation.Ordinal, observation.SourceSessionID, observation.MessageID, observation.PartID,
			measurementText(observation.CacheRead), measurementText(observation.CacheWrite), provider, model)
	}
	_ = tw.Flush()
}

func measurementText(measurement dbpkg.IntegerMeasurement) string {
	if measurement.Value == nil {
		return measurement.State
	}
	return strconv.FormatInt(*measurement.Value, 10) + " (" + measurement.State + ")"
}

func telemetryCommandFunc(ctx *DispatchContext) (any, error) {
	if len(ctx.Args) == 0 || (ctx.Args[0] != "record" && ctx.Args[0] != "list") {
		return telemetryLogic(ctx.Args, ctx.Format, ctx.Stdin, nil, ctx.Stdout, ctx.Stderr)
	}
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		return telemetryDatabaseFailure(ctx, fmt.Errorf("resolve database path: %w", err))
	}
	db, err := dbpkg.Open(path)
	if err != nil {
		return telemetryDatabaseFailure(ctx, fmt.Errorf("open database: %w", err))
	}
	defer db.Close()
	return telemetryLogic(ctx.Args, ctx.Format, ctx.Stdin, db, ctx.Stdout, ctx.Stderr)
}

func telemetryDatabaseFailure(ctx *DispatchContext, err error) (any, error) {
	if len(ctx.Args) > 0 && ctx.Args[0] == "record" {
		fmt.Fprintf(ctx.Stderr, "ktx: telemetry not recorded: %v\n", err)
		return nil, nil
	}
	return nil, internalError("ktx telemetry: %v", err)
}

func init() {
	Register("telemetry", RegisterOptions{
		Description:   "record and inspect optional session telemetry",
		NeedsStdin:    true,
		FormatDefault: FormatJSON,
		Help:          printTelemetryHelp,
	}, telemetryCommandFunc)
}

func printTelemetryHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx telemetry — record and inspect optional session telemetry")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  ktx telemetry record < observation.json")
	fmt.Fprintln(w, "  ktx telemetry list --session ID [--limit N] [--format json|text]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "record accepts one normalized OpenCode model-step observation. Persistence")
	fmt.Fprintln(w, "is best-effort: failures are reported on stderr without failing the command.")
	fmt.Fprintf(w, "This is %s: %s.\n", opencodeTelemetryEvidence, opencodeTelemetryClaimBoundary)
	fmt.Fprintln(w, "list returns at most 100 observations by default; --limit accepts 1 through 1000.")
}
