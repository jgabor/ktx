package experiment

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/jgabor/ktx/internal/db"
)

type pricingSnapshot struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Price    db.Price `json:"price"`
}

type pinnedPricingSnapshot struct {
	Data     []byte
	SHA256   string
	Snapshot pricingSnapshot
}

func loadPinnedPricingSnapshot(path, expectedSHA256, manifestModel string) (*pinnedPricingSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hash := sha256Bytes(data)
	if expectedSHA256 != "" && hash != expectedSHA256 {
		return nil, fmt.Errorf("pricing snapshot hash mismatch")
	}
	var snapshot pricingSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	provider, model, err := splitModel(manifestModel)
	if err != nil || snapshot.Provider != provider || snapshot.Model != model {
		return nil, fmt.Errorf("pricing snapshot provider/model does not match %q", manifestModel)
	}
	return &pinnedPricingSnapshot{Data: data, SHA256: hash, Snapshot: snapshot}, nil
}

func writePinnedPricingSnapshot(experimentDir string, pinned *pinnedPricingSnapshot) (*PricingSnapshotMetadata, error) {
	if err := os.WriteFile(filepath.Join(experimentDir, "pricing.json"), pinned.Data, 0o644); err != nil {
		return nil, err
	}
	price := pinned.Snapshot.Price
	var retrievedAt *string
	if price.RetrievedAt != "" {
		retrievedAt = &price.RetrievedAt
	}
	sourceURL := price.SourceURL
	if sourceURL == "" {
		sourceURL = db.ModelsDevURL
	}
	return &PricingSnapshotMetadata{Path: "pricing.json", SHA256: pinned.SHA256, SourceURL: sourceURL, RetrievedAt: retrievedAt, Provider: pinned.Snapshot.Provider, Model: pinned.Snapshot.Model}, nil
}

func splitModel(value string) (string, string, error) {
	provider, model, ok := strings.Cut(value, "/")
	if !ok || provider == "" || model == "" {
		return "", "", fmt.Errorf("model must be provider/name: %q", value)
	}
	return provider, model, nil
}

func writePricingSnapshot(experimentDir, provider, model string, price db.Price) (*PricingSnapshotMetadata, error) {
	data, err := MarshalJSON(pricingSnapshot{Provider: provider, Model: model, Price: price})
	if err != nil {
		return nil, err
	}
	path := filepath.Join(experimentDir, "pricing.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	var retrievedAt *string
	if price.RetrievedAt != "" {
		value := price.RetrievedAt
		retrievedAt = &value
	}
	sourceURL := price.SourceURL
	if sourceURL == "" {
		sourceURL = db.ModelsDevURL
	}
	return &PricingSnapshotMetadata{Path: "pricing.json", SHA256: sha256Bytes(data), SourceURL: sourceURL, RetrievedAt: retrievedAt, Provider: provider, Model: model}, nil
}

func loadPricingSnapshot(experimentDir string, metadata *PricingSnapshotMetadata) (*pricingSnapshot, error) {
	a, err := openArchive(experimentDir)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	return a.loadPricingSnapshot(metadata)
}

func (a *archive) loadPricingSnapshot(metadata *PricingSnapshotMetadata) (*pricingSnapshot, error) {
	if metadata == nil {
		return nil, nil
	}
	data, err := a.readFile(metadata.Path)
	if err != nil {
		return nil, err
	}
	if sha256Bytes(data) != metadata.SHA256 {
		return nil, fmt.Errorf("pricing snapshot hash mismatch")
	}
	var snapshot pricingSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func preparePricingEvidence(experimentDir string, manifest Manifest) (*pricingSnapshot, error) {
	a, err := openArchive(experimentDir)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	return a.preparePricingEvidence(manifest)
}

func (a *archive) preparePricingEvidence(manifest Manifest) (*pricingSnapshot, error) {
	if manifest.Pricing == nil {
		return nil, nil
	}
	provider, model, err := splitModel(manifest.Model)
	if err != nil {
		return nil, fmt.Errorf("pricing metadata requires manifest model provider/name: %w", err)
	}
	if manifest.Pricing.Provider != provider || manifest.Pricing.Model != model {
		return nil, fmt.Errorf("pricing metadata provider/model does not match manifest model %q", manifest.Model)
	}
	snapshot, err := a.loadPricingSnapshot(manifest.Pricing)
	if err != nil {
		return nil, err
	}
	if snapshot.Provider != provider || snapshot.Model != model {
		return nil, fmt.Errorf("pricing snapshot embedded provider/model does not match manifest model %q", manifest.Model)
	}
	return snapshot, nil
}

func estimateAttemptAPICost(attempt Attempt, snapshot *pricingSnapshot) db.EstimatedCostResult {
	reasons := []string{}
	if snapshot == nil {
		reasons = append(reasons, "pricing_snapshot_unavailable")
	}
	values := map[string]*float64{
		"uncached_input": attempt.TokensIn,
		"output":         attempt.TokensOut,
		"reasoning":      attempt.Reasoning,
		"cache_read":     attempt.CacheRead,
		"cache_write":    attempt.CacheWrite,
	}
	tokens := map[string]int64{}
	for _, name := range []string{"uncached_input", "output", "reasoning", "cache_read", "cache_write"} {
		value := values[name]
		if value == nil {
			reasons = append(reasons, name+"_unavailable")
			continue
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			reasons = append(reasons, name+"_nonfinite")
			continue
		}
		if *value < 0 || math.Trunc(*value) != *value || *value > math.MaxInt64 {
			reasons = append(reasons, name+"_invalid")
			continue
		}
		tokens[name] = int64(*value)
	}
	if len(reasons) > 0 {
		return db.EstimatedCostResult{AvailabilityReasons: reasons}
	}
	input, output := tokens["uncached_input"], tokens["output"]
	reasoning, cacheRead, cacheWrite := tokens["reasoning"], tokens["cache_read"], tokens["cache_write"]
	return db.EstimateAPICostDetails(db.EstimatedCostInputs{
		UncachedInput: input, Output: output,
		Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite,
	}, snapshot.Price)
}

func estimateAttempts(attempts []Attempt, snapshot *pricingSnapshot) []Attempt {
	for i := range attempts {
		estimate := estimateAttemptAPICost(attempts[i], snapshot)
		attempts[i].EstimatedAPICost = estimate.Value
		attempts[i].estimatedAPICostReasons = append([]string(nil), estimate.AvailabilityReasons...)
	}
	return attempts
}
