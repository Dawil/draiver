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

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Meta is an attempt's static provenance, persisted as attempt.md frontmatter.
// The control state is derived from the log, not stored here.
type Meta struct {
	ID      string    `yaml:"id"`
	Ticket  string    `yaml:"ticket"`
	Tool    string    `yaml:"tool"`           // coding-agent adapter, e.g. claude-code
	Model   string    `yaml:"model"`          // e.g. opus-4.8 (optional)
	Repo    string    `yaml:"repo,omitempty"` // local path to the git working tree the attempt targets (drvctl-015)
	Base    string    `yaml:"base,omitempty"` // branch the attempt lands back into: merge target / sync source (drvctl-021)
	Actor   string    `yaml:"actor"`
	Started time.Time `yaml:"started"`
	From    string    `yaml:"from,omitempty"` // provenance: branched from this attempt

	// ProtocolVersion is the version handle of the invariant protocol the attempt's
	// session launched with in its system-prompt append (drvctl-034), folded from
	// session.json on retire. It pins the run to the exact protocol text for A/B and
	// reproducibility. Empty when no protocol was appended (or the attempt predates
	// the field), so it renders only once there is something to record.
	ProtocolVersion string `yaml:"protocol_version,omitempty"`

	// AdapterVersion is the version of the coding-agent adapter binary the attempt's
	// session launched against (e.g. "2.1.216" for claude-code, drvctl-033), folded
	// from session.json on retire. Pinned for the session (auto-update gated off), it
	// pins the run to the adapter build for A/B and for attributing a prompt-cache
	// prefix bust to an operator upgrade. Empty when unknown (probe failed, or the
	// attempt predates the field), so it renders only once there is something to record.
	AdapterVersion string `yaml:"adapter_version,omitempty"`

	// Metrics is the meter's final token/caching tally, folded in on retire — the
	// "reserved metrics room" made real (drvctl-031). Nil until an attempt retires
	// with a metered session, so it renders only once there is something to record.
	Metrics *agent.Metrics `yaml:"metrics,omitempty"`
}

// New identifies the parameters for creating an attempt.
type New struct {
	Tool  string
	Model string
	Repo  string
	Base  string
	Actor string
	From  string
	TS    time.Time // zero → now
}

// ErrRepoRequired is returned by Create when asked to mint an attempt with no
// repo path. Every attempt must record where it runs (drvctl-017): the daemon
// cuts each session's worktree from this path, and an attempt that omits it can
// never come up. The check lives here, the one chokepoint every creation path
// funnels through, so no caller can mint a repo-less attempt; the commands layer
// this with an earlier, friendlier message.
var ErrRepoRequired = errors.New("a repo path is required: set --repo to the local git working tree this attempt targets")

// Create allocates the next attempt id on a ticket, writes attempt.md, ensures
// the attempt's dirs, appends the genesis "created" event, and returns the meta.
// Id allocation is race-safe: the attempt dir is created exclusively and a
// collision retries with the next id.
func Create(root store.Root, ticket string, n New) (Meta, error) {
	if !root.Exists(ticket) {
		return Meta{}, fmt.Errorf("attempt: ticket %q does not exist", ticket)
	}
	// Repo is mandatory and stored trimmed, so no path (including a whitespace-only
	// one) can slip through — validated before any side effect so a rejected Create
	// writes nothing (drvctl-017).
	n.Repo = strings.TrimSpace(n.Repo)
	if n.Repo == "" {
		return Meta{}, ErrRepoRequired
	}
	// Base is optional and stored trimmed. It records the branch this attempt lands
	// back into; when omitted the commands layer defaults it to the bound repo's
	// current branch, but a hand-made or legacy attempt may carry none.
	n.Base = strings.TrimSpace(n.Base)
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
			ID: id, Ticket: ticket, Tool: n.Tool, Model: n.Model, Repo: n.Repo, Base: n.Base,
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

// WriteMeta writes an attempt's attempt.md from m, self-documenting the
// load-bearing frontmatter: a set field renders as a real YAML line, an unset
// optional field (repo/base/tool/model) renders as a commented example showing
// the key, a sample value, and what consumes it. It re-emits the canonical block,
// so it doubles as the write path for `draiver attempt set` — which, like
// `draiver title` on spec.md, edits this static metadata outside the hash-chained
// log. parseMeta ignores YAML comment lines, so write→parse→write is idempotent.
func WriteMeta(root store.Root, m Meta) error {
	return writeMeta(root, m)
}

func writeMeta(root store.Root, m Meta) error {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(metaFrontmatter(m))
	b.WriteString("---\n\n")
	b.WriteString("<!-- Attempt provenance. The control state is derived from the log. -->\n")
	if err := os.WriteFile(root.AttemptMetaPath(m.Ticket, m.ID), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write attempt.md: %w", err)
	}
	return nil
}

// metaFrontmatter renders the attempt.md frontmatter body (between the fences).
// Identity fields (id/ticket/actor) always render; started renders only when set
// (so a rewrite of a legacy file that never recorded one doesn't inject a bogus
// zero time); from renders only when set (pure provenance, not human-fillable).
// The four optional/load-bearing fields render as real lines when set and as
// commented hints when unset, so a hand-editor sees the key, an example, and what
// reads it — turning an invisible-when-empty field into a self-documenting one.
func metaFrontmatter(m Meta) string {
	var b strings.Builder
	b.WriteString(metaLine("id", m.ID))
	b.WriteString(metaLine("ticket", m.Ticket))
	b.WriteString(optionalMetaLine("tool", m.Tool, "claude-code", "coding-agent adapter that runs this attempt"))
	b.WriteString(optionalMetaLine("model", m.Model, "opus-4.8", "model the adapter runs (optional)"))
	b.WriteString(optionalMetaLine("repo", m.Repo, "/path/to/repo", "local git working tree the daemon cuts this attempt's worktree from"))
	b.WriteString(optionalMetaLine("base", m.Base, "main", "branch this attempt lands into; used by `ctl merge`/`sync`"))
	b.WriteString(metaLine("actor", m.Actor))
	if !m.Started.IsZero() {
		b.WriteString(yamlMarshalLine(map[string]time.Time{"started": m.Started}))
	}
	if strings.TrimSpace(m.From) != "" {
		b.WriteString(metaLine("from", m.From))
	}
	// ProtocolVersion renders only when recorded (machine-written on retire, like
	// metrics below — not a hand-editable hint, so no commented placeholder).
	if strings.TrimSpace(m.ProtocolVersion) != "" {
		b.WriteString(metaLine("protocol_version", m.ProtocolVersion))
	}
	// AdapterVersion renders only when recorded (machine-written on retire, like
	// protocol_version above — not a hand-editable hint, so no commented placeholder).
	if strings.TrimSpace(m.AdapterVersion) != "" {
		b.WriteString(metaLine("adapter_version", m.AdapterVersion))
	}
	// Metrics renders as a nested block only when the meter has been folded in on
	// retire — machine-written, not a hand-editable hint, so unlike the optional
	// lines above it has no commented placeholder. parseMeta reads it straight back.
	if m.Metrics != nil {
		b.WriteString(yamlMarshalLine(map[string]agent.Metrics{"metrics": *m.Metrics}))
	}
	return b.String()
}

// metaLine renders one `key: value` frontmatter line (newline included),
// YAML-encoding the value so a colon/quote/leading-# can't produce invalid
// frontmatter that every later status/brief/webui read would choke on.
func metaLine(key, val string) string {
	return yamlMarshalLine(map[string]string{key: val})
}

// optionalMetaLine renders a set value as a real line, or an unset one as a
// commented example: `# key: <example>  # <consumer>`. parseMeta parses YAML, so
// the commented line is ignored on read and the field round-trips as empty.
func optionalMetaLine(key, val, example, consumer string) string {
	if strings.TrimSpace(val) != "" {
		return metaLine(key, val)
	}
	return fmt.Sprintf("# %s: %s  # %s\n", key, example, consumer)
}

// yamlMarshalLine marshals a single-key map to its `key: value\n` line. yaml.v3
// marshalling a one-entry map does not fail; the guard is defensive so a future
// change here can never silently drop the value.
func yamlMarshalLine(m any) string {
	out, err := yaml.Marshal(m)
	if err != nil {
		return ""
	}
	return string(out)
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
	// Recompute the derived caching metrics from the raw token fields rather than
	// trusting the derived values in the file. attempt.md written before a derived
	// field existed (drvweb-018 added cost_avoided_input_tokens / pct_cost_avoided /
	// read_creation_ratio) carries only the raw fields, so a straight unmarshal reads
	// those derived values as their zero value — the "$0 / — on every historical
	// ticket" bug. Rebuilding from raw mirrors agent.Totals.UnmarshalJSON, which
	// likewise ignores derived fields on the wire and recomputes, so a stale or
	// absent derived value can never desync from the totals it describes.
	if m.Metrics != nil {
		recomputed := agent.Totals{
			InputTokens:         m.Metrics.InputTokens,
			OutputTokens:        m.Metrics.OutputTokens,
			CacheReadTokens:     m.Metrics.CacheReadTokens,
			CacheCreationTokens: m.Metrics.CacheCreationTokens,
		}.Metrics()
		m.Metrics = &recomputed
	}
	return m, nil
}
