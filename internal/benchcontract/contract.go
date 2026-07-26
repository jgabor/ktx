package benchcontract

import (
	"fmt"
	"os"
	"regexp"
)

const (
	AttemptSessionEnv     = "KTX_ATTEMPT_SESSION_ID"
	IntervalEnv           = "KTX_BENCH_EVENT_INTERVAL_ID"
	ContractEnv           = "KTX_BENCH_EVENT_CONTRACT"
	CollectorModeEnv      = "KTX_BENCH_COLLECTOR_MODE"
	ContractVersion       = "opencode-event-v2"
	LegacyContractVersion = "opencode-event-v1"
	CollectorRequired     = "required"
	Runtime               = "opencode"
	RuntimeVersion        = "1.18.5"
)

var idPattern = regexp.MustCompile(`^[a-z]{16}$`)

type Environment struct {
	SessionID  string
	IntervalID string
	Contract   string
	Mode       string
}

func ReadEnvironment() (Environment, bool, error) {
	env := Environment{SessionID: os.Getenv(AttemptSessionEnv), IntervalID: os.Getenv(IntervalEnv), Contract: os.Getenv(ContractEnv), Mode: os.Getenv(CollectorModeEnv)}
	present := env.SessionID != "" || env.IntervalID != "" || env.Contract != "" || env.Mode != ""
	if !present {
		return Environment{}, false, nil
	}
	if !idPattern.MatchString(env.SessionID) || !idPattern.MatchString(env.IntervalID) || env.Contract != ContractVersion || env.Mode != CollectorRequired {
		return Environment{}, true, fmt.Errorf("benchmark event collection requires valid %s, %s, %s=%s, and %s=%s", AttemptSessionEnv, IntervalEnv, ContractEnv, ContractVersion, CollectorModeEnv, CollectorRequired)
	}
	if os.Getenv("KTX_SESSION_ID") != "" {
		return Environment{}, true, fmt.Errorf("%s conflicts with KTX_SESSION_ID", AttemptSessionEnv)
	}
	return env, true, nil
}
