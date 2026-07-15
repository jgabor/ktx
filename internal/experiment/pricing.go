package experiment

import (
	"encoding/json"
	"fmt"
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
	if metadata == nil {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(experimentDir, filepath.FromSlash(metadata.Path)))
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

func preparePricingEvidence(experimentDir string, manifest Manifest, attempts []Attempt, summaryData []byte) (*pricingSnapshot, error) {
	hasAttemptEstimate := false
	for _, attempt := range attempts {
		if attempt.EstimatedAPICost != nil {
			hasAttemptEstimate = true
			break
		}
	}
	hasSummaryEstimate := summaryHasEstimatedEvidence(summaryData)
	if manifest.Pricing == nil {
		if hasAttemptEstimate || hasSummaryEstimate {
			return nil, fmt.Errorf("estimated_api_cost evidence requires a retained pricing snapshot")
		}
		return nil, nil
	}
	provider, model, err := splitModel(manifest.Model)
	if err != nil {
		return nil, fmt.Errorf("pricing metadata requires manifest model provider/name: %w", err)
	}
	if manifest.Pricing.Provider != provider || manifest.Pricing.Model != model {
		return nil, fmt.Errorf("pricing metadata provider/model does not match manifest model %q", manifest.Model)
	}
	snapshot, err := loadPricingSnapshot(experimentDir, manifest.Pricing)
	if err != nil {
		return nil, err
	}
	if snapshot.Provider != provider || snapshot.Model != model {
		return nil, fmt.Errorf("pricing snapshot embedded provider/model does not match manifest model %q", manifest.Model)
	}
	return snapshot, nil
}

func summaryHasEstimatedEvidence(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	var visit func(any) bool
	visit = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if strings.Contains(key, "estimated_api_cost") && child != nil {
					return true
				}
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}

func estimateAttempts(attempts []Attempt, snapshot *pricingSnapshot) []Attempt {
	if snapshot == nil {
		return attempts
	}
	for i := range attempts {
		if attempts[i].TokensIn == nil || attempts[i].TokensOut == nil {
			attempts[i].EstimatedAPICost = nil
			continue
		}
		input, output := int64(*attempts[i].TokensIn), int64(*attempts[i].TokensOut)
		var reasoning, cacheRead, cacheWrite *int64
		if attempts[i].Reasoning != nil {
			v := int64(*attempts[i].Reasoning)
			reasoning = &v
		}
		if attempts[i].CacheRead != nil {
			v := int64(*attempts[i].CacheRead)
			cacheRead = &v
		}
		if attempts[i].CacheWrite != nil {
			v := int64(*attempts[i].CacheWrite)
			cacheWrite = &v
		}
		attempts[i].EstimatedAPICost = db.EstimateAPICost(db.EstimatedCostInputs{UncachedInput: input, Output: output, Reasoning: reasoning, CacheRead: cacheRead, CacheWrite: cacheWrite}, snapshot.Price)
	}
	return attempts
}
