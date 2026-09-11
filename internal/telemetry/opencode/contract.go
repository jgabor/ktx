// Package opencode validates the pinned OpenCode plugin event shapes used for
// optional model-step telemetry.
package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jgabor/ktx/internal/telemetry"
)

const (
	Runtime        = "opencode"
	RuntimeVersion = "1.18.5"
)

var (
	ErrInvalid     = errors.New("invalid OpenCode telemetry event")
	ErrUnsupported = errors.New("unsupported OpenCode telemetry event")
	ErrDuplicate   = errors.New("duplicate OpenCode telemetry event")
)

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

// Collector retains first observations until persistence acknowledges them.
// Calls, including persistence, are serialized; retries require redelivery.
type Collector struct {
	mu         sync.Mutex
	identities map[messageKey]Identity
	observed   map[telemetry.Key]*delivery
	next       uint64
}

type delivery struct {
	evidence   string
	payload    []byte
	capturedAt string
	accepted   bool
}

func NewCollector() *Collector {
	return &Collector{
		identities: make(map[messageKey]Identity),
		observed:   make(map[telemetry.Key]*delivery),
	}
}

// Collect correlates an identity or persists one model step. A nil persistence
// error acknowledges durable acceptance, including an exact database duplicate.
// Failed or uncertain writes retain the first payload and observation time.
// The callback must not reenter the collector.
func (c *Collector) Collect(data []byte, persist func(*telemetry.Observation, string) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	eventType, err := envelopeType(data)
	if err != nil {
		return err
	}
	switch eventType {
	case "message.updated":
		key, identity, err := DecodeMessageIdentity(data)
		if err != nil {
			return err
		}
		if prior, ok := c.identities[key]; ok && prior != identity {
			return fmt.Errorf("%w: conflicting assistant identity", ErrInvalid)
		}
		c.identities[key] = identity
		return nil
	case "message.part.updated":
		observation, err := DecodeModelStep(data)
		if err != nil {
			return err
		}
		observation.Source = telemetry.SourceEventHook
		return c.accept(observation, persist)
	default:
		return fmt.Errorf("%w: type %q", ErrUnsupported, eventType)
	}
}

// CollectStream persists one OpenCode JSONL step_finish record from the wrapper.
func (c *Collector) CollectStream(data []byte, persist func(*telemetry.Observation, string) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	observation, err := DecodeStreamModelStep(data)
	if err != nil {
		return err
	}
	observation.Source = telemetry.SourceWrapperStream
	return c.accept(observation, persist)
}

func (c *Collector) accept(observation telemetry.Observation, persist func(*telemetry.Observation, string) error) error {
	evidence, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	prior := c.observed[observation.Key]
	if prior != nil {
		if prior.evidence != string(evidence) {
			return invalid("conflicting model-step redelivery")
		}
		if prior.accepted {
			return ErrDuplicate
		}
	} else {
		if observation.Source == telemetry.SourceEventHook {
			if identity, ok := c.identities[messageKey{SessionID: observation.SessionID, MessageID: observation.MessageID}]; ok {
				observation.Provider = stringPtr(identity.Provider)
				observation.Model = stringPtr(identity.Model)
			}
		}
		c.next++
		observation.Ordinal = c.next
		payload, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		prior = &delivery{evidence: string(evidence), payload: payload, capturedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		c.observed[observation.Key] = prior
	}
	// Decode a fresh copy so a failed callback cannot mutate retained evidence.
	var retained telemetry.Observation
	if err := json.Unmarshal(prior.payload, &retained); err != nil {
		return err
	}
	if err := persist(&retained, prior.capturedAt); err != nil {
		return err
	}
	prior.accepted = true
	return nil
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
func DecodeModelStep(data []byte) (telemetry.Observation, error) {
	var event struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return telemetry.Observation{}, invalid("malformed JSON")
	}
	if event.Type != "message.part.updated" {
		return telemetry.Observation{}, fmt.Errorf("%w: type %q", ErrUnsupported, event.Type)
	}
	properties, err := object(event.Properties, "properties")
	if err != nil {
		return telemetry.Observation{}, err
	}
	part, err := object(properties["part"], "properties.part")
	if err != nil {
		return telemetry.Observation{}, err
	}
	return normalizePart(part, "properties.part")
}

// DecodeStreamModelStep normalizes the wrapper's OpenCode JSONL envelope.
func DecodeStreamModelStep(data []byte) (telemetry.Observation, error) {
	var event struct {
		Type string          `json:"type"`
		Part json.RawMessage `json:"part"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return telemetry.Observation{}, invalid("malformed JSON")
	}
	if event.Type != "step_finish" {
		return telemetry.Observation{}, fmt.Errorf("%w: type %q", ErrUnsupported, event.Type)
	}
	part, err := object(event.Part, "part")
	if err != nil {
		return telemetry.Observation{}, err
	}
	observation, err := normalizePart(part, "part")
	if err != nil {
		return telemetry.Observation{}, err
	}
	provider, providerOK := optionalString(part["provider"])
	model, modelOK := optionalString(part["model"])
	if providerOK && modelOK {
		observation.Provider = stringPtr(provider)
		observation.Model = stringPtr(model)
	}
	return observation, nil
}

func normalizePart(part map[string]json.RawMessage, path string) (telemetry.Observation, error) {
	partType, err := requiredString(part["type"], path+".type")
	if err != nil {
		return telemetry.Observation{}, err
	}
	if partType != "step-finish" {
		return telemetry.Observation{}, fmt.Errorf("%w: part type %q", ErrUnsupported, partType)
	}
	observation := telemetry.Observation{Runtime: Runtime, RuntimeVersion: RuntimeVersion}
	if observation.SessionID, err = requiredString(part["sessionID"], path+".sessionID"); err != nil {
		return telemetry.Observation{}, err
	}
	if observation.MessageID, err = requiredString(part["messageID"], path+".messageID"); err != nil {
		return telemetry.Observation{}, err
	}
	if observation.PartID, err = requiredString(part["id"], path+".id"); err != nil {
		return telemetry.Observation{}, err
	}
	if observation.Reason, err = requiredString(part["reason"], path+".reason"); err != nil {
		return telemetry.Observation{}, err
	}
	tokens, tokensState := objectState(part["tokens"])
	cache, cacheState := childObjectState(tokens, tokensState, "cache")
	observation.Cost = telemetry.DecodeCostMeasurement(part["cost"], telemetry.MeasurementMeasured)
	observation.Tokens = telemetry.Tokens{
		Input:      telemetry.DecodeIntegerMeasurement(tokens["input"], tokensState),
		Output:     telemetry.DecodeIntegerMeasurement(tokens["output"], tokensState),
		Reasoning:  telemetry.DecodeIntegerMeasurement(tokens["reasoning"], tokensState),
		CacheRead:  telemetry.DecodeIntegerMeasurement(cache["read"], cacheState),
		CacheWrite: telemetry.DecodeIntegerMeasurement(cache["write"], cacheState),
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

func objectState(raw json.RawMessage) (map[string]json.RawMessage, telemetry.MeasurementState) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, telemetry.MeasurementUnknown
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, telemetry.MeasurementMalformed
	}
	return value, telemetry.MeasurementMeasured
}

func childObjectState(parent map[string]json.RawMessage, state telemetry.MeasurementState, key string) (map[string]json.RawMessage, telemetry.MeasurementState) {
	if state != telemetry.MeasurementMeasured {
		return nil, state
	}
	return objectState(parent[key])
}

func optionalString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func requiredString(raw json.RawMessage, name string) (string, error) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", invalid(name + " must be a non-blank string")
	}
	return value, nil
}

func invalid(detail string) error { return fmt.Errorf("%w: %s", ErrInvalid, detail) }

func stringPtr(value string) *string { return &value }
