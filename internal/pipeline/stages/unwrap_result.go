// unwrap-result stage: project known structured tool-result envelopes to text.
//
// The stage recognizes the successful, single-text-result shapes emitted by
// MCP tools such as Kagi and Context7, plus Codex's web result array. It is
// intentionally strict: any malformed, ambiguous, multipart, error, or
// unfamiliar shape passes through byte-for-byte. Originals remain recoverable
// at the pipeline boundary like every other ktx transformation.
package stages

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type resultTextBlock struct {
	Type string  `json:"type"`
	Text *string `json:"text"`
}

type structuredResult struct {
	Result *string `json:"result"`
}

type fastMCPMetadata struct {
	FastMCP *fastMCPWrapper `json:"fastmcp"`
}

type fastMCPWrapper struct {
	WrapResult *bool `json:"wrap_result"`
}

// UnwrapResult returns the decoded text carried by a recognized structured
// result envelope. Non-matching input is returned unchanged with no error so
// the stage remains a silent, conservative preprocessor in a default profile.
func UnwrapResult(input []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return input, nil
	}

	switch trimmed[0] {
	case '{':
		// Avoid fully decoding unrelated JSON objects on the hook's critical
		// path. The strict decoder below remains the source of truth.
		if !bytes.Contains(trimmed, []byte(`"content"`)) {
			return input, nil
		}
		return unwrapMCPObject(input, trimmed)
	case '[':
		if !bytes.Contains(trimmed, []byte(`"input_text"`)) {
			return input, nil
		}
		return unwrapWebResult(input, trimmed)
	default:
		return input, nil
	}
}

func unwrapMCPObject(input, data []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := decodeSingleJSON(data, &fields); err != nil {
		return input, nil
	}

	contentRaw, ok := fields["content"]
	if !ok {
		return input, nil
	}
	var content []resultTextBlock
	if err := decodeSingleJSON(contentRaw, &content); err != nil || len(content) != 1 {
		return input, nil
	}
	block := content[0]
	if block.Type != "text" || block.Text == nil || *block.Text == "" {
		return input, nil
	}

	switch len(fields) {
	case 1:
		// Context7's content-only MCP result.
		return []byte(*block.Text), nil
	case 4:
		// Kagi's FastMCP wrapper duplicates the canonical text in
		// structuredContent.result. Only discard it after proving the full
		// observed successful shape and byte-for-byte equality.
		metaRaw, hasMeta := fields["_meta"]
		structuredRaw, hasStructured := fields["structuredContent"]
		isErrorRaw, hasIsError := fields["isError"]
		if !hasMeta || !hasStructured || !hasIsError {
			return input, nil
		}

		var isError bool
		if err := decodeSingleJSON(isErrorRaw, &isError); err != nil || isError {
			return input, nil
		}
		var meta fastMCPMetadata
		if err := decodeSingleJSON(metaRaw, &meta); err != nil ||
			meta.FastMCP == nil || meta.FastMCP.WrapResult == nil || !*meta.FastMCP.WrapResult {
			return input, nil
		}
		var structured structuredResult
		if err := decodeSingleJSON(structuredRaw, &structured); err != nil ||
			structured.Result == nil || *structured.Result != *block.Text {
			return input, nil
		}
		return []byte(*block.Text), nil
	default:
		return input, nil
	}
}

func unwrapWebResult(input, data []byte) ([]byte, error) {
	var content []resultTextBlock
	if err := decodeSingleJSON(data, &content); err != nil || len(content) != 1 {
		return input, nil
	}
	block := content[0]
	if block.Type != "input_text" || block.Text == nil || *block.Text == "" {
		return input, nil
	}
	return []byte(*block.Text), nil
}

// decodeSingleJSON rejects unknown fields in typed destinations and trailing
// JSON values. Callers treat every rejection as an ordinary stage no-op.
func decodeSingleJSON(data []byte, dst any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkUniqueJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON token")
		}
		return err
	}
	return nil
}

func walkUniqueJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := walkUniqueJSONValue(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkUniqueJSONValue(dec); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}

	_, err = dec.Token()
	return err
}
