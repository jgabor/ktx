package experiment

import (
	"io"
	"sync"
)

// Diagnostic output is not evidence or a parsed command response. Drain excess
// bytes without retaining them or changing the command's exit status.
type diagnosticLimit struct {
	mu        sync.Mutex
	dst       io.Writer
	remaining int
	truncated bool
}

func newDiagnosticLimit(dst io.Writer) *diagnosticLimit {
	return &diagnosticLimit{dst: dst, remaining: 64 << 10}
}

func (w *diagnosticLimit) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	keep := min(n, w.remaining)
	if keep > 0 {
		written, err := w.dst.Write(p[:keep])
		w.remaining -= written
		if err != nil {
			return written, err
		}
		if written != keep {
			return written, io.ErrShortWrite
		}
	}
	if keep < n && !w.truncated {
		w.truncated = true
		if _, err := io.WriteString(w.dst, "\n[diagnostics truncated]\n"); err != nil {
			return keep, err
		}
	}
	return n, nil
}
