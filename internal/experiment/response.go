package experiment

import (
	"context"
	"errors"
)

const maxInspectionResponseBytes = 64 << 10

var errInspectionResponseSize = errors.New("response exceeds 65536-byte maximum")

// inspectionResponse retains a one-byte sentinel and cancels the producer as
// soon as the raw protocol response exceeds its limit, before any parsing.
type inspectionResponse struct {
	data   [maxInspectionResponseBytes + 1]byte
	n      int
	cancel context.CancelFunc
}

func (response *inspectionResponse) Len() int { return response.n }

func (response *inspectionResponse) Write(p []byte) (int, error) {
	n := copy(response.data[response.n:], p)
	response.n += n
	if response.n > maxInspectionResponseBytes {
		response.cancel()
		return n, errInspectionResponseSize
	}
	return n, nil
}

func inspectResponse(ctx context.Context, command Command, run func(context.Context, Command) (CommandResult, error)) ([]byte, CommandResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	response := &inspectionResponse{cancel: cancel}
	command.Stdout = response
	// Run must settle process-group and container cleanup before inspecting the
	// buffer. A writer error alone cannot stop a producer that ignores SIGPIPE.
	result, err := run(ctx, command)
	if response.n > maxInspectionResponseBytes {
		return nil, result, errors.Join(errInspectionResponseSize, err)
	}
	return response.data[:response.n], result, err
}
