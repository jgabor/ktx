package experiment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
)

// The explicit input is limited to a single OpenAI model registration. Other
// OpenCode settings can load plugins, files, or packages outside the archive.
type modelRegistration struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Attachment  *bool  `json:"attachment"`
	Reasoning   *bool  `json:"reasoning"`
	Temperature *bool  `json:"temperature"`
	ToolCall    *bool  `json:"tool_call"`
	Modalities  struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
	Variants map[string]struct {
		ReasoningEffort string `json:"reasoningEffort"`
	} `json:"variants"`
}

func decodeStrict(data []byte, value any) error {
	// encoding/json accepts case-insensitive struct fields and duplicate keys;
	// OpenCode uses case-sensitive keys. Check every object before decoding.
	if err := validateRegistrationKeys(json.NewDecoder(bytes.NewReader(data)), reflect.TypeOf(value).Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func validateRegistrationKeys(decoder *json.Decoder, shape reflect.Type) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("duplicate or invalid runtime configuration key")
			}
			seen[key] = true
			var child reflect.Type
			switch shape.Kind() {
			case reflect.Map:
				child = shape.Elem()
			case reflect.Struct:
				for i := 0; i < shape.NumField(); i++ {
					field := shape.Field(i)
					if field.Tag.Get("json") == key {
						child = field.Type
						break
					}
				}
			}
			if child == nil {
				return fmt.Errorf("unsupported runtime configuration key %q", key)
			}
			if err := validateRegistrationKeys(decoder, child); err != nil {
				return err
			}
		}
	case json.Delim('['):
		if shape.Kind() != reflect.Slice {
			return errors.New("unexpected runtime configuration array")
		}
		for decoder.More() {
			if err := validateRegistrationKeys(decoder, shape.Elem()); err != nil {
				return err
			}
		}
	default:
		return nil // Scalar types and required values are checked by typed decoding.
	}
	_, err = decoder.Token()
	return err
}

// OpenCode substitutes these tokens in raw text, before JSON escape decoding.
var runtimeConfigReference = regexp.MustCompile(`\{(?:env|file):[^}]+\}`)

func validateRuntimeConfig(data []byte, selectedModel, selectedVariant string) error {
	if runtimeConfigReference.Match(data) {
		return errors.New("runtime configuration cannot contain env or file references")
	}
	provider, model, err := splitModel(selectedModel)
	if err != nil {
		return err
	}
	if provider != "openai" {
		return errors.New("explicit runtime configuration supports only openai model registration")
	}
	var config struct {
		Provider map[string]struct {
			NPM    string                       `json:"npm"`
			Models map[string]modelRegistration `json:"models"`
		} `json:"provider"`
	}
	if err := decodeStrict(data, &config); err != nil {
		return fmt.Errorf("invalid runtime configuration: %w", err)
	}
	if len(config.Provider) != 1 {
		return errors.New("runtime configuration requires exactly one provider")
	}
	entry, ok := config.Provider[provider]
	if !ok || entry.NPM != "@ai-sdk/openai" || len(entry.Models) != 1 {
		return errors.New("runtime configuration requires only the selected openai provider and one model with @ai-sdk/openai")
	}
	registration, ok := entry.Models[model]
	if !ok || registration.ID != model || registration.Name == "" || registration.Attachment == nil || registration.Reasoning == nil || registration.Temperature == nil || registration.ToolCall == nil ||
		len(registration.Modalities.Input) == 0 || len(registration.Modalities.Output) == 0 || registration.Limit.Context <= 0 || registration.Limit.Input <= 0 || registration.Limit.Output <= 0 {
		return errors.New("runtime configuration requires a complete registration for the selected model")
	}
	for _, modality := range append(registration.Modalities.Input, registration.Modalities.Output...) {
		if modality != "text" && modality != "image" {
			return errors.New("runtime configuration has unsupported modality")
		}
	}
	for name, variant := range registration.Variants {
		if name == "" || (variant.ReasoningEffort != "low" && variant.ReasoningEffort != "medium" && variant.ReasoningEffort != "high") {
			return errors.New("runtime configuration has unsupported variant")
		}
	}
	if selectedVariant != "" {
		if _, ok := registration.Variants[selectedVariant]; !ok {
			return errors.New("runtime configuration does not register the selected variant")
		}
	}
	return nil
}

// Auth is not configuration-free: wellknown entries load remote config/plugins.
// Mount exactly the checked bytes from a private, disposable location, never
// from the mutable source or the retained experiment archive.
func stageRuntimeConfigAuth(source string) (string, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return "", errors.New("cannot read OpenCode auth for explicit runtime configuration")
	}
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil || entries == nil {
		return "", errors.New("explicit runtime configuration requires an OpenCode auth object")
	}
	for _, entry := range entries {
		var kind string
		if json.Unmarshal(entry["type"], &kind) == nil && kind == "wellknown" {
			return "", errors.New("explicit runtime configuration cannot use wellknown OpenCode auth; use API or OAuth credentials without remote configuration")
		}
	}
	dir, err := os.MkdirTemp("", "ktx-runtime-auth-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "auth.json")
	// The private parent protects host access; the mounted file is readable by
	// the image's node user, which may have a different UID.
	if err := os.WriteFile(path, data, 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

func captureRuntimeConfig(experimentDir, source, model, variant string) (*InputMetadata, error) {
	if source == "" {
		return nil, nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read runtime configuration: %w", err)
	}
	if err := validateRuntimeConfig(data, model, variant); err != nil {
		return nil, err
	}
	const relative = "inputs/runtime-config/opencode.json"
	destination := filepath.Join(experimentDir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return nil, err
	}
	// The containing directory is private; the bind-mounted file must be
	// readable by the image's node user even when the host UID differs.
	if err := os.WriteFile(destination, data, 0o644); err != nil {
		return nil, err
	}
	return &InputMetadata{ID: "opencode", Kind: "runtime-config", Format: "json", Path: relative, SHA256: sha256Bytes(data)}, nil
}

func runtimeConfigInput(manifest Manifest) (InputMetadata, error) {
	if manifest.RuntimeConfig == nil {
		for _, input := range manifest.Inputs {
			if input.Kind == "runtime-config" {
				return InputMetadata{}, errors.New("unclaimed retained runtime configuration")
			}
		}
		return InputMetadata{}, nil
	}
	config := *manifest.RuntimeConfig
	if config.ID != "opencode" || config.Kind != "runtime-config" || config.Format != "json" || config.Path != "inputs/runtime-config/opencode.json" || config.SHA256 == "" {
		return InputMetadata{}, errors.New("invalid runtime configuration identity")
	}
	count := 0
	for _, input := range manifest.Inputs {
		if input.Kind == "runtime-config" {
			count++
			if input != config {
				return InputMetadata{}, errors.New("runtime configuration differs from retained input")
			}
		}
	}
	if count != 1 {
		return InputMetadata{}, errors.New("runtime configuration requires retained input")
	}
	return config, nil
}

func (a *archive) validateRuntimeConfig(manifest Manifest) error {
	config, err := runtimeConfigInput(manifest)
	if err != nil || config.Path == "" {
		return err
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc", ".opencode"} {
		_, err := a.stat(filepath.Join(manifest.Repository.Snapshot.Path, name))
		if err == nil {
			return fmt.Errorf("retained repository contains conflicting project %s", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect retained project configuration: %w", err)
		}
	}
	data, err := a.readFile(config.Path)
	if err != nil {
		return fmt.Errorf("read retained runtime configuration: %w", err)
	}
	variant := ""
	if manifest.Variant != nil {
		variant = *manifest.Variant
	}
	return validateRuntimeConfig(data, manifest.Model, variant)
}

func rejectProjectRuntimeConfig(root string) error {
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		_, err := os.Lstat(filepath.Join(root, name))
		if err == nil {
			return fmt.Errorf("explicit runtime configuration cannot coexist with project %s", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
