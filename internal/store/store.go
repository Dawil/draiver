// Package store resolves the on-disk layout of the ticket data root and lists
// tickets. It owns paths only; reading and appending the log lives in ticketlog.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Root is a resolved data-root directory holding one folder per ticket.
type Root struct{ Dir string }

// Resolve picks the data root from, in order: an explicit flag value, the
// DRAIVER_DATA environment variable, then ~/.draiver/data.
func Resolve(flagVal string) (Root, error) {
	dir := flagVal
	if dir == "" {
		dir = os.Getenv("DRAIVER_DATA")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Root{}, fmt.Errorf("resolve data root: %w", err)
		}
		dir = filepath.Join(home, ".draiver", "data")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Root{}, fmt.Errorf("resolve data root: %w", err)
	}
	return Root{Dir: abs}, nil
}

// TicketDir is the folder for a ticket id.
func (r Root) TicketDir(id string) string { return filepath.Join(r.Dir, id) }

// LogDir is the append-only event directory for a ticket.
func (r Root) LogDir(id string) string { return filepath.Join(r.TicketDir(id), "log") }

// SpecPath is the immutable design input for a ticket.
func (r Root) SpecPath(id string) string { return filepath.Join(r.TicketDir(id), "spec.md") }

// StatePath is the generated projection for a ticket (never read as truth).
func (r Root) StatePath(id string) string { return filepath.Join(r.TicketDir(id), "state.md") }

// ArtefactsDir holds blobs the log references.
func (r Root) ArtefactsDir(id string) string { return filepath.Join(r.TicketDir(id), "artefacts") }

// Exists reports whether a ticket folder is present.
func (r Root) Exists(id string) bool {
	fi, err := os.Stat(r.TicketDir(id))
	return err == nil && fi.IsDir()
}

// EnsureTicketDirs creates the ticket folder and its log/artefacts subdirs.
func (r Root) EnsureTicketDirs(id string) error {
	for _, d := range []string{r.LogDir(id), r.ArtefactsDir(id)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("ensure %s: %w", d, err)
		}
	}
	return nil
}

// ListTickets returns the ticket ids present under the root, sorted. A missing
// root is treated as empty, not an error.
func (r Root) ListTickets() ([]string, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}
