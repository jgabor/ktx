// Package opencodetelemetry validates the pinned OpenCode plugin event shapes
// used for optional model-step telemetry.
package opencodetelemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const RuntimeVersion = "1.18.4"

var (
	ErrInvalid     = errors.New("invalid OpenCode telemetry event")
	ErrUnsupported = errors.New("unsupported OpenCode telemetry event")
	ErrDuplicate   = errors.New("duplicate OpenCode telemetry event")
)

// Key is the authoritative model-step identity exposed by OpenCode.
type Key struct {
	SessionID string
	MessageID string
	PartID    string
}

type messageKey struct {
	SessionID string
	MessageID string
}

// Identity is optional provider and model metadata correlated from an earlier
// assistant message.updated event.
type Identity struct {
	Provider string
	Model    string
}

// Tokens contains OpenCode-normalized model-step token measurements.
type Tokens struct {
	Input      int64
	Output     int64
	Reasoning  int64
	CacheRead  int64
	CacheWrite int64
}

// Observation is one accepted step-finish part. Ordinal is local collector
// acceptance order, not a provider request number or source timestamp.
type Observation struct {
	Key
	Ordinal  uint64
	Reason   string
	Cost     float64
	Tokens   Tokens
	Provider *string
	Model    *string
}

// Collector correlates message identity, deduplicates model steps, and assigns
// contiguous ordinals. Callers must serialize concurrent calls to Add.
type Collector struct {
	identities map[messageKey]Identity
	seen       map[Key]struct{}
	next       uint64
}

func NewCollector() *Collector {
	return &Collector{
		identities: make(map[messageKey]Identity),
		seen:       make(map[Key]struct{}),
	}
}

// Add accepts an assistant identity event or returns one model-step
// observation. A nil observation means an identity event was accepted.
func (c *Collector) Add(data []byte) (*Observation, error) {
	eventType, err := envelopeType(data)
	if err != nil {
		return nil, err
	}
	switch eventType {
	case "message.updated":
		key, identity, err := DecodeMessageIdentity(data)
		if err != nil {
			return nil, err
		}
		if prior, ok := c.identities[key]; ok && prior != identity {
			return nil, fmt.Errorf("%w: conflicting assistant identity", ErrInvalid)
		}
		c.identities[key] = identity
		return nil, nil
	case "message.part.updated":
		observation, err := DecodeModelStep(data)
		if err != nil {
			return nil, err
		}
		if _, exists := c.seen[observation.Key]; exists {
			return nil, ErrDuplicate
		}
		c.seen[observation.Key] = struct{}{}
		c.next++
		observation.Ordinal = c.next
		if identity, ok := c.identities[messageKey{SessionID: observation.SessionID, MessageID: observation.MessageID}]; ok {
			observation.Provider = stringPtr(identity.Provider)
			observation.Model = stringPtr(identity.Model)
		}
		return &observation, nil
	default:
		return nil, fmt.Errorf("%w: type %q", ErrUnsupported, eventType)
	}
}

// DecodeMessageIdentity validates the assistant message shape used only for
// provider/model correlation.
func DecodeMessageIdentity(data []byte) (messageKey, Identity, error) {
	var event struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return messageKey{}, Identity{}, invalid("malformed JSON")
	}
	if event.Type != "message.updated" {
		return messageKey{}, Identity{}, fmt.Errorf("%w: type %q", ErrUnsupported, event.Type)
	}
	properties, err := object(event.Properties, "properties")
	if err != nil {
		return messageKey{}, Identity{}, err
	}
	info, err := object(properties["info"], "properties.info")
	if err != nil {
		return messageKey{}, Identity{}, err
	}
	role, err := requiredString(info["role"], "properties.info.role")
	if err != nil {
		return messageKey{}, Identity{}, err
	}
	if role != "assistant" {
		return messageKey{}, Identity{}, fmt.Errorf("%w: message role %q", ErrUnsupported, role)
	}
	sessionID, err := requiredString(info["sessionID"], "properties.info.sessionID")
	if err != nil {
		return messageKey{}, Identity{}, err
	}
	messageID, err := requiredString(info["id"], "properties.info.id")
	if err != nil {
		return messageKey{}, Identity{}, err
	}
	provider, err := requiredString(info["providerID"], "properties.info.providerID")
	if err != nil {
		return messageKey{}, Identity{}, err
	}
	model, err := requiredString(info["modelID"], "properties.info.modelID")
	if err != nil {
		return messageKey{}, Identity{}, err
	}
	return messageKey{SessionID: sessionID, MessageID: messageID}, Identity{Provider: provider, Model: model}, nil
}

// DecodeModelStep validates one message.part.updated step-finish event.
func DecodeModelStep(data []byte) (Observation, error) {
	var event struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return Observation{}, invalid("malformed JSON")
	}
	if event.Type != "message.part.updated" {
		return Observation{}, fmt.Errorf("%w: type %q", ErrUnsupported, event.Type)
	}
	properties, err := object(event.Properties, "properties")
	if err != nil {
		return Observation{}, err
	}
	part, err := object(properties["part"], "properties.part")
	if err != nil {
		return Observation{}, err
	}
	partType, err := requiredString(part["type"], "properties.part.type")
	if err != nil {
		return Observation{}, err
	}
	if partType != "step-finish" {
		return Observation{}, fmt.Errorf("%w: part type %q", ErrUnsupported, partType)
	}
	observation := Observation{}
	if observation.SessionID, err = requiredString(part["sessionID"], "properties.part.sessionID"); err != nil {
		return Observation{}, err
	}
	if observation.MessageID, err = requiredString(part["messageID"], "properties.part.messageID"); err != nil {
		return Observation{}, err
	}
	if observation.PartID, err = requiredString(part["id"], "properties.part.id"); err != nil {
		return Observation{}, err
	}
	if observation.Reason, err = requiredString(part["reason"], "properties.part.reason"); err != nil {
		return Observation{}, err
	}
	if observation.Cost, err = nonnegativeNumber(part["cost"], "properties.part.cost"); err != nil {
		return Observation{}, err
	}
	tokens, err := object(part["tokens"], "properties.part.tokens")
	if err != nil {
		return Observation{}, err
	}
	cache, err := object(tokens["cache"], "properties.part.tokens.cache")
	if err != nil {
		return Observation{}, err
	}
	measurements := []struct {
		raw  json.RawMessage
		name string
		set  *int64
	}{
		{tokens["input"], "properties.part.tokens.input", &observation.Tokens.Input},
		{tokens["output"], "properties.part.tokens.output", &observation.Tokens.Output},
		{tokens["reasoning"], "properties.part.tokens.reasoning", &observation.Tokens.Reasoning},
		{cache["read"], "properties.part.tokens.cache.read", &observation.Tokens.CacheRead},
		{cache["write"], "properties.part.tokens.cache.write", &observation.Tokens.CacheWrite},
	}
	for _, measurement := range measurements {
		*measurement.set, err = nonnegativeInteger(measurement.raw, measurement.name)
		if err != nil {
			return Observation{}, err
		}
	}
	return observation, nil
}

func envelopeType(data []byte) (string, error) {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return "", invalid("malformed JSON")
	}
	if strings.TrimSpace(event.Type) == "" {
		return "", invalid("missing type")
	}
	return event.Type, nil
}

func object(raw json.RawMessage, name string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, invalid("missing " + name)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, invalid(name + " must be an object")
	}
	return value, nil
}

func requiredString(raw json.RawMessage, name string) (string, error) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", invalid(name + " must be a non-blank string")
	}
	return value, nil
}

func nonnegativeInteger(raw json.RawMessage, name string) (int64, error) {
	if len(raw) == 0 {
		return 0, invalid("missing " + name)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, invalid(name + " must be a non-negative safe integer")
	}
	value, err := number.Int64()
	if err != nil || value < 0 || value >= 1<<53 {
		return 0, invalid(name + " must be a non-negative safe integer")
	}
	return value, nil
}

func nonnegativeNumber(raw json.RawMessage, name string) (float64, error) {
	var value float64
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, invalid(name + " must be a non-negative number")
	}
	return value, nil
}

func invalid(detail string) error { return fmt.Errorf("%w: %s", ErrInvalid, detail) }

func stringPtr(value string) *string { return &value }
