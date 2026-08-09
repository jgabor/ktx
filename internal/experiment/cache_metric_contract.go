package experiment

import "fmt"

const (
	cacheAwareMethodologyV5         = "v5-cache-aware"
	promptCacheHitRateMethodologyV6 = "v6-prompt-cache-hit-rate"
	promptCacheMetricContractV2     = "cache-reuse-v2"
)

func isCacheAwareMethodology(version string) bool {
	return version == cacheAwareMethodologyV5 || version == promptCacheHitRateMethodologyV6
}

func promptCacheMetricContractActivated(manifest Manifest) bool {
	return manifest.Methodology.Version == promptCacheHitRateMethodologyV6 &&
		manifest.Methodology.CacheMetricContract == promptCacheMetricContractV2
}

func supportsPromptCacheTokenHitRate(manifest Manifest) bool {
	return promptCacheMetricContractActivated(manifest) &&
		manifest.CacheControl != nil &&
		manifest.CacheControl.RuntimeVersion == directCacheRuntimeVersion &&
		manifest.Identities[directCacheRuntime] == directCacheRuntimeVersion
}

func supportsCacheMetricComparison(manifest Manifest) bool {
	return supportsDirectCachedInputShare(manifest) || supportsPromptCacheTokenHitRate(manifest)
}

func cacheMetricContractVersion(manifest Manifest) string {
	if promptCacheMetricContractActivated(manifest) {
		return promptCacheMetricContractV2
	}
	return directCacheContractVersion
}

func validateCacheMetricContractActivation(manifest Manifest) error {
	marker := manifest.Methodology.CacheMetricContract
	switch marker {
	case "":
		if manifest.Methodology.Version == promptCacheHitRateMethodologyV6 {
			return fmt.Errorf("methodology %q requires cache_metric_contract %q", promptCacheHitRateMethodologyV6, promptCacheMetricContractV2)
		}
		return nil
	case promptCacheMetricContractV2:
		if manifest.Methodology.Version != promptCacheHitRateMethodologyV6 {
			return fmt.Errorf("cache_metric_contract %q requires methodology %q", promptCacheMetricContractV2, promptCacheHitRateMethodologyV6)
		}
		return nil
	default:
		return fmt.Errorf("unsupported cache_metric_contract %q", marker)
	}
}
