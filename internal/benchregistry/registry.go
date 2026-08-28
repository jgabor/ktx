package benchregistry

import (
	"fmt"
	"path/filepath"
	"regexp"
)

type Kind string

const (
	Spec     Kind = "spec"
	Task     Kind = "task"
	Pipeline Kind = "pipeline"
	Contract Kind = "contract"
)

type Entry struct {
	Kind   Kind
	ID     string
	Format string
	Path   string
}

var (
	semanticID    = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	versionSuffix = regexp.MustCompile(`-v[0-9]+$`)
)

var entries = []Entry{
	{Spec, "eval-matrix", "yaml", "tools/bench/specs/eval-matrix.yaml"},
	{Spec, "cache-aware", "yaml", "tools/bench/specs/cache-aware.yaml"},
	{Spec, "prompt-cache-token-hit-rate", "yaml", "tools/bench/specs/prompt-cache-token-hit-rate.yaml"},
	{Spec, "recovery-aware-comparison", "yaml", "tools/bench/specs/recovery-aware-comparison.yaml"},
	{Task, "crossfile-nav", "toml", "tools/bench/tasks/crossfile-nav.toml"},
	{Task, "ephemeral-midfact", "toml", "tools/bench/tasks/ephemeral-midfact.toml"},
	{Task, "fact-midfile", "toml", "tools/bench/tasks/fact-midfile.toml"},
	{Task, "structured-diff", "toml", "tools/bench/tasks/structured-diff.toml"},
	{Task, "log-tail-error", "toml", "tools/bench/tasks/log-tail-error.toml"},
	{Task, "grep-noise", "toml", "tools/bench/tasks/grep-noise.toml"},
	{Task, "failure-passthrough", "toml", "tools/bench/tasks/failure-passthrough.toml"},
	{Pipeline, "aggressive-keep-ends", "toml", "tools/bench/pipelines/aggressive-keep-ends.toml"},
	{Pipeline, "aggressive-head", "toml", "tools/bench/pipelines/aggressive-head.toml"},
	{Pipeline, "conservative-a-recover", "toml", "tools/bench/pipelines/conservative-a-recover.toml"},
	{Pipeline, "conservative-a", "toml", "tools/bench/pipelines/conservative-a.toml"},
	{Pipeline, "keep-ends-mid", "toml", "tools/bench/pipelines/keep-ends-mid.toml"},
	{Pipeline, "sanitize", "toml", "tools/bench/pipelines/sanitize.toml"},
	{Pipeline, "passthrough", "toml", "tools/bench/pipelines/passthrough.toml"},
	{Contract, "cache-reuse", "json", "tools/bench/contracts/cache-reuse.json"},
}

type Registry struct {
	byKey   map[string]Entry
	byPath  map[string]Entry
	entries []Entry
}

func Active() Registry {
	registry, err := New(entries)
	if err != nil {
		panic(err)
	}
	return registry
}

func New(source []Entry) (Registry, error) {
	r := Registry{byKey: make(map[string]Entry), byPath: make(map[string]Entry), entries: append([]Entry(nil), source...)}
	for _, entry := range source {
		if !semanticID.MatchString(entry.ID) || versionSuffix.MatchString(entry.ID) {
			return Registry{}, fmt.Errorf("invalid %s ID %q: use a versionless semantic ID", entry.Kind, entry.ID)
		}
		key := string(entry.Kind) + "\x00" + entry.ID
		if _, exists := r.byKey[key]; exists {
			return Registry{}, fmt.Errorf("duplicate %s ID %q", entry.Kind, entry.ID)
		}
		clean := filepath.Clean(filepath.FromSlash(entry.Path))
		if _, exists := r.byPath[clean]; exists {
			return Registry{}, fmt.Errorf("duplicate registry path %q", entry.Path)
		}
		r.byKey[key], r.byPath[clean] = entry, entry
	}
	return r, nil
}

func (r Registry) Lookup(kind Kind, id string) (Entry, error) {
	if !semanticID.MatchString(id) || versionSuffix.MatchString(id) {
		return Entry{}, fmt.Errorf("invalid %s ID %q: paths and versions are not accepted", kind, id)
	}
	entry, ok := r.byKey[string(kind)+"\x00"+id]
	if !ok {
		return Entry{}, fmt.Errorf("unknown %s ID %q", kind, id)
	}
	return entry, nil
}

func (r Registry) LookupPath(root string, kind Kind, path string) (Entry, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Entry{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Entry{}, err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return Entry{}, err
	}
	entry, ok := r.byPath[filepath.Clean(relative)]
	if !ok || entry.Kind != kind {
		return Entry{}, fmt.Errorf("unregistered %s input %q", kind, path)
	}
	return entry, nil
}

func (r Registry) Entries(kind Kind) []Entry {
	result := make([]Entry, 0)
	for _, entry := range r.entries {
		if entry.Kind == kind {
			result = append(result, entry)
		}
	}
	return result
}
