// Package session persists a draiverctld session's runtime state: the session/
// directory under an attempt, holding session.json (identity), meter.json
// (token/cost/context + watchdog counters), and stream.jsonl (the raw
// stream-json tee).
//
// This state is deliberately kept OUT of the attempt's hash-chained log. It is
// rebuildable from disk plus the live process table, so a draiverctld restart
// re-derives it and a stale or corrupt session/ can be discarded without
// touching the durable truth. Whole-file records (session.json, meter.json) are
// replaced atomically via a temp-file rename; stream.jsonl is append-only and
// safe for concurrent appends.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/store"
)

// Identity is a session's stable runtime identity — session.json. Every field
// is re-derivable (the session id and worktree from the log or a live process,
// the pid from the process table), which is why it lives outside the hash chain.
type Identity struct {
	Adapter   string    `json:"adapter"`         // coding-agent adapter, e.g. claude-code
	Model     string    `json:"model,omitempty"` // e.g. opus-4.8 (optional)
	SessionID string    `json:"session_id"`      // the agent's cattle handle, for --resume
	PID       int       `json:"pid,omitempty"`   // live process; 0 when not running
	Worktree  string    `json:"worktree"`        // the session's working directory
	Started   time.Time `json:"started"`
}

// Meter is a session's live cost + liveness tally — meter.json. It is folded
// into attempt.md metrics on retire and is fully rebuildable by replaying
// stream.jsonl.
type Meter struct {
	// Usage is the latest cumulative token/cost/context snapshot from the
	// session's stream. EventUsage is cumulative, so this is a replace, not a
	// sum (see RecordUsage). ContextTokens is the current context-window fill —
	// the live gauge the supervisor surfaces.
	Usage agent.Usage `json:"usage"`

	// Respawns counts how many times this attempt's session has been reaped and
	// restarted; the StartLimit ceiling watches this counter.
	Respawns int `json:"respawns"`

	// LastEventAt is the progress watchdog's heartbeat: the time of the most
	// recent semantic event. Zero until the first event lands.
	LastEventAt time.Time `json:"last_event_at,omitempty"`
}

// Store is the handle to one attempt's session/ directory. It owns reading and
// writing the three runtime files. A single mutex serializes this process's
// atomic file replacements and its appends to stream.jsonl, and guards the
// lazily-opened stream append handle.
//
// A Store is safe for concurrent use. Close it to release the stream handle.
type Store struct {
	root    store.Root
	ticket  string
	attempt string

	mu     sync.Mutex
	stream *os.File // lazily opened O_APPEND handle for stream.jsonl
}

// Open returns a session store for one attempt, creating its session/ directory
// if needed. It errors if the attempt itself does not exist. The returned Store
// must be Closed to release the stream.jsonl append handle.
func Open(root store.Root, ticket, attempt string) (*Store, error) {
	if !root.AttemptExists(ticket, attempt) {
		return nil, fmt.Errorf("session: attempt %s/%s does not exist", ticket, attempt)
	}
	if err := root.EnsureSessionDir(ticket, attempt); err != nil {
		return nil, err
	}
	return &Store{root: root, ticket: ticket, attempt: attempt}, nil
}

func (s *Store) metaPath() string   { return s.root.SessionMetaPath(s.ticket, s.attempt) }
func (s *Store) meterPath() string  { return s.root.SessionMeterPath(s.ticket, s.attempt) }
func (s *Store) streamPath() string { return s.root.SessionStreamPath(s.ticket, s.attempt) }

// --- identity (session.json) ---

// WriteIdentity persists the session identity, replacing the file atomically.
func (s *Store) WriteIdentity(id Identity) error {
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("session: marshal identity: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeAtomic(s.metaPath(), append(data, '\n')); err != nil {
		return fmt.Errorf("session: write identity: %w", err)
	}
	return nil
}

// ReadIdentity loads the session identity. A missing file returns a wrapped
// fs.ErrNotExist (test with errors.Is).
func (s *Store) ReadIdentity() (Identity, error) {
	data, err := os.ReadFile(s.metaPath())
	if err != nil {
		return Identity{}, fmt.Errorf("session: read identity: %w", err)
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return Identity{}, fmt.Errorf("session: parse identity: %w", err)
	}
	return id, nil
}

// --- meter (meter.json) ---

// ReadMeter loads the meter. A missing file returns a wrapped fs.ErrNotExist
// (test with errors.Is); callers that want "zero if absent" can check for it.
func (s *Store) ReadMeter() (Meter, error) {
	data, err := os.ReadFile(s.meterPath())
	if err != nil {
		return Meter{}, fmt.Errorf("session: read meter: %w", err)
	}
	var m Meter
	if err := json.Unmarshal(data, &m); err != nil {
		return Meter{}, fmt.Errorf("session: parse meter: %w", err)
	}
	return m, nil
}

// WriteMeter persists the meter, replacing the file atomically.
func (s *Store) WriteMeter(m Meter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeMeterLocked(m)
}

func (s *Store) writeMeterLocked(m Meter) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("session: marshal meter: %w", err)
	}
	if err := writeAtomic(s.meterPath(), append(data, '\n')); err != nil {
		return fmt.Errorf("session: write meter: %w", err)
	}
	return nil
}

// UpdateMeter applies fn to the current meter and writes the result atomically,
// all under the store lock so concurrent updates can't lose writes. A missing
// meter starts from the zero value. It returns the persisted meter.
func (s *Store) UpdateMeter(fn func(*Meter)) (Meter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.ReadMeter()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Meter{}, err
	}
	fn(&m)
	if err := s.writeMeterLocked(m); err != nil {
		return Meter{}, err
	}
	return m, nil
}

// RecordUsage folds a stream usage snapshot into the meter. EventUsage is
// cumulative per session, so the snapshot replaces the meter's Usage rather than
// summing into it. Folding across respawns (where a fresh session's cumulative
// count restarts) is the caller's concern, via UpdateMeter.
func (s *Store) RecordUsage(u agent.Usage) (Meter, error) {
	return s.UpdateMeter(func(m *Meter) { m.Usage = u })
}

// --- stream tee (stream.jsonl) ---

// AppendStream tees one raw stream-json line to stream.jsonl. It is safe for
// concurrent callers: in-process appends are serialized by the store lock, and
// the file is opened O_APPEND so each write lands at EOF even if another process
// is appending too. A trailing newline is added if absent; an embedded newline
// is rejected so one call is always exactly one line.
func (s *Store) AppendStream(line []byte) error {
	trimmed := bytes.TrimRight(line, "\n")
	if bytes.IndexByte(trimmed, '\n') >= 0 {
		return fmt.Errorf("session: stream line contains an embedded newline")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		f, err := os.OpenFile(s.streamPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("session: open stream: %w", err)
		}
		s.stream = f
	}
	buf := make([]byte, 0, len(trimmed)+1)
	buf = append(buf, trimmed...)
	buf = append(buf, '\n')
	if _, err := s.stream.Write(buf); err != nil {
		return fmt.Errorf("session: append stream: %w", err)
	}
	return nil
}

// ReadStream returns the raw stream-json lines teed so far, in order, for replay
// or rebuild. Blank lines are skipped and a missing stream file reads as empty.
func (s *Store) ReadStream() ([]json.RawMessage, error) {
	f, err := os.Open(s.streamPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: open stream: %w", err)
	}
	defer f.Close()

	var lines []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tolerate long stream-json lines
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		cp := make([]byte, len(b))
		copy(cp, b)
		lines = append(lines, json.RawMessage(cp))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("session: read stream: %w", err)
	}
	return lines, nil
}

// Close releases the stream.jsonl append handle. It is idempotent; the other
// files are opened per call and need no cleanup.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		return nil
	}
	err := s.stream.Close()
	s.stream = nil
	return err
}

// writeAtomic replaces path with data by writing a sibling temp file, fsyncing
// it, and renaming over the target — so a reader ever sees either the old file
// or the complete new one, never a partial write. The temp file is removed on
// any error path (and the post-rename Remove is a harmless no-op).
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}
