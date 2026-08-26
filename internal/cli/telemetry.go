package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/telemetry"
	"github.com/jgabor/ktx/internal/telemetry/opencode"
)

const (
	defaultTelemetryLimit = 100
	maxTelemetryLimit     = 1000
)

type telemetryRecordInput struct {
	KtxSessionID string          `json:"ktx_session_id"`
	SessionID    string          `json:"session_id"`
	MessageID    string          `json:"message_id"`
	PartID       string          `json:"part_id"`
	Ordinal      *uint64         `json:"ordinal"`
	Reason       string          `json:"reason"`
	Cost         json.RawMessage `json:"cost"`
	Tokens       struct {
		Input      json.RawMessage `json:"input"`
		Output     json.RawMessage `json:"output"`
		Reasoning  json.RawMessage `json:"reasoning"`
		CacheRead  json.RawMessage `json:"cache_read"`
		CacheWrite json.RawMessage `json:"cache_write"`
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
	case "collector-open":
		return nil, recordCollectorOpen(args[1:], stdin, db, stdout)
	case "event-record":
		return nil, recordRuntimeEvent(args[1:], stdin, db, stdout)
	case "cue-delivered":
		return nil, acknowledgeRecoveryCue(args[1:], stdin, db, stdout)
	case "list":
		return nil, listTelemetry(args[1:], format, db, stdout)
	default:
		return nil, usageError("ktx telemetry: unknown subcommand %q (want record or list)", args[0])
	}
}

type recoveryCueAckInput struct {
	SourceID, ArtifactID, SessionID, RuntimeSessionID, CallID string
}

func (i *recoveryCueAckInput) UnmarshalJSON(data []byte) error {
	type wire struct {
		SourceID         string `json:"source_id"`
		ArtifactID       string `json:"artifact_id"`
		SessionID        string `json:"session_id"`
		RuntimeSessionID string `json:"runtime_session_id"`
		CallID           string `json:"call_id"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	i.SourceID, i.ArtifactID, i.SessionID, i.RuntimeSessionID, i.CallID = w.SourceID, w.ArtifactID, w.SessionID, w.RuntimeSessionID, w.CallID
	return nil
}

func acknowledgeRecoveryCue(args []string, stdin io.Reader, db *dbpkg.DB, stdout io.Writer) error {
	if len(args) != 0 {
		return usageError("ktx telemetry cue-delivered: unexpected arguments")
	}
	collectionID, sessionID, present, err := recoveryEvidenceOwner(db)
	if err != nil {
		return usageError("ktx telemetry cue-delivered: ownership mismatch: %v", err)
	}
	if !present {
		return usageError("ktx telemetry cue-delivered: collection ownership is absent")
	}
	var in recoveryCueAckInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return usageError("ktx telemetry cue-delivered: parse input: %v", err)
	}
	if in.SessionID != sessionID {
		return usageError("ktx telemetry cue-delivered: session ownership mismatch")
	}
	src, err := correlation.Resolve(correlation.NativeTuple{Runtime: "opencode", SessionID: in.RuntimeSessionID, CallID: in.CallID}, correlation.Trusted(correlation.ProvenanceOpenCodeHook))
	if err != nil || src.ID != in.SourceID {
		return usageError("ktx telemetry cue-delivered: native identity mismatch")
	}
	err = db.AcknowledgeRecoveryCue(dbpkg.RecoveryCueDelivery{CollectionID: collectionID, SourceID: in.SourceID, ArtifactID: in.ArtifactID, SessionID: sessionID, RuntimeSessionID: in.RuntimeSessionID, RuntimeCallID: in.CallID, DeliveredAt: time.Now().UTC().Format(time.RFC3339Nano), Authority: dbpkg.RecoveryDeliveryAuthority})
	if err != nil {
		return conflictError("ktx telemetry cue-delivered: acknowledgement conflict: %v", err)
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"ok": true, "collection_id": collectionID, "session_id": sessionID, "source_id": in.SourceID, "artifact_id": in.ArtifactID})
}

func recoveryEvidenceOwner(database *dbpkg.DB) (collectionID, sessionID string, present bool, err error) {
	benchmark, benchmarkPresent, benchmarkErr := benchcontract.ReadEnvironment()
	if benchmarkPresent {
		if benchmarkErr != nil {
			return "", "", true, benchmarkErr
		}
		if os.Getenv("KTX_COLLECTION_ID") != "" || os.Getenv("KTX_SESSION_ID") != "" {
			return "", "", true, errors.New("benchmark and explicit collection ownership conflict")
		}
		interval, loadErr := database.LoadRuntimeEventInterval(benchmark.IntervalID)
		if loadErr != nil || interval.SessionID != benchmark.SessionID || interval.Runtime != benchcontract.Runtime || interval.RuntimeVersion != benchcontract.RuntimeVersion || interval.Source != dbpkg.RuntimeEventSource || interval.ContractVersion != benchcontract.ContractVersion || interval.CloseState != dbpkg.RuntimeOpen || interval.CollectorEpochCount == 0 {
			return "", "", true, errors.New("benchmark interval owner is unavailable")
		}
		var collectionCount int
		if queryErr := database.DB().QueryRow(`SELECT count(*) FROM collection_intervals WHERE id=? AND supported_ingresses='["run"]'`, interval.ID).Scan(&collectionCount); queryErr != nil || collectionCount != 1 {
			return "", "", true, errors.New("benchmark collection owner is unavailable")
		}
		return interval.ID, interval.SessionID, true, nil
	}
	collectionID = strings.TrimSpace(os.Getenv("KTX_COLLECTION_ID"))
	sessionID = strings.TrimSpace(os.Getenv("KTX_SESSION_ID"))
	if collectionID == "" && sessionID == "" {
		return "", "", false, nil
	}
	if !dbpkg.IDPattern.MatchString(collectionID) || !dbpkg.IDPattern.MatchString(sessionID) {
		return "", "", true, errors.New("invalid explicit collection ownership")
	}
	if validateErr := database.ValidateCollectionInterval(collectionID, []string{"run"}); validateErr != nil {
		return "", "", true, validateErr
	}
	return collectionID, sessionID, true, nil
}

type collectorOpenInput struct {
	EpochID    string `json:"epoch_id"`
	SessionID  string `json:"ktx_session_id,omitempty"`
	IntervalID string `json:"interval_id,omitempty"`
}

type runtimeEventRecordInput struct {
	EpochID      string          `json:"epoch_id"`
	EpochOrdinal uint64          `json:"epoch_ordinal"`
	AfterOrdinal *uint64         `json:"after_ordinal"`
	SessionID    string          `json:"ktx_session_id,omitempty"`
	IntervalID   string          `json:"interval_id,omitempty"`
	Event        json.RawMessage `json:"event"`
}

type runtimeEventAck struct {
	OK                      bool    `json:"ok"`
	SessionID               string  `json:"session_id"`
	IntervalID              string  `json:"interval_id"`
	EpochID                 string  `json:"epoch_id"`
	EpochOrdinal            uint64  `json:"epoch_ordinal"`
	Ordinal                 uint64  `json:"ordinal"`
	Kind                    string  `json:"kind"`
	Contract                string  `json:"contract"`
	ModelStepClassification *string `json:"model_step_classification,omitempty"`
}

func requiredCollectorEnvironment(db *dbpkg.DB) (benchcontract.Environment, error) {
	env, present, err := benchcontract.ReadEnvironment()
	if err != nil {
		return env, err
	}
	if !present {
		return env, errors.New("required benchmark collector environment is absent")
	}
	interval, err := db.LoadRuntimeEventInterval(env.IntervalID)
	if err != nil {
		return env, err
	}
	if interval.SessionID != env.SessionID || interval.Runtime != benchcontract.Runtime || interval.RuntimeVersion != benchcontract.RuntimeVersion || interval.Source != telemetry.SourceEventHook || interval.ContractVersion != env.Contract || interval.CloseState != dbpkg.RuntimeOpen || interval.ContradictionCount != 0 {
		return env, errors.New("benchmark collector owner or interval contract does not match the database")
	}
	return env, nil
}

func decodeSingleJSON(stdin io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(stdin, 8<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("input must contain one JSON value")
	}
	return nil
}

func recordCollectorOpen(args []string, stdin io.Reader, db *dbpkg.DB, stdout io.Writer) error {
	if len(args) != 0 {
		return usageError("ktx telemetry collector-open: unexpected argument %q", args[0])
	}
	env, err := requiredCollectorEnvironment(db)
	if err != nil {
		return usageError("ktx telemetry collector-open: %v", err)
	}
	var input collectorOpenInput
	if err := decodeSingleJSON(stdin, &input); err != nil {
		return usageError("ktx telemetry collector-open: parse JSON: %v", err)
	}
	if input.EpochID == "" || input.SessionID != "" && input.SessionID != env.SessionID || input.IntervalID != "" && input.IntervalID != env.IntervalID {
		return usageError("ktx telemetry collector-open: owner, interval, or epoch conflict")
	}
	event, _, err := db.PersistRuntimeEvent(dbpkg.RuntimeEventInput{IntervalID: env.IntervalID, SessionID: env.SessionID, EpochID: input.EpochID, Kind: "collector_open", EventType: "collector.open", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return internalError("ktx telemetry collector-open: %v", err)
	}
	return json.NewEncoder(stdout).Encode(runtimeEventAck{OK: true, SessionID: env.SessionID, IntervalID: env.IntervalID, EpochID: input.EpochID, Ordinal: event.Ordinal, Kind: event.Kind, Contract: env.Contract})
}

func recordRuntimeEvent(args []string, stdin io.Reader, db *dbpkg.DB, stdout io.Writer) error {
	if len(args) != 0 {
		return usageError("ktx telemetry event-record: unexpected argument %q", args[0])
	}
	env, err := requiredCollectorEnvironment(db)
	if err != nil {
		return usageError("ktx telemetry event-record: %v", err)
	}
	var input runtimeEventRecordInput
	if err := decodeSingleJSON(stdin, &input); err != nil {
		return usageError("ktx telemetry event-record: parse JSON: %v", err)
	}
	if input.EpochID == "" || input.EpochOrdinal == 0 || input.AfterOrdinal == nil || len(input.Event) == 0 || input.SessionID != "" && input.SessionID != env.SessionID || input.IntervalID != "" && input.IntervalID != env.IntervalID {
		return usageError("ktx telemetry event-record: owner, interval, epoch, or event conflict")
	}
	normalized, observation, err := normalizeRuntimeEvent(input.Event)
	if err != nil {
		return usageError("ktx telemetry event-record: %v", err)
	}
	normalized.IntervalID, normalized.SessionID, normalized.EpochID, normalized.EpochOrdinal = env.IntervalID, env.SessionID, input.EpochID, input.EpochOrdinal
	normalized.AfterOrdinal = input.AfterOrdinal
	normalized.CapturedAt, normalized.Telemetry = time.Now().UTC().Format(time.RFC3339Nano), observation
	event, _, err := db.PersistRuntimeEvent(normalized)
	if err != nil {
		return internalError("ktx telemetry event-record: %v", err)
	}
	if event.ModelStepClassification != nil && *event.ModelStepClassification == dbpkg.ModelStepConflictingDuplicate {
		return internalError("ktx telemetry event-record: conflicting duplicate model step retained at callback ordinal %d", event.Ordinal)
	}
	return json.NewEncoder(stdout).Encode(runtimeEventAck{OK: true, SessionID: env.SessionID, IntervalID: env.IntervalID, EpochID: input.EpochID, EpochOrdinal: input.EpochOrdinal, Ordinal: event.Ordinal, Kind: event.Kind, Contract: env.Contract, ModelStepClassification: event.ModelStepClassification})
}

func normalizeRuntimeEvent(raw json.RawMessage) (dbpkg.RuntimeEventInput, *telemetry.Observation, error) {
	var event struct {
		Type       string `json:"type"`
		Properties struct {
			SessionID string `json:"sessionID"`
			Info      struct {
				SessionID string `json:"sessionID"`
			} `json:"info"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
			Part struct {
				Type      string `json:"type"`
				SessionID string `json:"sessionID"`
				MessageID string `json:"messageID"`
				ID        string `json:"id"`
			} `json:"part"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &event); err != nil || strings.TrimSpace(event.Type) == "" {
		return dbpkg.RuntimeEventInput{}, nil, errors.New("event must be an object with a non-blank type")
	}
	sessionID := event.Properties.Part.SessionID
	if sessionID == "" {
		sessionID = event.Properties.SessionID
	}
	if sessionID == "" {
		sessionID = event.Properties.Info.SessionID
	}
	input := dbpkg.RuntimeEventInput{Kind: "other", EventType: event.Type}
	if sessionID != "" {
		input.SourceSessionID = &sessionID
	}
	partType := event.Properties.Part.Type
	switch {
	case event.Type == "message.part.updated" && partType == "step-finish":
		observation, err := opencode.DecodeModelStep(raw)
		if err != nil {
			return input, nil, err
		}
		observation.Ordinal, observation.Source = 1, telemetry.SourceEventHook
		input.Kind, input.SourceSessionID, input.MessageID, input.PartID = "model_step", &observation.SessionID, &observation.MessageID, &observation.PartID
		return input, &observation, nil
	case event.Type == "message.part.updated" && partType == "retry", event.Type == "session.status" && event.Properties.Status.Type == "retry":
		input.Kind = "retry"
	case event.Type == "message.part.updated" && partType == "compaction", event.Type == "session.compacted":
		input.Kind = "compaction"
	case event.Type == "session.error", event.Type == "server.instance.disposed":
		input.Kind = "interruption"
	}
	return input, nil, nil
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
	if err := telemetry.ValidateObservation(observation); err != nil {
		detail := strings.TrimPrefix(err.Error(), telemetry.ErrInvalid.Error())
		return usageError("ktx telemetry record: %s%s", opencode.ErrInvalid, detail)
	}
	capturedAt := time.Now().UTC().Format(time.RFC3339Nano)
	sessionHint := input.KtxSessionID
	benchmarkEnv, required, envErr := benchcontract.ReadEnvironment()
	if required {
		if envErr != nil {
			return usageError("ktx telemetry record: %v", envErr)
		}
		if sessionHint != "" && sessionHint != benchmarkEnv.SessionID {
			return usageError("ktx telemetry record: payload session conflicts with benchmark attempt owner")
		}
		sessionHint = benchmarkEnv.SessionID
	}
	started := time.Now()
	sessionID, err := db.ResolveSessionID(sessionHint, capturedAt)
	if err == nil {
		_, err = db.PersistSessionTelemetry(sessionID, capturedAt, observation)
	}
	err = dbpkg.AttributeDatabaseUnavailable(dbpkg.OperationSource, started, err)
	if err != nil {
		if required {
			return internalError("ktx telemetry record: required persistence failed: %v", err)
		}
		// Session telemetry is optional. Keep the failure visible to the operator,
		// but do not turn this side channel into a failed agent operation.
		path, pathErr := dbpkg.ResolvePath("")
		if classified := classifyDatabaseError(path, "telemetry record", err, dbpkg.OperationSource, started); pathErr == nil && classified != nil {
			printError(stderr, classified, FormatJSON)
		} else {
			fmt.Fprintf(stderr, "ktx: telemetry not recorded: %v\n", err)
		}
	}
	return nil
}

func (input telemetryRecordInput) observation() (*telemetry.Observation, error) {
	if input.Ordinal == nil {
		return nil, fmt.Errorf("missing required field: ordinal")
	}
	return &telemetry.Observation{
		Key: telemetry.Key{
			SessionID: input.SessionID,
			MessageID: input.MessageID,
			PartID:    input.PartID,
		},
		Ordinal: *input.Ordinal,
		Reason:  input.Reason,
		Cost:    decodeRecordCost(input.Cost),
		Tokens: telemetry.Tokens{
			Input:      decodeRecordInteger(input.Tokens.Input),
			Output:     decodeRecordInteger(input.Tokens.Output),
			Reasoning:  decodeRecordInteger(input.Tokens.Reasoning),
			CacheRead:  decodeRecordInteger(input.Tokens.CacheRead),
			CacheWrite: decodeRecordInteger(input.Tokens.CacheWrite),
		},
		Provider:       input.Provider,
		Model:          input.Model,
		Runtime:        opencode.Runtime,
		RuntimeVersion: opencode.RuntimeVersion,
		Source:         telemetry.SourceEventHook,
	}, nil
}

func decodeRecordInteger(raw json.RawMessage) telemetry.IntegerMeasurement {
	var measurement telemetry.IntegerMeasurement
	if len(raw) > 0 && json.Unmarshal(raw, &measurement) == nil && measurement.State != "" {
		return measurement
	}
	return telemetry.DecodeIntegerMeasurement(raw, telemetry.MeasurementMeasured)
}

func decodeRecordCost(raw json.RawMessage) telemetry.CostMeasurement {
	var measurement telemetry.CostMeasurement
	if len(raw) > 0 && json.Unmarshal(raw, &measurement) == nil && measurement.State != "" {
		return measurement
	}
	return telemetry.DecodeCostMeasurement(raw, telemetry.MeasurementMeasured)
}

func listTelemetry(args []string, format string, db *dbpkg.DB, stdout io.Writer) (err error) {
	started := time.Now()
	defer func() { err = dbpkg.AttributeDatabaseUnavailable(dbpkg.OperationQuery, started, err) }()
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
	fmt.Fprintln(tw, "ORDINAL\tSOURCE SESSION\tMESSAGE\tPART\tCACHE READ\tCACHE WRITE\tPROVIDER\tMODEL\tRUNTIME\tRUNTIME VERSION\tSOURCE")
	for _, observation := range observations {
		provider, model := "-", "-"
		if observation.Provider != nil {
			provider = *observation.Provider
		}
		if observation.Model != nil {
			model = *observation.Model
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			observation.Ordinal, observation.SourceSessionID, observation.MessageID, observation.PartID,
			measurementText(observation.CacheRead), measurementText(observation.CacheWrite), provider, model,
			observation.Runtime, observation.RuntimeVersion, observation.Source)
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
	if len(ctx.Args) == 0 || (ctx.Args[0] != "record" && ctx.Args[0] != "list" && ctx.Args[0] != "collector-open" && ctx.Args[0] != "event-record" && ctx.Args[0] != "cue-delivered") {
		return telemetryLogic(ctx.Args, ctx.Format, ctx.Stdin, nil, ctx.Stdout, ctx.Stderr)
	}
	target, err := dbpkg.ResolveTarget("")
	if err != nil {
		return telemetryDatabaseFailure(ctx, fmt.Errorf("resolve database path: %w", err))
	}
	var db *dbpkg.DB
	if ctx.Args[0] == "list" {
		db, err = openQueryDatabase(target, "telemetry list")
	} else {
		db, err = openDatabase(target, "telemetry "+ctx.Args[0])
	}
	if err != nil {
		if _, ok := err.(*Error); ok {
			return nil, err
		}
		return telemetryDatabaseFailure(ctx, fmt.Errorf("open database: %w", err))
	}
	defer db.Close()
	result, err := telemetryLogic(ctx.Args, ctx.Format, ctx.Stdin, db, ctx.Stdout, ctx.Stderr)
	if err != nil {
		phase := dbpkg.OperationSource
		if ctx.Args[0] == "list" {
			phase = dbpkg.OperationQuery
		}
		if classified := classifyDatabaseError(target.Path, "telemetry "+ctx.Args[0], err, phase, time.Now()); classified != nil {
			err = classified
		}
	}
	return result, err
}

func telemetryDatabaseFailure(ctx *DispatchContext, err error) (any, error) {
	if len(ctx.Args) > 0 && ctx.Args[0] == "record" {
		if _, present, envErr := benchcontract.ReadEnvironment(); present {
			if envErr != nil {
				return nil, usageError("ktx telemetry: %v", envErr)
			}
			return nil, internalError("ktx telemetry: required collector database unavailable: %v", err)
		}
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
	fmt.Fprintln(w, "cue-delivered is an adapter-only acknowledgement used by explicit collection.")
}
