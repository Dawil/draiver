package reconcile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Dawil/draiver/internal/store"
)

// Controller is the running daemon's identity: the pid of a `ctl up` supervisor
// and a boot nonce minted fresh each time it starts. `ctl up` writes it to the
// root-level controller.json on start and removes it on clean exit, so the
// imperative client verbs can probe "is PID 1 up?" and stamp their transient
// desired-markers with the nonce. The stamp is what makes an imperative start
// transient: a restarted daemon has a new nonce and treats markers stamped with
// the old one as stale (drvctl-016).
type Controller struct {
	PID     int       `json:"pid"`
	Nonce   string    `json:"nonce"`
	Started time.Time `json:"started"`
}

// NewNonce mints a fresh boot nonce — a short random hex string, unique per
// `ctl up` so a restarted daemon never collides with the nonce it wrote before.
func NewNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mint controller nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// WriteController records the running controller's identity at the root-level
// controller.json (atomically, so a concurrent read never sees a half-written
// file).
func WriteController(root store.Root, c Controller) error {
	if err := os.MkdirAll(root.Dir, 0o755); err != nil {
		return fmt.Errorf("ensure data root: %w", err)
	}
	return writeJSONAtomic(root.ControllerPath(), c)
}

// ReadController reads the recorded controller identity. A missing file is not
// an error — it reports ok=false, meaning no controller has claimed this root.
func ReadController(root store.Root) (Controller, bool, error) {
	var c Controller
	ok, err := readJSON(root.ControllerPath(), &c)
	return c, ok, err
}

// RemoveController clears the controller record on a clean daemon exit. A
// missing file is fine — the goal state (no controller) already holds.
func RemoveController(root store.Root) error {
	if err := os.Remove(root.ControllerPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove controller record: %w", err)
	}
	return nil
}

// liveController reads the recorded controller and confirms its process is
// actually alive, so a stale record left by a crashed daemon (removed only on a
// *clean* exit) never passes for a running PID 1. It returns ok=false when no
// controller is recorded or the recorded pid is dead.
func (r *Reconciler) liveController() (Controller, bool) {
	c, ok, err := ReadController(r.opt.Root)
	if err != nil || !ok {
		return Controller{}, false
	}
	if c.PID <= 0 || !r.opt.Proc.Alive(c.PID) {
		return Controller{}, false
	}
	return c, true
}

// desiredMarker is the transient imperative "run this attempt now" flag `ctl
// start` writes, stamped with the nonce of the controller it handed off to. The
// reconcile loop honours it only while a controller with that exact nonce is
// live, and sweeps it otherwise — so an imperative start dies with its daemon.
type desiredMarker struct {
	Nonce string    `json:"nonce"`
	Stamp time.Time `json:"stamp"`
}

// writeDesiredMarker stamps an attempt as imperatively desired by the given
// controller nonce.
func writeDesiredMarker(root store.Root, ticket, attempt string, m desiredMarker) error {
	return writeJSONAtomic(root.DesiredMarkerPath(ticket, attempt), m)
}

// readDesiredMarker reads an attempt's desired-marker; ok=false when none is set.
func readDesiredMarker(root store.Root, ticket, attempt string) (desiredMarker, bool, error) {
	var m desiredMarker
	ok, err := readJSON(root.DesiredMarkerPath(ticket, attempt), &m)
	return m, ok, err
}

// removeDesiredMarker clears an attempt's desired-marker (a swept stale one, or
// one whose imperative session has been retired). Missing is not an error.
func removeDesiredMarker(root store.Root, ticket, attempt string) error {
	if err := os.Remove(root.DesiredMarkerPath(ticket, attempt)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove desired marker %s/%s: %w", ticket, attempt, err)
	}
	return nil
}

// writeJSONAtomic marshals v and writes it via a temp-file rename, so a reader
// never observes a partial file.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
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
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// readJSON unmarshals path into v. A missing file reports ok=false with no
// error — the caller treats "no record" as a normal state, not a failure.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return true, nil
}
