package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDatabaseSafetyGuidanceContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	contract := readGuidanceFile(t, filepath.Join(root, "docs", "database-safety.md"))

	for _, required := range []string{
		"DB-free",
		"Genuinely read-only",
		"Creation and existing-upgrade capable",
		"Forbidden ambient-live development access",
		"removes `KTX_DB`",
		"HOME=",
		"XDG_DATA_HOME=",
		"XDG_CONFIG_HOME=",
		"XDG_CACHE_HOME=",
		"XDG_STATE_HOME=",
		"GOCACHE",
		"GOMODCACHE",
		"BUN_INSTALL_CACHE_DIR",
		"GOLANGCI_LINT_CACHE",
		"verification-only database access audit",
		"forbidden=0",
		"seeded temporary database",
		"Do not install yet",
		"effective installed binary",
		"Do not write the database",
		"Do not lower `PRAGMA user_version`",
		"doctor",
		"never creates or migrates",
		"--expect-runtime-sha256",
		"runtime-proof-required",
		"runtime-proof-mismatch",
		"operator ownership declarations",
		"not a safety oracle",
		"writes no receipt or trust state",
		"They do not perform the live open",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("database safety contract missing %q", required)
		}
	}
	for _, target := range []string{"`mage test`", "`mage check`", "`mage verify`"} {
		if !strings.Contains(contract, target) {
			t.Errorf("database safety contract missing self-hermetic target %q", target)
		}
	}

	for _, path := range []string{"AGENTS.md", "README.md", filepath.Join(".github", "workflows", "ci.yml")} {
		content := readGuidanceFile(t, filepath.Join(root, path))
		if !strings.Contains(content, "docs/database-safety.md") {
			t.Errorf("%s does not point to the canonical database safety contract", path)
		}
	}

	routing := readGuidanceFile(t, filepath.Join(root, "docs", "routing-candidate-characterization.md"))
	for _, forbidden := range []string{
		`build/ktx db path`,
		"sqlite3 -readonly \"$(",
		"\ngo test ./internal/router",
		"\nmage check",
	} {
		if strings.Contains(routing, forbidden) {
			t.Errorf("routing characterization retains unsafe active procedure %q", forbidden)
		}
	}
	for _, required := range []string{
		"non-live",
		"mktemp -d",
		"sqlite3 -readonly \"$root/routing-characterization.db\"",
		"ktx_verify go test ./internal/router",
		"ktx_verify mage check",
	} {
		if !strings.Contains(routing, required) {
			t.Errorf("routing characterization missing safe procedure %q", required)
		}
	}
	if !regexp.MustCompile(`sanitized\s+(?:non-live\s+)?snapshot`).MatchString(routing) {
		t.Error("routing characterization does not require a sanitized snapshot")
	}

	ambientSQLite := regexp.MustCompile(`(?s)sqlite3\s+-readonly\s+"\$\([^\n]*\bdb\s+path\b`)
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatalf("list active documentation: %v", err)
	}
	for _, path := range docs {
		if ambientSQLite.MatchString(readGuidanceFile(t, path)) {
			t.Errorf("%s derives a read-only SQLite target from ambient db path resolution", path)
		}
	}
}

func TestDBDispatchCommentClassifiesOwnedOpen(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	dbSource := readGuidanceFile(t, filepath.Join(filepath.Dir(source), "db.go"))
	match := regexp.MustCompile(`(?s)(// dbCommandFunc.*?)\nfunc dbCommandFunc`).FindStringSubmatch(dbSource)
	if match == nil {
		t.Fatal("dbCommandFunc documentation comment not found")
	}
	doc := regexp.MustCompile(`(?m)^// ?`).ReplaceAllString(match[1], "")
	doc = strings.Join(strings.Fields(doc), " ")

	for _, required := range []string{
		"NeedsDB=false skips only the dispatcher-managed database open",
		"`db path` and unavailable `db prune` are DB-free",
		"`db report` owns its genuinely read-only open",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("dbCommandFunc documentation missing classification %q", required)
		}
	}

	blanketDBFree := regexp.MustCompile(`(?i)\b(?:ktx db|(?:the|this|database|db) namespace)\b[^.]*\b(?:(?:is|remains)\s+(?:entirely\s+)?DB-free|(?:never|does not|doesn't|cannot|can't)[^.]*\b(?:database|DB))\b`)
	if contradiction := blanketDBFree.FindString(doc); contradiction != "" {
		t.Errorf("dbCommandFunc documentation misclassifies the namespace as DB-free: %q", contradiction)
	}
}

func TestBinaryCapabilitiesGuidanceVerifiesEffectiveInstallBeforeAmbientAccess(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	guidance := readGuidanceFile(t, filepath.Join(root, "docs", "binary-capabilities.md"))
	ordered := []string{
		"mage build",
		"mage verify",
		"Do not rebuild after that evidence is established",
		"KTX_VERIFIED_ARTIFACT=./build/ktx",
		`KTX_VERIFIED_SHA256=$(sha256sum "$KTX_VERIFIED_ARTIFACT"`,
		"mage install",
		"KTX_VERIFIED_BIN=$(command -v ktx)",
		`KTX_INSTALLED_SHA256=$(sha256sum "$KTX_VERIFIED_BIN"`,
		`test "$KTX_INSTALLED_SHA256" = "$KTX_VERIFIED_SHA256"`,
		`"$KTX_VERIFIED_BIN" binary-capabilities --expect-sha256 "$KTX_VERIFIED_SHA256"`,
		`"$KTX_VERIFIED_BIN" --expect-runtime-sha256 "$KTX_VERIFIED_SHA256" list`,
	}
	position := 0
	for _, expected := range ordered {
		index := strings.Index(guidance[position:], expected)
		if index < 0 {
			t.Fatalf("binary-capabilities guidance missing ordered install-verification step %q", expected)
		}
		position += index + len(expected)
	}
	compactGuidance := strings.Join(strings.Fields(guidance), " ")
	for _, required := range []string{
		"operator-only",
		"Development, tests, CI, and migration rehearsal must stop before the install",
		"checkout-derived digest",
		"digest merely reported by the candidate itself",
		"exact installed binary it invokes",
		"per invocation and creates no receipt or persistent trust state",
	} {
		if !strings.Contains(compactGuidance, required) {
			t.Errorf("binary-capabilities guidance missing %q", required)
		}
	}
}

func TestActiveShellExamplesOwnOrProveOpenBackedDatabase(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	paths, err := filepath.Glob(filepath.Join(root, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, docs...)

	for _, path := range paths {
		content := readGuidanceFile(t, path)
		inShell := false
		var block strings.Builder
		blockStart := 0
		for lineNumber, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				if !inShell {
					language := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
					inShell = language == "bash" || language == "sh" || language == "shell"
					if inShell {
						blockStart = lineNumber + 2
						block.Reset()
					}
					continue
				}
				for _, command := range unsafeDocumentedAmbientInvocations(block.String()) {
					t.Errorf("%s:%d ambient Open-backed example lacks prior same-binary verification and per-invocation proof or explicit KTX_DB ownership: %s", filepath.Base(path), blockStart+command.line-1, command.invocation)
				}
				inShell = false
				continue
			}
			if inShell {
				block.WriteString(line)
				block.WriteByte('\n')
			}
		}
	}
}

func TestOpenBackedHelpExamplesDeclareDatabaseAuthority(t *testing.T) {
	for verb := range dispatchRegistry {
		var stdout, stderr strings.Builder
		if code := runWith([]string{verb, "--help"}, &stdout, &stderr); code != ExitOK {
			t.Fatalf("%s help exit=%d stderr=%q", verb, code, stderr.String())
		}
		for _, sequence := range documentedHelpExampleSequences(stdout.String()) {
			for _, command := range unsafeDocumentedAmbientInvocations(sequence) {
				t.Errorf("%s help Open-backed example lacks prior same-binary verification and per-invocation proof or nonempty explicit KTX_DB ownership: %s", verb, command.invocation)
			}
		}
	}
}

type documentedValueState uint8

const (
	documentedValueUnknown documentedValueState = iota
	documentedValueEmpty
	documentedValueNonempty
)

type documentedVariableBinding struct {
	generation int
	state      documentedValueState
	exported   bool
}

type documentedIdentity struct {
	literal    string
	variable   string
	generation int
}

func (identity documentedIdentity) key() string {
	if identity.variable != "" {
		return "variable\x00" + identity.variable + "\x00" + strconv.Itoa(identity.generation)
	}
	if identity.literal != "" {
		return "literal\x00" + identity.literal
	}
	return ""
}

type documentedShellCommand struct {
	text               string
	line               int
	executable         string
	invocation         string
	executableIdentity documentedIdentity
	comparisonIdentity documentedIdentity
	runtimeIdentity    documentedIdentity
	proof              bool
	openBacked         bool
	localOwnerShadowed bool
	localOwner         bool
}

func documentedKTXInvocation(line string) (invocation string, proof, openBacked bool) {
	command, found := documentedKTXInvocationDetails(line)
	if !found {
		return "", false, false
	}
	return command.invocation, command.proof, command.openBacked
}

func documentedKTXInvocationDetails(script string) (documentedShellCommand, bool) {
	bindings := make(map[string]documentedVariableBinding)
	for _, shellCommand := range splitDocumentedShellCommands(script) {
		fields := documentedShellFields(shellCommand.text)
		if applyDocumentedPersistentState(fields, bindings) {
			continue
		}
		command, found := parseDocumentedKTXCommand(shellCommand, fields, bindings)
		if found {
			return command, true
		}
	}
	return documentedShellCommand{}, false
}

func parseDocumentedKTXCommand(shellCommand documentedShellCommand, fields []string, bindings map[string]documentedVariableBinding) (documentedShellCommand, bool) {
	if len(fields) == 0 {
		return documentedShellCommand{}, false
	}
	if fields[0] == "$" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return documentedShellCommand{}, false
	}

	position := 0
	for position < len(fields) && shellAssignmentName(fields[position]) != "" {
		if shellAssignmentName(fields[position]) == "KTX_DB" {
			shellCommand.localOwnerShadowed = true
			shellCommand.localOwner = documentedAssignmentState(fields[position], bindings) == documentedValueNonempty
		}
		position++
	}
	if position < len(fields) && fields[position] == "env" {
		position++
		for position < len(fields) {
			field := fields[position]
			if field == "--" {
				position++
				break
			}
			if field == "-u" || field == "--unset" {
				if position+1 >= len(fields) {
					return shellCommand, false
				}
				if fields[position+1] == "KTX_DB" {
					shellCommand.localOwnerShadowed = true
					shellCommand.localOwner = false
				}
				position += 2
				continue
			}
			if strings.HasPrefix(field, "--unset=") {
				if strings.TrimPrefix(field, "--unset=") == "KTX_DB" {
					shellCommand.localOwnerShadowed = true
					shellCommand.localOwner = false
				}
				position++
				continue
			}
			if field == "-i" || field == "--ignore-environment" {
				shellCommand.localOwnerShadowed = true
				shellCommand.localOwner = false
				position++
				continue
			}
			if field == "-C" || field == "--chdir" || field == "-S" || field == "--split-string" {
				if position+1 >= len(fields) {
					return shellCommand, false
				}
				position += 2
				continue
			}
			if strings.HasPrefix(field, "--chdir=") || strings.HasPrefix(field, "--split-string=") || strings.HasPrefix(field, "-") {
				position++
				continue
			}
			if shellAssignmentName(field) == "" {
				break
			}
			if shellAssignmentName(field) == "KTX_DB" {
				shellCommand.localOwnerShadowed = true
				shellCommand.localOwner = documentedAssignmentState(field, bindings) == documentedValueNonempty
			}
			position++
		}
	}
	if position < len(fields) && (fields[position] == "command" || fields[position] == "exec") {
		position++
		for position < len(fields) && strings.HasPrefix(fields[position], "-") {
			position++
		}
	}
	if position >= len(fields) {
		return shellCommand, false
	}

	shellCommand.executable = fields[position]
	shellCommand.invocation = strings.Join(fields[position:], " ")
	shellCommand.executableIdentity = documentedTokenIdentity(fields[position], bindings)
	args := fields[position+1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		flag := args[0]
		if flag == "--expect-runtime-sha256" && len(args) > 1 {
			shellCommand.runtimeIdentity = documentedTokenIdentity(args[1], bindings)
			shellCommand.proof = shellCommand.runtimeIdentity.key() != ""
		}
		if strings.HasPrefix(flag, "--expect-runtime-sha256=") {
			shellCommand.runtimeIdentity = documentedTokenIdentity(strings.TrimPrefix(flag, "--expect-runtime-sha256="), bindings)
			shellCommand.proof = shellCommand.runtimeIdentity.key() != ""
		}
		if strings.Contains(flag, "=") || flag == "--quiet" || flag == "-q" || flag == "--help" || flag == "-h" || flag == "--version" {
			args = args[1:]
		} else if len(args) > 1 {
			args = args[2:]
		} else {
			args = nil
		}
	}
	if len(args) == 0 {
		return shellCommand, isDefiniteDocumentedKTXExecutable(shellCommand.executable)
	}
	variableExecutable := isSimpleShellVariable(shellCommand.executable)
	definiteExecutable := isDefiniteDocumentedKTXExecutable(shellCommand.executable)
	if !variableExecutable && !definiteExecutable {
		return shellCommand, false
	}
	switch args[0] {
	case "binary-capabilities":
		for index := 1; index < len(args); index++ {
			if args[index] == "--expect-sha256" && index+1 < len(args) {
				shellCommand.comparisonIdentity = documentedTokenIdentity(args[index+1], bindings)
				break
			}
			if strings.HasPrefix(args[index], "--expect-sha256=") {
				shellCommand.comparisonIdentity = documentedTokenIdentity(strings.TrimPrefix(args[index], "--expect-sha256="), bindings)
				break
			}
		}
	case "get", "read", "list", "stats", "decompress", "write", "compress", "hook", "run":
		shellCommand.openBacked = true
	case "telemetry":
		shellCommand.openBacked = len(args) > 1 && (args[1] == "record" || args[1] == "list" || args[1] == "collector-open" || args[1] == "event-record")
	case "bench":
		if len(args) >= 2 && (args[1] == "run" || args[1] == "report") {
			shellCommand.openBacked = !containsShellField(args, "--replay-file")
		}
	}
	if variableExecutable && args[0] != "binary-capabilities" && !shellCommand.openBacked {
		return shellCommand, false
	}
	return shellCommand, true
}

func unsafeDocumentedAmbientInvocations(script string) []documentedShellCommand {
	verified := make(map[string]bool)
	bindings := make(map[string]documentedVariableBinding)
	var unsafe []documentedShellCommand
	for _, shellCommand := range splitDocumentedShellCommands(script) {
		fields := documentedShellFields(shellCommand.text)
		if applyDocumentedPersistentState(fields, bindings) {
			continue
		}
		command, found := parseDocumentedKTXCommand(shellCommand, fields, bindings)
		if !found {
			continue
		}
		verificationKey := documentedVerificationKey(command.executableIdentity, command.comparisonIdentity)
		if verificationKey != "" {
			verified[verificationKey] = true
		}
		ownerAuthorized := command.localOwner
		if !command.localOwnerShadowed {
			if owner, ok := bindings["KTX_DB"]; ok {
				ownerAuthorized = owner.exported && owner.state == documentedValueNonempty
			}
		}
		runtimeKey := documentedVerificationKey(command.executableIdentity, command.runtimeIdentity)
		if command.openBacked && !ownerAuthorized && (!command.proof || !verified[runtimeKey]) {
			unsafe = append(unsafe, command)
		}
	}
	return unsafe
}

func documentedHelpExampleSequences(help string) []string {
	var sequences []string
	var current strings.Builder
	inExamples := false
	flush := func() {
		if strings.TrimSpace(current.String()) != "" {
			sequences = append(sequences, current.String())
		}
		current.Reset()
	}
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Example:" || trimmed == "Examples:" {
			flush()
			inExamples = true
			continue
		}
		if inExamples && trimmed == "" {
			flush()
			inExamples = false
			continue
		}
		if inExamples {
			current.WriteString(trimmed)
			current.WriteByte('\n')
		}
	}
	flush()
	return sequences
}

func applyDocumentedPersistentState(fields []string, bindings map[string]documentedVariableBinding) bool {
	if len(fields) == 0 {
		return true
	}
	if fields[0] == ":" {
		for _, field := range fields[1:] {
			if variable, ok := documentedRequiredVariable(field); ok {
				binding := bindings[variable]
				binding.state = documentedValueNonempty
				bindings[variable] = binding
			}
		}
		return true
	}
	if fields[0] == "unset" {
		for _, variable := range fields[1:] {
			if isShellVariableName(variable) {
				binding := bindings[variable]
				binding.generation++
				binding.state = documentedValueEmpty
				binding.exported = false
				bindings[variable] = binding
			}
		}
		return true
	}
	if fields[0] == "export" {
		for _, field := range fields[1:] {
			name := shellAssignmentName(field)
			if name != "" {
				applyDocumentedAssignment(field, true, bindings)
				continue
			}
			if isShellVariableName(field) {
				binding := bindings[field]
				binding.exported = true
				bindings[field] = binding
			}
		}
		return true
	}
	allAssignments := true
	for _, field := range fields {
		if shellAssignmentName(field) == "" {
			allAssignments = false
			break
		}
	}
	if allAssignments {
		for _, field := range fields {
			applyDocumentedAssignment(field, false, bindings)
		}
		return true
	}
	if shellAssignmentName(fields[0]) != "" && strings.HasPrefix(documentedAssignmentValue(fields[0]), "$(") {
		applyDocumentedAssignment(fields[0], false, bindings)
		return true
	}
	return false
}

func applyDocumentedAssignment(field string, export bool, bindings map[string]documentedVariableBinding) {
	name := shellAssignmentName(field)
	if name == "" {
		return
	}
	binding := bindings[name]
	binding.generation++
	binding.state = documentedAssignmentState(field, bindings)
	binding.exported = binding.exported || export
	bindings[name] = binding
}

func documentedAssignmentValue(field string) string {
	separator := strings.IndexByte(field, '=')
	if separator < 0 {
		return ""
	}
	return field[separator+1:]
}

func documentedAssignmentState(field string, bindings map[string]documentedVariableBinding) documentedValueState {
	value := documentedAssignmentValue(field)
	if value == "" {
		return documentedValueEmpty
	}
	if variable, ok := documentedSimpleVariable(value); ok {
		return bindings[variable].state
	}
	if strings.HasPrefix(value, "$(") {
		return documentedValueUnknown
	}
	return documentedValueNonempty
}

func documentedRequiredVariable(field string) (string, bool) {
	if !strings.HasPrefix(field, "${") || !strings.HasSuffix(field, "}") {
		return "", false
	}
	content := strings.TrimSuffix(strings.TrimPrefix(field, "${"), "}")
	separator := strings.Index(content, ":?")
	if separator <= 0 || !isShellVariableName(content[:separator]) {
		return "", false
	}
	return content[:separator], true
}

func documentedTokenIdentity(token string, bindings map[string]documentedVariableBinding) documentedIdentity {
	if token == "" {
		return documentedIdentity{}
	}
	if variable, ok := documentedSimpleVariable(token); ok {
		return documentedIdentity{variable: variable, generation: bindings[variable].generation}
	}
	return documentedIdentity{literal: token}
}

func documentedVerificationKey(executable, digest documentedIdentity) string {
	executableKey, digestKey := executable.key(), digest.key()
	if executableKey == "" || digestKey == "" {
		return ""
	}
	return executableKey + "\x00" + digestKey
}

func splitDocumentedShellCommands(script string) []documentedShellCommand {
	var commands []documentedShellCommand
	var current strings.Builder
	line, startLine := 1, 1
	var quote byte
	escaped := false
	flush := func() {
		text := strings.TrimSpace(current.String())
		if text != "" {
			commands = append(commands, documentedShellCommand{text: text, line: startLine})
		}
		current.Reset()
		startLine = line
	}
	for index := 0; index < len(script); index++ {
		character := script[index]
		if escaped {
			if character == '\n' {
				line++
				current.WriteByte(' ')
			} else {
				current.WriteByte(character)
			}
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteByte(character)
			if character == quote {
				quote = 0
			}
			if character == '\n' {
				line++
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			current.WriteByte(character)
			continue
		}
		if character == '#' && (current.Len() == 0 || strings.HasSuffix(current.String(), " ") || strings.HasSuffix(current.String(), "\t")) {
			for index < len(script) && script[index] != '\n' {
				index++
			}
			flush()
			line++
			startLine = line
			continue
		}
		if character == '\n' || character == ';' || character == '|' || character == '&' {
			flush()
			if character == '\n' {
				line++
			}
			startLine = line
			continue
		}
		current.WriteByte(character)
	}
	if escaped {
		current.WriteByte('\\')
	}
	flush()
	return commands
}

func documentedShellFields(command string) []string {
	var fields []string
	var current strings.Builder
	var quote byte
	escaped := false
	tokenStarted := false
	flush := func() {
		if tokenStarted {
			fields = append(fields, current.String())
			current.Reset()
			tokenStarted = false
		}
	}
	for index := 0; index < len(command); index++ {
		character := command[index]
		if escaped {
			current.WriteByte(character)
			tokenStarted = true
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteByte(character)
			}
			tokenStarted = true
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			tokenStarted = true
			continue
		}
		if character == ' ' || character == '\t' || character == '\n' || character == '\r' {
			flush()
			continue
		}
		current.WriteByte(character)
		tokenStarted = true
	}
	flush()
	return fields
}

func shellAssignmentName(field string) string {
	separator := strings.IndexByte(field, '=')
	if separator <= 0 {
		return ""
	}
	name := field[:separator]
	for index, character := range name {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && character != '_' && (index == 0 || character < '0' || character > '9') {
			return ""
		}
	}
	return name
}

func isDefiniteDocumentedKTXExecutable(candidate string) bool {
	return candidate == "ktx" || strings.Contains(candidate, "/") && filepath.Base(candidate) == "ktx"
}

func isSimpleShellVariable(candidate string) bool {
	_, ok := documentedSimpleVariable(candidate)
	return ok
}

func documentedSimpleVariable(candidate string) (string, bool) {
	if strings.HasPrefix(candidate, "${") && strings.HasSuffix(candidate, "}") {
		name := strings.TrimSuffix(strings.TrimPrefix(candidate, "${"), "}")
		return name, isShellVariableName(name)
	}
	if strings.HasPrefix(candidate, "$") {
		name := strings.TrimPrefix(candidate, "$")
		return name, isShellVariableName(name)
	}
	return "", false
}

func isShellVariableName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		if !letter && character != '_' && (index == 0 || character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func containsShellField(fields []string, wanted string) bool {
	for _, field := range fields {
		if field == wanted || strings.HasPrefix(field, wanted+"=") {
			return true
		}
	}
	return false
}

func TestDocumentedKTXInvocationRecognition(t *testing.T) {
	positive := []struct {
		name   string
		line   string
		proof  bool
		opened bool
	}{
		{name: "bare", line: `ktx list`, opened: true},
		{name: "relative dot", line: `./build/ktx list`, opened: true},
		{name: "relative build", line: `build/ktx list`, opened: true},
		{name: "relative parent", line: `../build/ktx list`, opened: true},
		{name: "general variable", line: `$TOOL list`, opened: true},
		{name: "general braced variable", line: `${TOOL} list`, opened: true},
		{name: "quoted variable", line: `"$TOOL" list`, opened: true},
		{name: "quoted braced variable", line: `"${TOOL}" list`, opened: true},
		{name: "absolute", line: `/usr/local/bin/ktx list`, opened: true},
		{name: "quoted absolute", line: `"/opt/ktx tools/ktx" list`, opened: true},
		{name: "assignment prefix", line: `NOTICE=operator-owned /usr/local/bin/ktx list`, opened: true},
		{name: "env no operand option", line: `env -i KTX_DB=/tmp/owned.db /usr/local/bin/ktx list`, opened: true},
		{name: "env long no operand option", line: `env --ignore-environment KTX_DB=/tmp/owned.db /usr/local/bin/ktx list`, opened: true},
		{name: "env unset other variable", line: `env -u OTHER KTX_DB=/tmp/owned.db /usr/local/bin/ktx list`, opened: true},
		{name: "root boolean flag", line: `ktx --quiet run opencode run "prompt"`, opened: true},
		{name: "continued proof", line: "\"$KTX_VERIFIED_BIN\" \\\n  --expect-runtime-sha256 \"$KTX_VERIFIED_SHA256\" \\\n  list", proof: true, opened: true},
	}
	for _, test := range positive {
		t.Run(test.name, func(t *testing.T) {
			invocation, proof, openBacked := documentedKTXInvocation(test.line)
			if invocation == "" || proof != test.proof || openBacked != test.opened {
				t.Fatalf("invocation=%q proof=%v openBacked=%v commands=%#v fields=%#v", invocation, proof, openBacked, splitDocumentedShellCommands(test.line), documentedShellFields(test.line))
			}
		})
	}

	negative := []struct {
		name string
		line string
	}{
		{name: "hash argument", line: `sha256sum /absolute/path/to/ktx`},
		{name: "quoted argument", line: `printf '%s\n' "$KTX_VERIFIED_BIN"`},
		{name: "test argument", line: `test -x /usr/local/bin/ktx`},
		{name: "assignment value", line: `KTX_VERIFIED_BIN=/usr/local/bin/ktx`},
		{name: "non ktx executable", line: `/usr/local/bin/not-ktx list`},
		{name: "later argument", line: `wrapper /usr/local/bin/ktx list`},
		{name: "variable unknown verb", line: `$TOOL config show`},
	}
	for _, test := range negative {
		t.Run(test.name, func(t *testing.T) {
			if invocation, _, _ := documentedKTXInvocation(test.line); invocation != "" {
				t.Fatalf("unexpected invocation %q", invocation)
			}
		})
	}
}

func TestDocumentedHelpAuthorizationUsesWholeExampleSequence(t *testing.T) {
	tests := []struct {
		name        string
		help        string
		unsafeCount int
	}{
		{
			name: "unverified digest",
			help: `Usage: sample

Example:
  "$KTX_BIN" --expect-runtime-sha256 "$UNVERIFIED_DIGEST" list

Related commands:
  ktx config show
			`,
			unsafeCount: 1,
		},
		{
			name: "prior matching verification",
			help: `Example:
  "$KTX_BIN" binary-capabilities --expect-sha256 "$DIGEST"
  "$KTX_BIN" --expect-runtime-sha256 "$DIGEST" list

Related commands:
			`,
		},
		{
			name: "nonempty owner and warning",
			help: `Examples:
  KTX_DB=/tmp/owned.db /usr/local/bin/ktx list
  This path is operator-owned; verify it is safe before running.

Related commands:
			`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sequences := documentedHelpExampleSequences(test.help)
			if len(sequences) != 1 {
				t.Fatalf("sequence count=%d, want 1", len(sequences))
			}
			unsafe := unsafeDocumentedAmbientInvocations(sequences[0])
			if len(unsafe) != test.unsafeCount {
				t.Fatalf("unsafe count=%d, want %d: %#v", len(unsafe), test.unsafeCount, unsafe)
			}
		})
	}
}

func TestDocumentedAmbientAuthorizationSequences(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		unsafeCount int
	}{
		{
			name: "same installed path and digest",
			script: `"$KTX_VERIFIED_BIN" binary-capabilities --expect-sha256 "$KTX_VERIFIED_SHA256"
"$KTX_VERIFIED_BIN" --expect-runtime-sha256 "$KTX_VERIFIED_SHA256" list`,
		},
		{
			name: "general variable same binding",
			script: `$TOOL binary-capabilities --expect-sha256 "$DIGEST"
${TOOL} --expect-runtime-sha256 "${DIGEST}" list`,
		},
		{
			name:   "explicit command owner with warning",
			script: `KTX_DB=/tmp/owned.db "/usr/local/bin/ktx" list # operator ownership does not prove safety`,
		},
		{
			name: "exported owner",
			script: `export KTX_DB=/tmp/owned.db
/usr/local/bin/ktx list`,
		},
		{
			name: "unset then nonempty command owner",
			script: `export KTX_DB=/tmp/exported.db
env -u KTX_DB KTX_DB=/tmp/owned.db /usr/local/bin/ktx list`,
		},
		{
			name: "asserted variable owner",
			script: `: "${OWNED_DB:?set this to an operator-owned path}"
export KTX_DB="$OWNED_DB"
/usr/local/bin/ktx list`,
		},
		{name: "ignore environment then nonempty owner", script: `env -i KTX_DB=/tmp/owned.db /usr/local/bin/ktx list`},
		{name: "continued owned invocation", script: "KTX_DB=/tmp/owned.db /usr/local/bin/ktx \\\n  list"},
		{
			name: "exact old unsafe absolute path pattern",
			script: `verified_sha256=$(./build/ktx binary-capabilities | jq -r .executable_sha256)
/absolute/path/to/candidate binary-capabilities --expect-sha256 "$verified_sha256"
/absolute/path/to/ktx --expect-runtime-sha256 "$verified_sha256" list`,
			unsafeCount: 1,
		},
		{
			name: "different verified executable",
			script: `./build/ktx binary-capabilities --expect-sha256 "$KTX_VERIFIED_SHA256"
/usr/local/bin/ktx --expect-runtime-sha256 "$KTX_VERIFIED_SHA256" list`,
			unsafeCount: 1,
		},
		{
			name: "different verified digest",
			script: `"$KTX_VERIFIED_BIN" binary-capabilities --expect-sha256 "$BUILD_SHA256"
"$KTX_VERIFIED_BIN" --expect-runtime-sha256 "$INSTALLED_SHA256" list`,
			unsafeCount: 1,
		},
		{name: "proof without prior comparison", script: `/usr/local/bin/ktx --expect-runtime-sha256 "$KTX_VERIFIED_SHA256" list`, unsafeCount: 1},
		{name: "bare variable without verification", script: `$TOOL list`, unsafeCount: 1},
		{name: "braced variable without verification", script: `${TOOL} list`, unsafeCount: 1},
		{name: "quoted variable without verification", script: `"$TOOL" list`, unsafeCount: 1},
		{name: "quoted braced variable without verification", script: `"${TOOL}" list`, unsafeCount: 1},
		{name: "env short unset", script: `env -u KTX_DB /usr/local/bin/ktx list`, unsafeCount: 1},
		{name: "env long unset operand", script: `env --unset KTX_DB /usr/local/bin/ktx list`, unsafeCount: 1},
		{name: "env long unset equals", script: `env --unset=KTX_DB /usr/local/bin/ktx list`, unsafeCount: 1},
		{
			name: "short ignore environment shadows exported owner",
			script: `export KTX_DB=/tmp/owned.db
env -i /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "long ignore environment shadows exported owner",
			script: `export KTX_DB=/tmp/owned.db
env --ignore-environment /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name:        "inline owner then ignore environment",
			script:      `KTX_DB=/tmp/owned.db env -i /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{name: "empty inline owner", script: `KTX_DB= /usr/local/bin/ktx list`, unsafeCount: 1},
		{name: "empty quoted inline owner", script: `KTX_DB="" /usr/local/bin/ktx list`, unsafeCount: 1},
		{
			name: "empty inline owner shadows exported owner",
			script: `export KTX_DB=/tmp/owned.db
KTX_DB= /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "unknown inline owner shadows exported owner",
			script: `export KTX_DB=/tmp/owned.db
KTX_DB="$UNKNOWN_DB" /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "empty env owner shadows exported owner",
			script: `export KTX_DB=/tmp/owned.db
env KTX_DB= /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name:        "inline owner then env unset",
			script:      `KTX_DB=/tmp/owned.db env -u KTX_DB /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name:        "nonempty then unset command owner",
			script:      `env KTX_DB=/tmp/owned.db -u KTX_DB /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name:        "nonempty then empty command owner",
			script:      `env KTX_DB=/tmp/owned.db KTX_DB= /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "empty exported owner",
			script: `export KTX_DB=""
/usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "unknown exported owner",
			script: `export KTX_DB="$UNKNOWN_DB"
/usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "unexported owner variable",
			script: `KTX_DB=/tmp/owned.db
/usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "env unset overrides exported owner",
			script: `export KTX_DB=/tmp/owned.db
env -u KTX_DB /usr/local/bin/ktx list`,
			unsafeCount: 1,
		},
		{
			name: "executable reassignment invalidates verification",
			script: `KTX_BIN=/usr/local/bin/ktx
DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
"$KTX_BIN" binary-capabilities --expect-sha256 "$DIGEST"
KTX_BIN=/different/ktx
"$KTX_BIN" --expect-runtime-sha256 "$DIGEST" list`,
			unsafeCount: 1,
		},
		{
			name: "digest reassignment invalidates verification",
			script: `KTX_BIN=/usr/local/bin/ktx
DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
"$KTX_BIN" binary-capabilities --expect-sha256 "$DIGEST"
DIGEST=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
"$KTX_BIN" --expect-runtime-sha256 "$DIGEST" list`,
			unsafeCount: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unsafe := unsafeDocumentedAmbientInvocations(test.script)
			if len(unsafe) != test.unsafeCount {
				t.Fatalf("unsafe count=%d, want %d: %#v", len(unsafe), test.unsafeCount, unsafe)
			}
		})
	}
}

func readGuidanceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
