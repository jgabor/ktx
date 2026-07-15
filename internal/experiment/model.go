package experiment

const (
	Baseline  = "baseline"
	Candidate = "candidate"
)

var Sides = []string{Baseline, Candidate}

var AttemptStatuses = []string{"completed", "timeout", "failed", "unlinked", "invalid"}

const scoreEpsilon = 1.0e-9

type Task struct {
	ID               string   `json:"id" toml:"id"`
	Prompt           string   `json:"prompt" toml:"prompt"`
	ExpectedOutput   *string  `json:"expected_output"`
	ExpectedFacts    []string `json:"expected_facts"`
	ScoringKind      string   `json:"scoring_kind"`
	MinimumToolCalls int      `json:"minimum_tool_calls" toml:"minimum_tool_calls"`
	Skill            string   `json:"skill,omitempty" toml:"skill"`
	TokenBand        []int    `json:"token_band,omitempty" toml:"token_band"`
	CorpusFiles      []string `json:"corpus_files" toml:"corpus_files"`
	WorkspaceMode    string   `json:"workspace_mode" toml:"workspace_mode"`
	QualityRisks     []string `json:"quality_risks,omitempty" toml:"quality_risks"`
	TaskFile         string   `json:"task_file,omitempty"`
}

type Score struct {
	Score                 float64  `json:"score"`
	FullPass              bool     `json:"full_pass"`
	AnswerMatchesExpected bool     `json:"answer_matches_expected"`
	FactsMatched          []string `json:"facts_matched"`
	FactsMissed           []string `json:"facts_missed"`
	FactsTotal            int      `json:"facts_total"`
}

type UsageEvidence struct {
	StepFinishEvents int `json:"step_finish_events"`
	TokensInEvents   int `json:"tokens_in_events"`
	TokensOutEvents  int `json:"tokens_out_events"`
	CostEvents       int `json:"cost_events"`
	CacheReadEvents  int `json:"cache_read_events"`
	CacheWriteEvents int `json:"cache_write_events"`
}

type Metrics struct {
	StreamComplete bool `json:"stream_complete"`
	Score
	ToolCallCount int           `json:"tool_call_count"`
	TokensIn      *float64      `json:"tokens_in"`
	TokensOut     *float64      `json:"tokens_out"`
	Cost          *float64      `json:"cost"`
	CacheRead     *float64      `json:"cache_read"`
	CacheWrite    *float64      `json:"cache_write"`
	AnswerExcerpt string        `json:"answer_excerpt"`
	UsageEvidence UsageEvidence `json:"usage_evidence"`
}

type SessionLink struct {
	State           string   `json:"state"`
	NewSessionCount int      `json:"new_session_count"`
	NewSessionIDs   []string `json:"new_session_ids"`
}

type RecordedStreamLink struct {
	ID         string  `json:"id"`
	BenchRunID *string `json:"bench_run_id"`
}

type Attempt struct {
	SchemaVersion    int                  `json:"schema_version"`
	ExperimentID     string               `json:"experiment_id"`
	AttemptID        string               `json:"attempt_id"`
	Side             string               `json:"side"`
	PipelineID       string               `json:"pipeline_id"`
	Cycle            int                  `json:"cycle"`
	Ordinal          int                  `json:"ordinal"`
	StartedAt        string               `json:"started_at"`
	EndedAt          string               `json:"ended_at"`
	Status           string               `json:"status"`
	ProcessExitCode  int                  `json:"process_exit_code"`
	SessionLink      SessionLink          `json:"session_link"`
	SessionID        *string              `json:"session_id"`
	RecordedStreams  []RecordedStreamLink `json:"recorded_streams"`
	RecordedStreamID *string              `json:"recorded_stream_id"`
	BenchRunID       *string              `json:"bench_run_id"`
	Metrics
	ConfigSHA256  string            `json:"config_sha256"`
	Artifacts     map[string]string `json:"artifacts"`
	ContentHashes map[string]string `json:"content_hashes"`
}

type PipelineMetadata struct {
	Path       string   `json:"path"`
	SourcePath string   `json:"source_path"`
	PipelineID string   `json:"pipeline_id"`
	SHA256     string   `json:"sha256"`
	Stages     []string `json:"stages"`
}

type MethodologyMetadata struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
}

type SnapshotMetadata struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	FileCount int    `json:"file_count"`
}

type RepositoryMetadata struct {
	Commit       *string          `json:"commit"`
	Dirty        *bool            `json:"dirty"`
	StatusSHA256 *string          `json:"status_sha256"`
	Snapshot     SnapshotMetadata `json:"snapshot"`
}

type Manifest struct {
	SchemaVersion           int                         `json:"schema_version"`
	ExperimentID            string                      `json:"experiment_id"`
	CreatedAt               string                      `json:"created_at"`
	Task                    Task                        `json:"task"`
	Model                   string                      `json:"model"`
	Variant                 *string                     `json:"variant,omitempty"`
	TargetValidPerPipeline  int                         `json:"target_valid_per_pipeline"`
	MaxAttempts             int                         `json:"max_attempts"`
	AttemptTimeout          string                      `json:"attempt_timeout"`
	TerminationGrace        string                      `json:"termination_grace"`
	PipelineExecutionPolicy map[string]any              `json:"pipeline_execution_policy"`
	Repository              RepositoryMetadata          `json:"repository"`
	Image                   map[string]string           `json:"image"`
	Identities              map[string]string           `json:"identities"`
	Pipelines               map[string]PipelineMetadata `json:"pipelines"`
	Methodology             MethodologyMetadata         `json:"methodology"`
}
