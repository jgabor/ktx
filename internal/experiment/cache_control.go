package experiment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type CacheCapabilityRequest struct {
	Runtime        string
	RuntimeVersion string
	Provider       string
	Model          string
}

type CacheCapability struct {
	Supported bool
	Source    string
}

type CacheCapabilityResolver func(CacheCapabilityRequest) CacheCapability

type resolvedCacheControl struct {
	mode   string
	source string
	raw    map[string]string
	hashes map[string]string
}

func OpenCodeCacheCapability(request CacheCapabilityRequest) CacheCapability {
	// OpenCode 1.18.0 merges arbitrary per-model options after its
	// session-derived defaults. Restrict the built-in adapter to provider paths
	// whose OpenCode source explicitly uses promptCacheKey.
	if request.Runtime != "opencode" || request.RuntimeVersion != "1.18.0" {
		return CacheCapability{Source: "unavailable"}
	}
	switch request.Provider {
	case "openai", "azure":
		return CacheCapability{Supported: true, Source: "runtime_adapter"}
	default:
		return CacheCapability{Source: "unavailable"}
	}
}

func resolveCacheControl(requested, experimentID string, request CacheCapabilityRequest, resolver CacheCapabilityResolver) (resolvedCacheControl, error) {
	if requested == "" {
		requested = "auto"
	}
	if requested != "auto" && requested != "isolated" && requested != "observed" {
		return resolvedCacheControl{}, fmt.Errorf("invalid cache control %q: want auto, isolated, or observed", requested)
	}
	if requested == "observed" {
		return resolvedCacheControl{mode: "observed", source: "unavailable"}, nil
	}
	if resolver == nil {
		resolver = OpenCodeCacheCapability
	}
	capability := resolver(request)
	if !capability.Supported {
		if requested == "isolated" {
			return resolvedCacheControl{}, fmt.Errorf("isolated cache control is unsupported for %s version %q provider %q; use --cache-control observed", request.Runtime, request.RuntimeVersion, request.Provider)
		}
		return resolvedCacheControl{mode: "observed", source: "unavailable"}, nil
	}
	resolved := resolvedCacheControl{mode: "isolated", source: capability.Source, raw: map[string]string{}, hashes: map[string]string{}}
	for _, side := range Sides {
		entropy := make([]byte, 16)
		if _, err := rand.Read(entropy); err != nil {
			return resolvedCacheControl{}, fmt.Errorf("generate cache namespace: %w", err)
		}
		digest := sha256.Sum256(append([]byte("ktx-cache-namespace\x00"+experimentID+"\x00"+side+"\x00"), entropy...))
		raw := "ktx-" + hex.EncodeToString(digest[:])
		retained := sha256.Sum256([]byte(raw))
		resolved.raw[side] = raw
		resolved.hashes[side] = hex.EncodeToString(retained[:])
	}
	return resolved, nil
}

var semanticVersionIdentity = regexp.MustCompile(`^(?:(?:opencode|opencode-ai)[ /])?v?(\d+\.\d+\.\d+)$`)

func normalizeRuntimeIdentities(identities map[string]string) map[string]string {
	result := make(map[string]string, len(identities)+1)
	for key, raw := range identities {
		key = strings.TrimSpace(strings.ToLower(key))
		value := strings.TrimSpace(raw)
		if key == "opencode" {
			if match := semanticVersionIdentity.FindStringSubmatch(strings.ToLower(value)); match != nil {
				value = match[1]
			}
			if value == "" {
				value = "unknown"
			}
		}
		result[key] = value
	}
	if result["opencode"] == "" {
		result["opencode"] = "unknown"
	}
	return result
}

func opencodeConfigContent(provider, model, namespace string) (string, error) {
	data, err := json.Marshal(map[string]any{"provider": map[string]any{provider: map[string]any{"models": map[string]any{model: map[string]any{"options": map[string]any{"promptCacheKey": namespace}}}}}})
	return string(data), err
}
