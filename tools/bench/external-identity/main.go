package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"unicode/utf8"
)

const (
	lockByteLimit     = 64 * 1024
	observedByteLimit = 32 * 1024
	maxJSONNesting    = 64
	lockDisplay       = "tools/bench/external-identity/lock.json"
)

var checkedSections = []string{
	"campaign",
	"evaluator",
	"task",
	"runtime",
	"adapter",
	"ktx",
	"execution",
	"digests",
}

var lockKeys = []string{
	"lock_version",
	"campaign",
	"evaluator",
	"task",
	"runtime",
	"adapter",
	"ktx",
	"execution",
	"digests",
	"claims",
}

var corrections = map[string]string{
	"campaign":  "Compare the campaign ID and provenance with the tracked lock.",
	"evaluator": "Compare the evaluator version, source commit, and binary digest with the tracked lock.",
	"task":      "Compare the task ID, external provenance, and task digests with the tracked lock.",
	"runtime":   "Compare the runtime name, version, release asset, and release digest with the tracked lock.",
	"adapter":   "Compare the adapter registry, protocol, and file digests with the tracked lock.",
	"ktx":       "Compare the retained KTX source, worktree, binary, version, and capability identities with the tracked lock.",
	"execution": "Compare the fixed model, variant, run settings, and configuration digests with the tracked lock.",
	"digests":   "Compare the retained archive, evidence, and current-metrics digests with the tracked lock.",
}

//go:embed lock.json
var trackedLock []byte

type dataError string

func (e dataError) Error() string { return string(e) }

type openFileFunc func(string) (*os.File, error)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, trackedLock))
}

func run(arguments []string, stdout, stderr io.Writer, lockData []byte) int {
	if len(arguments) == 1 && (arguments[0] == "help" || arguments[0] == "-h" || arguments[0] == "--help") {
		fmt.Fprintln(stdout, "usage: external-identity --observed FILE")
		return 0
	}
	if len(arguments) != 2 || arguments[0] != "--observed" {
		fmt.Fprintln(stderr, "external identity lock: usage: external-identity --observed FILE")
		return 64
	}

	lock, err := loadLock(lockData)
	if err != nil {
		fmt.Fprintf(stderr, "external identity lock: invalid tracked lock: %v\n", err)
		return 70
	}
	required := make(map[string]any, len(checkedSections))
	for _, section := range checkedSections {
		required[section] = lock[section]
	}

	observedData, err := readRegularFile(arguments[1], observedByteLimit)
	if err != nil {
		fmt.Fprintf(stderr, "external identity lock: observed identity unavailable or malformed: %v\n", err)
		return 65
	}
	observed, err := parseJSONObject(observedData, maxJSONNesting)
	if err != nil {
		fmt.Fprintf(stderr, "external identity lock: observed identity unavailable or malformed: %v\n", err)
		return 65
	}

	if problem := shapeError(required, observed, ""); problem != "" {
		fmt.Fprintf(
			stderr,
			"external identity lock: malformed observed identity: %s. Supply the exact fields listed in %s.\n",
			problem,
			lockDisplay,
		)
		return 65
	}

	expectedLeaves := collectLeaves(required, "")
	observedLeaves := collectLeaves(observed, "")
	observedByPath := make(map[string]any, len(observedLeaves))
	for _, leaf := range observedLeaves {
		observedByPath[leaf.path] = leaf.value
	}

	mismatched := false
	for _, expected := range expectedLeaves {
		if reflect.DeepEqual(expected.value, observedByPath[expected.path]) {
			continue
		}
		mismatched = true
		section := expected.path
		for index, character := range section {
			if character == '.' || character == '[' {
				section = section[:index]
				break
			}
		}
		rendered, marshalErr := json.Marshal(expected.value)
		if marshalErr != nil {
			fmt.Fprintln(stderr, "external identity lock: invalid tracked lock: value cannot be rendered")
			return 70
		}
		fmt.Fprintf(
			stderr,
			"external identity lock: mismatch at %s; expected %s. %s\n",
			expected.path,
			rendered,
			corrections[section],
		)
	}
	if mismatched {
		return 2
	}

	fmt.Fprintln(stdout, "external identity lock: match")
	return 0
}

func loadLock(data []byte) (map[string]any, error) {
	if len(data) > lockByteLimit {
		return nil, dataError(fmt.Sprintf("file exceeds %d bytes", lockByteLimit))
	}
	lock, err := parseJSONObject(data, maxJSONNesting)
	if err != nil {
		return nil, err
	}
	if len(lock) != len(lockKeys) {
		return nil, dataError("unexpected direct lock shape or version")
	}
	for _, key := range lockKeys {
		if _, ok := lock[key]; !ok {
			return nil, dataError("unexpected direct lock shape or version")
		}
	}
	version, ok := lock["lock_version"].(json.Number)
	if !ok || version.String() != "1" {
		return nil, dataError("unexpected direct lock shape or version")
	}
	for _, section := range append(append([]string(nil), checkedSections...), "claims") {
		if _, ok := lock[section].(map[string]any); !ok {
			return nil, dataError("unexpected direct lock shape or version")
		}
	}
	return lock, nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	return readRegularFileWith(path, limit, os.Open)
}

func readRegularFileWith(path string, limit int64, open openFileFunc) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, dataError("file is unavailable")
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, dataError("file is not a regular non-symlink file")
	}

	source, err := open(path)
	if err != nil || source == nil {
		return nil, dataError("file could not be opened for reading")
	}
	after, statErr := source.Stat()
	if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = source.Close()
		return nil, dataError("file changed or could not be inspected after opening")
	}

	data, readErr := readBounded(source, limit)
	closeErr := source.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, dataError("file could not be read")
	}
	return data, nil
}

func readBounded(source io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(source, limit+1))
	if err != nil {
		return nil, dataError("file could not be read")
	}
	if int64(len(data)) > limit {
		return nil, dataError(fmt.Sprintf("file exceeds %d bytes", limit))
	}
	return data, nil
}

func parseJSONObject(data []byte, maxNesting int) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, dataError("file contains invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder, 0, maxNesting)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, dataError("file contains trailing JSON data")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, dataError("file is not one JSON object")
	}
	return object, nil
}

func decodeJSONValue(decoder *json.Decoder, depth, maxNesting int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, dataError("file is not valid JSON")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		switch token.(type) {
		case nil, bool, string, json.Number:
			return token, nil
		default:
			return nil, dataError("file is not valid JSON")
		}
	}
	if depth >= maxNesting {
		return nil, dataError(fmt.Sprintf("JSON nesting exceeds %d containers", maxNesting))
	}
	switch delimiter {
	case '{':
		return decodeJSONObject(decoder, depth+1, maxNesting)
	case '[':
		return decodeJSONArray(decoder, depth+1, maxNesting)
	default:
		return nil, dataError("file is not valid JSON")
	}
}

func decodeJSONObject(decoder *json.Decoder, depth, maxNesting int) (map[string]any, error) {
	object := make(map[string]any)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, dataError("file is not valid JSON")
		}
		key, ok := token.(string)
		if !ok {
			return nil, dataError("file is not valid JSON")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, dataError("duplicate JSON member")
		}
		value, err := decodeJSONValue(decoder, depth, maxNesting)
		if err != nil {
			return nil, err
		}
		object[key] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, dataError("file is not valid JSON")
	}
	return object, nil
}

func decodeJSONArray(decoder *json.Decoder, depth, maxNesting int) ([]any, error) {
	var array []any
	for decoder.More() {
		value, err := decodeJSONValue(decoder, depth, maxNesting)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return nil, dataError("file is not valid JSON")
	}
	return array, nil
}

func shapeError(required, observed any, path string) string {
	switch expected := required.(type) {
	case map[string]any:
		actual, ok := observed.(map[string]any)
		if !ok {
			return fmt.Sprintf("wrong type at %s", displayPath(path))
		}
		missing := difference(sortedKeys(expected), actual)
		if len(missing) > 0 {
			return "missing " + joinPath(path, missing[0])
		}
		unexpected := difference(sortedKeys(actual), expected)
		if len(unexpected) > 0 {
			return "unexpected " + joinPath(path, unexpected[0])
		}
		for _, key := range sortedKeys(expected) {
			if problem := shapeError(expected[key], actual[key], joinPath(path, key)); problem != "" {
				return problem
			}
		}
		return ""
	case []any:
		actual, ok := observed.([]any)
		if !ok {
			return fmt.Sprintf("wrong type at %s", displayPath(path))
		}
		if len(expected) != len(actual) {
			return fmt.Sprintf("wrong length at %s", displayPath(path))
		}
		for index := range expected {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if problem := shapeError(expected[index], actual[index], childPath); problem != "" {
				return problem
			}
		}
		return ""
	case nil:
		if observed != nil {
			return fmt.Sprintf("wrong type at %s", displayPath(path))
		}
		return ""
	case string:
		if _, ok := observed.(string); !ok {
			return fmt.Sprintf("wrong type at %s", displayPath(path))
		}
	case bool:
		if _, ok := observed.(bool); !ok {
			return fmt.Sprintf("wrong type at %s", displayPath(path))
		}
	case json.Number:
		if _, ok := observed.(json.Number); !ok {
			return fmt.Sprintf("wrong type at %s", displayPath(path))
		}
	default:
		return fmt.Sprintf("wrong type at %s", displayPath(path))
	}
	return ""
}

func displayPath(path string) string {
	if path == "" {
		return "root"
	}
	return path
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func difference(keys []string, other map[string]any) []string {
	var result []string
	for _, key := range keys {
		if _, ok := other[key]; !ok {
			result = append(result, key)
		}
	}
	return result
}

type leafValue struct {
	path  string
	value any
}

func collectLeaves(value any, path string) []leafValue {
	var leaves []leafValue
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(typed) {
			leaves = append(leaves, collectLeaves(typed[key], joinPath(path, key))...)
		}
	case []any:
		for index, member := range typed {
			leaves = append(leaves, collectLeaves(member, fmt.Sprintf("%s[%d]", path, index))...)
		}
	default:
		leaves = append(leaves, leafValue{path: path, value: value})
	}
	return leaves
}
