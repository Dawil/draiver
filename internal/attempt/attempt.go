// Package attempt creates and reads attempts on a ticket. An attempt is one
// journey at a ticket from a starting point — its own working tree, its own log
// and hash chain — and is the unit compared across coding tools/models.
package attempt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"draiver/internal/event"
	"draiver/internal/store"
	"draiver/internal/ticketlog"
)

// Meta is an attempt's static provenance, persisted as attempt.md frontmatter.
// The control state is derived from the log, not stored here. The commented
// fields are reserved for later (metrics, worktree) and not populated in MVP.
type Meta struct {
	ID      string    `yaml:"id"`
	Ticket  string    `yaml:"ticket"`
	Tool    string    `yaml:"tool"`  // coding-agent adapter, e.g. claude-code
	Model   string    `yaml:"model"` // e.g. opus-4.8 (optional)
	Actor   string    `yaml:"actor"`
	Started time.Time `yaml:"started"`
	From    string    `yaml:"from,omitempty"` // provenance: branched from this attempt
}

// New identifies the parameters for creating an attempt.
type New struct {
	Tool  string
	Model string
	Actor string
	From  string
	TS    time.Time // zero → now
}

// Create allocates the next attempt id on a ticket, writes attempt.md, ensures
// the attempt's dirs, appends the genesis "created" event, and returns the meta.
// Id allocation is race-safe: the attempt dir is created exclusively and a
// collision retries with the next id.
func Create(root store.Root, ticket string, n New) (Meta, error) {
	if !root.Exists(ticket) {
		return Meta{}, fmt.Errorf("attempt: ticket %q does not exist", ticket)
	}
	if err := os.MkdirAll(root.AttemptsDir(ticket), 0o755); err != nil {
		return Meta{}, fmt.Errorf("attempt: ensure attempts dir: %w", err)
	}
	ts := n.TS
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC().Truncate(time.Second)

	const maxRetries = 8
	for i := 0; i < maxRetries; i++ {
		id, err := nextID(root, ticket)
		if err != nil {
			return Meta{}, err
		}
		// Claim the id exclusively; a concurrent creator racing the same id fails here.
		if err := os.Mkdir(root.AttemptDir(ticket, id), 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return Meta{}, fmt.Errorf("attempt: claim %s: %w", id, err)
		}
		if err := root.EnsureAttemptDirs(ticket, id); err != nil {
			return Meta{}, err
		}
		m := Meta{
			ID: id, Ticket: ticket, Tool: n.Tool, Model: n.Model,
			Actor: n.Actor, Started: ts, From: n.From,
		}
		if err := writeMeta(root, m); err != nil {
			return Meta{}, err
		}
		body := fmt.Sprintf("Attempt %s started", id)
		if n.Tool != "" {
			body += " (tool: " + n.Tool
			if n.Model != "" {
				body += ", model: " + n.Model
			}
			body += ")"
		}
		if _, err := ticketlog.Append(root, ticket, id, event.Event{
			Type: "created", Actor: n.Actor, TS: ts, Body: body,
		}); err != nil {
			return Meta{}, err
		}
		return m, nil
	}
	return Meta{}, fmt.Errorf("attempt: gave up after %d id collisions", maxRetries)
}

// List returns a ticket's attempt ids, sorted.
func List(root store.Root, ticket string) ([]string, error) {
	return root.ListAttempts(ticket)
}

// Latest returns the highest attempt id on a ticket, or ok=false if none exist.
func Latest(root store.Root, ticket string) (string, bool, error) {
	ids, err := root.ListAttempts(ticket)
	if err != nil {
		return "", false, err
	}
	if len(ids) == 0 {
		return "", false, nil
	}
	return ids[len(ids)-1], true, nil // ListAttempts is sorted; zero-padded ids sort numerically
}

// LoadMeta reads an attempt's attempt.md. A missing file yields a zero Meta with
// the id filled in, so older/hand-made attempts still load.
func LoadMeta(root store.Root, ticket, id string) (Meta, error) {
	data, err := os.ReadFile(root.AttemptMetaPath(ticket, id))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{ID: id, Ticket: ticket}, nil
		}
		return Meta{}, fmt.Errorf("read attempt.md: %w", err)
	}
	m, err := parseMeta(data)
	if err != nil {
		return Meta{}, err
	}
	if m.ID == "" {
		m.ID = id
	}
	if m.Ticket == "" {
		m.Ticket = ticket
	}
	return m, nil
}

func nextID(root store.Root, ticket string) (string, error) {
	ids, err := root.ListAttempts(ticket)
	if err != nil {
		return "", err
	}
	max := 0
	for _, id := range ids {
		if n, err := strconv.Atoi(id); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("%04d", max+1), nil
}

func writeMeta(root store.Root, m Meta) error {
	front, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal attempt.md: %w", err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n\n")
	b.WriteString("<!-- Attempt provenance. The control state is derived from the log. -->\n")
	if err := os.WriteFile(root.AttemptMetaPath(m.Ticket, m.ID), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write attempt.md: %w", err)
	}
	return nil
}

func parseMeta(data []byte) (Meta, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return Meta{}, nil
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Meta{}, nil
	}
	var m Meta
	if err := yaml.Unmarshal([]byte(rest[:end]), &m); err != nil {
		return Meta{}, fmt.Errorf("parse attempt.md frontmatter: %w", err)
	}
	return m, nil
}
