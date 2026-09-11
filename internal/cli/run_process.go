package cli

import "time"

// Internal shutdown budget, not a runtime or output-size limit.
const runTerminationGrace = time.Second

type runShutdown struct {
	interrupted bool
	err         error
}
