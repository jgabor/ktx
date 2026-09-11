package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/usage"
)

const recordingSlabSize = 64 * 1024

// recordingSpool is private scratch until the producer's actual completion
// authority is supplied. Closing bytes alone never makes a replay recording.
// Capture failures are latched rather than returned to the producer's pipe.
type recordingSpool struct {
	root *os.Root
	path string
	file interface {
		io.Writer
		Sync() error
		Close() error
	}
	hash   hash.Hash
	size   int64
	err    error
	closed bool
	row    dbpkg.RecordedStream
}

func newRecordingSpool() (*recordingSpool, error) {
	path, err := dbpkg.DefaultPath()
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	base, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	id, err := dbpkg.NewID()
	if err != nil {
		return nil, err
	}
	name := "recording-" + id
	if err := base.Mkdir(name, 0o700); err != nil {
		return nil, err
	}
	root, err := base.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	f, err := root.OpenFile("stream", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	directory, err := base.Open(".")
	if err == nil {
		err = errors.Join(directory.Sync(), directory.Close())
	}
	if err != nil {
		_ = f.Close()
		_ = root.Close()
		return nil, err
	}
	s := &recordingSpool{root: root, path: filepath.Join(parent, name), file: f, hash: sha256.New(), row: dbpkg.RecordedStream{ID: id, StreamFormat: "opencode-jsonl-1", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	return s, nil
}

func (s *recordingSpool) Write(p []byte) (int, error) {
	if s.err == nil {
		n, err := s.file.Write(p)
		_, _ = s.hash.Write(p[:n])
		s.size += int64(n)
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		s.err = err
	}
	return len(p), nil
}

func (s *recordingSpool) ReadFrom(r io.Reader) (int64, error) {
	n, err := io.CopyBuffer(struct{ io.Writer }{s}, r, make([]byte, recordingSlabSize))
	s.err = errors.Join(s.err, err)
	return n, err
}

func (s *recordingSpool) finish() error {
	if !s.closed {
		s.err = errors.Join(s.err, s.file.Sync(), s.file.Close())
		s.closed = true
		s.row.ContentHash = hex.EncodeToString(s.hash.Sum(nil))
	}
	return s.err
}

func (s *recordingSpool) close() {
	_ = s.finish()
	_ = s.root.Close()
}

// complete records an already established producer receipt, not an inference
// from a full-looking file. Failure leaves the only byte copy in place.
func (s *recordingSpool) complete(session string) error {
	if err := s.finish(); err != nil {
		return err
	}
	s.row.SessionID = session
	return s.metadata()
}

func (s *recordingSpool) metadata() error {
	data, err := json.Marshal(struct {
		Recording dbpkg.RecordedStream `json:"recording"`
		Size      int64                `json:"size"`
	}{s.row, s.size})
	if err != nil {
		return err
	}
	f, err := s.root.OpenFile("completion.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	fileErr := errors.Join(writeErr, f.Sync(), f.Close())
	dir, err := s.root.Open(".")
	if err != nil {
		return errors.Join(fileErr, err)
	}
	return errors.Join(fileErr, dir.Sync(), dir.Close())
}

func (s *recordingSpool) open() (*os.File, error) {
	if !s.closed || s.err != nil {
		return nil, errors.New("recording capture incomplete")
	}
	return s.root.Open("stream")
}

func (s *recordingSpool) remove() error {
	if err := s.root.Remove("stream"); err != nil {
		return err
	}
	if err := s.root.Remove("completion.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *recordingSpool) publish(db *dbpkg.DB, benchID string) error {
	if s.row.SessionID == "" || !s.closed || s.err != nil {
		return errors.New("recording completion unavailable")
	}
	s.row.BenchRunID = benchID
	if err := s.metadata(); err != nil {
		return err
	}
	insertErr := db.PublishRecordedStream(&s.row, s.size, func() (string, error) {
		f, err := s.open()
		if err != nil {
			return "", err
		}
		data, readErr := io.ReadAll(f)
		return string(data), errors.Join(readErr, f.Close())
	})
	// Even an uncertain commit is reconciled by exact identity, never by
	// session existence or a copied hash. No automatic retry of ambiguity.
	got, err := db.GetRecordedStreamByID(s.row.ID)
	if err != nil {
		return fmt.Errorf("recording unavailable/unverified; private copy retained: %w", errors.Join(insertErr, err))
	}
	if got.ID != s.row.ID || got.SessionID != s.row.SessionID || got.BenchRunID != s.row.BenchRunID || got.StreamFormat != s.row.StreamFormat || got.CapturedAt != s.row.CapturedAt || int64(len(got.StreamText)) != s.size || got.ContentHash != s.row.ContentHash || dbpkg.ContentHash(got.StreamText) != s.row.ContentHash {
		return errors.Join(insertErr, errors.New("committed recording verification failed"))
	}
	if err := s.remove(); err != nil {
		return fmt.Errorf("committed recording verified; private cleanup incomplete: %w", err)
	}
	return nil
}

// copyDiagnostics verifies bytes while forwarding without trimming or adding a
// newline. An incomplete delivery never authorizes removal of diagnostic scratch.
func (s *recordingSpool) copyDiagnostics(w io.Writer) error {
	f, err := s.open()
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.CopyBuffer(io.MultiWriter(w, h), struct{ io.Reader }{f}, make([]byte, recordingSlabSize))
	err = errors.Join(copyErr, f.Close())
	if n != s.size || hex.EncodeToString(h.Sum(nil)) != s.row.ContentHash {
		err = errors.Join(err, fmt.Errorf("diagnostic size or hash mismatch"))
	}
	return err
}

// observedReader distinguishes underlying I/O failures from the parsers' existing
// intentional stop/skip boundaries without changing their EOF behavior.
type observedReader struct {
	io.Reader
	err  error
	size int64
}

func (r *observedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.size += int64(n)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

func interpretRecording(s *recordingSpool) (sm streamMetrics, answer string, err error) {
	for pass := 0; pass < 2; pass++ {
		f, err := s.open()
		if err != nil {
			return sm, answer, err
		}
		h := sha256.New()
		r := &observedReader{Reader: io.TeeReader(f, h)}
		if pass == 0 {
			sm = extractStreamMetricsReader(r, usage.NewAggregateOnly())
		} else {
			answer = extractAgentOutputReader(r)
		}
		// Scoring intentionally stops at oversized records. Drain solely to
		// check I/O/integrity; never feed later bytes back to the scorer.
		_, drainErr := io.CopyBuffer(io.Discard, r, make([]byte, recordingSlabSize))
		err = errors.Join(r.err, drainErr, f.Close())
		if r.size != s.size || hex.EncodeToString(h.Sum(nil)) != s.row.ContentHash {
			err = errors.Join(err, errors.New("recording size or hash changed during interpretation"))
		}
		if err != nil {
			return sm, answer, err
		}
	}
	return sm, answer, nil
}
