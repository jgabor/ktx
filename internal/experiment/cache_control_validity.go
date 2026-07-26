package experiment

const (
	cacheControlInvalidMode              = "invalid_mode"
	cacheControlInvalidSource            = "invalid_source"
	cacheControlObservedSource           = "observed_source"
	cacheControlIsolatedSource           = "isolated_source"
	cacheControlRuntimeIsolationMismatch = "runtime_isolation_mismatch"
)

// retainedCacheControlIssues is the shared authority for cache-control
// mode/source combinations that retained evidence may claim.
func retainedCacheControlIssues(control *CacheControlManifest) []string {
	if control == nil {
		return []string{cacheControlInvalidMode}
	}
	issues := []string{}
	if control.Mode != "isolated" && control.Mode != "observed" {
		issues = append(issues, cacheControlInvalidMode)
	}
	if control.Source != "runtime_adapter" && control.Source != "provider_adapter" && control.Source != "unavailable" {
		issues = append(issues, cacheControlInvalidSource)
	}
	if control.Mode == "observed" && control.Source != "unavailable" {
		issues = append(issues, cacheControlObservedSource)
	}
	if control.Mode == "isolated" && control.Source == "unavailable" {
		issues = append(issues, cacheControlIsolatedSource)
	}
	if control.Mode == "isolated" && control.Source == "runtime_adapter" && control.RuntimeVersion != "1.18.0" {
		issues = append(issues, cacheControlRuntimeIsolationMismatch)
	}
	return issues
}

func hasRetainedCacheControlIssue(issues []string, target string) bool {
	for _, issue := range issues {
		if issue == target {
			return true
		}
	}
	return false
}
