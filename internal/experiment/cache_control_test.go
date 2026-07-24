package experiment

import "testing"

func TestCacheCapabilityAdapterSeam(t *testing.T) {
	resolved, err := resolveCacheControl("auto", "experiment", CacheCapabilityRequest{Runtime: "opencode", RuntimeVersion: "custom-version", Provider: "custom", Model: "model"}, func(request CacheCapabilityRequest) CacheCapability {
		if request.Runtime != "opencode" || request.RuntimeVersion != "custom-version" || request.Provider != "custom" || request.Model != "model" {
			t.Fatalf("request = %#v", request)
		}
		return CacheCapability{Supported: true, Source: "provider_adapter"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.mode != "isolated" || resolved.source != "provider_adapter" || resolved.hashes[Baseline] == resolved.hashes[Candidate] {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestRuntimeVersionNormalization(t *testing.T) {
	tests := []struct {
		name, raw   string
		wantVersion string
	}{
		{name: "exact", raw: "1.18.0", wantVersion: "1.18.0"},
		{name: "prefixed", raw: "opencode v1.18.0", wantVersion: "1.18.0"},
		{name: "prior pinned unsupported", raw: "1.17.15", wantVersion: "1.17.15"},
		{name: "unsupported", raw: "1.17.20", wantVersion: "1.17.20"},
		{name: "malformed", raw: "version banana", wantVersion: "version banana"},
		{name: "unknown", raw: "", wantVersion: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identities := normalizeRuntimeIdentities(map[string]string{"opencode": test.raw})
			if identities["opencode"] != test.wantVersion {
				t.Fatalf("normalized version = %q", identities["opencode"])
			}
		})
	}
}

func TestOpenCodeCacheCapabilityKeepsIsolationUnavailable(t *testing.T) {
	for _, request := range []CacheCapabilityRequest{
		{Runtime: "opencode", RuntimeVersion: "1.18.0", Provider: "openai", Model: "test"},
		{Runtime: "opencode", RuntimeVersion: "1.18.0", Provider: "azure", Model: "test"},
		{Runtime: "opencode", RuntimeVersion: "1.18.4", Provider: "opencode", Model: "big-pickle"},
	} {
		if capability := OpenCodeCacheCapability(request); capability.Supported || capability.Source != "unavailable" {
			t.Fatalf("capability for %#v = %#v", request, capability)
		}
	}
}

func TestCacheControlModesDoNotPromoteHistoricalMetadata(t *testing.T) {
	for _, version := range []string{"1.18.0", "1.18.4", "1.17.15", "version banana", "unknown"} {
		auto, err := resolveCacheControl("auto", "experiment", CacheCapabilityRequest{Runtime: "opencode", RuntimeVersion: version, Provider: "openai", Model: "test"}, nil)
		if err != nil || auto.mode != "observed" || auto.source != "unavailable" {
			t.Fatalf("auto version %q = %#v, err=%v", version, auto, err)
		}
		if _, err := resolveCacheControl("isolated", "experiment", CacheCapabilityRequest{Runtime: "opencode", RuntimeVersion: version, Provider: "openai", Model: "test"}, nil); err == nil {
			t.Fatalf("isolated version %q succeeded", version)
		}
	}
}
