// Package project derives an attempt's control state — the human's relationship
// to it — from its append-only log, and renders the generated state.md
// projection. Each attempt on a ticket has its own state.
package project

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// State is a control state: the human's relationship to the attempt, not its
// progress.
type State string

const (
	Running State = "Running"  // agent working, no action; rendered as a count
	Pending State = "Pending"  // enabled, no live agent, self-resolving wait; a count, below Needs-me
	NeedsMe State = "Needs me" // unresolved escalation — the board
	Review  State = "Review"   // agent claims done; a claim, not a fact
	Done    State = "Done"     // closed
)

// SpecMeta is the identity carried in a ticket's spec.md frontmatter.
type SpecMeta struct {
	ID       string `yaml:"id"`
	Title    string `yaml:"title"`
	Project  string `yaml:"project"`
	Team     string `yaml:"team"`
	Assignee string `yaml:"assignee"`
}

// Attempt is one attempt's identity plus its derived control state and log. It
// is the unit rendered as a board card.
type Attempt struct {
	Ticket   string
	ID       string // attempt id
	Title    string // from ticket spec
	Assignee string
	Tool     string // from attempt.md
	Model    string
	Repo     string // local git working-tree path the attempt targets (from attempt.md)
	Base     string // branch the attempt lands back into (from attempt.md; drvctl-021)

	State           State
	Enabled         bool // directly opted into daemon supervision (durable; see DeriveEnabled)
	Archived        bool // taken off the board (see DeriveArchived)
	Events          []event.Event
	OpenEscalations []event.Event // escalations with no later resolution

	// Supervision is the attempt's per-ticket coordinator-supervision override
	// (drvctl-042), SupervisionDefault when none is set — the last `supervision`
	// log event wins (see DeriveSupervision). It is the dial on reverse-`wants:`
	// activation: on a child escalation, only a coordinator whose effective mode
	// (this override folded over the config default, see Effective) is pre-digest
	// is woken. A durable, log-derived bit like Enabled.
	Supervision Supervision

	// Desired and Live are runtime bits a presentation caller fills to derive the
	// Pending control state (see Control); both default false on a log-only load, so
	// the log tier (reconciler, brief) is unaffected. Desired is effective
	// supervised-desiredness — directly enabled OR enabled-via-parent down `wants:`
	// (drvctl-038), computed by DeriveDesired — as distinct from the durable, direct
	// Enabled bit. Live is whether a runtime probe (session.Alive) found a live agent.
	Desired bool
	Live    bool

	// WaitingReason is the human-facing "waiting on X" line a board card shows for a
	// Pending attempt (drvweb-015), filled by the presentation caller from
	// WaitingReason(). Empty on a log-only load and for every non-Pending attempt, so
	// the card renders it only where it means something.
	WaitingReason string

	// Metrics is the attempt's final prompt-caching tally, folded into attempt.md
	// on retire (drvctl-031). Nil until an attempt retires with a metered session,
	// so a card renders the cache panel only once there is something to show.
	Metrics *agent.Metrics
}

// isLifecycle reports whether an event type moves the control state: the last
// such event (subject to the open-escalation override) decides the derived
// state. `decision` is a lifecycle event so that logging one against a Review
// attempt reopens it — as the latest lifecycle marker a `decision` displaces the
// prior `review`, and since it is neither `done` nor `review` Derive falls
// through to Running. That is the first-class Review → Running transition: a
// human (or a resumed agent) revising a "done" claim writes a decision whose
// text is the recorded reason, with no escalate/resolve workaround. Decisions
// logged during ordinary work keep an already-Running attempt Running, so the
// rule is invisible except when it reopens.
func isLifecycle(t string) bool {
	switch t {
	case "escalation", "review", "decision", "done":
		return true
	}
	return false
}

// Derive computes the control state and the set of unresolved escalations from
// an attempt's events (which must be in seq order). Precedence, first match
// wins: Done > Needs me (open escalation) > Review > Running. A `decision`
// logged after a `review` becomes the latest lifecycle marker and lands in the
// default Running branch, returning a reviewed attempt to active work.
func Derive(events []event.Event) (State, []event.Event) {
	resolved := map[int]bool{}
	for _, e := range events {
		if e.Type == "resolution" {
			for _, r := range e.Refs {
				resolved[r] = true
			}
		}
	}

	var open []event.Event
	var lastLifecycle *event.Event
	for i := range events {
		e := events[i]
		if e.Type == "escalation" && !resolved[e.Seq] {
			open = append(open, e)
		}
		if isLifecycle(e.Type) {
			lastLifecycle = &events[i]
		}
	}

	switch {
	case lastLifecycle != nil && lastLifecycle.Type == "done":
		return Done, open
	case len(open) > 0:
		return NeedsMe, open
	case lastLifecycle != nil && lastLifecycle.Type == "review":
		return Review, open
	default:
		return Running, open
	}
}

// Control folds runtime desiredness + liveness into the log-derived control state
// to yield the board's effective state — the one place Pending is named. Pending
// is the sole control state the log cannot name on its own: a desired attempt
// whose log state is Running but which has no live agent is not being worked, it is
// a self-resolving wait for a gate to open or an activation to fire (zero human
// attention, below Needs-me). Every other state passes through unchanged; an
// undesired or already-live Running attempt is not Pending.
//
// `desired` is effective supervised-desiredness — directly enabled OR
// enabled-via-parent (see DeriveDesired) — not the durable, direct Enabled bit: an
// enabled-via-parent child not yet admitted must still derive Pending. `live` is
// the caller's runtime probe (session.Alive); the project tier never touches the
// process table itself.
//
// This is deliberately a read-time projection, kept OFF Attempt.State and OUT of
// Derive (the log tier stays pure — no new event, the hash chain stays clean). The
// reconciler admits precisely the log-Running desired attempts (see
// reconcile.desired); folding Pending into Attempt.State would make those very
// attempts stop looking Running and the daemon would never admit them — Pending
// would be a black hole. So the log projection keeps saying Running and only the
// board view (this function) shows Pending.
func Control(state State, desired, live bool) State {
	if state == Running && desired && !live {
		return Pending
	}
	return state
}

// Control is the attempt's effective board state, folding its runtime Desired and
// Live bits (which a presentation caller fills via DeriveDesired and session.Alive)
// into the log-derived State. See the package-level Control function.
func (a Attempt) Control() State { return Control(a.State, a.Desired, a.Live) }

// DeriveEnabled reports whether an attempt has opted into daemon supervision.
// Enablement is a separate axis from control state (State): a Running attempt is
// merely "a human could pick this up," not "the supervisor will auto-spawn an
// agent on it now." An attempt joins the supervised fleet only after an explicit
// `enable`, so the default is disabled — the last enable/disable event wins, and
// with neither present it is off. Both events live in the hash chain like every
// other, so the bit is durable and replays from the log via brief.
func DeriveEnabled(events []event.Event) bool {
	enabled := false
	for _, e := range events {
		switch e.Type {
		case "enable":
			enabled = true
		case "disable":
			enabled = false
		}
	}
	return enabled
}

// DeriveArchived reports whether an attempt has been taken off the board. Archive
// is board-membership ("is this on my board"), a separate axis from the lifecycle
// State — a finished Done attempt and an abandoned Running one are equally
// archivable, and archiving neither closes nor reopens an attempt. It mirrors
// DeriveEnabled exactly: the last archive/unarchive event wins and the default is
// un-archived, so an `unarchive` brings a card back with nothing destroyed (the log
// is immutable; only this derived bit flips). The sentiment of an archive — accepted
// (a Done card's green tick) vs abandoned (an active card's grey cross) — rides on
// the archive event's Outcome field, not on this bit: both are the same archive
// axis, distinguished only for metrics.
func DeriveArchived(events []event.Event) bool {
	archived := false
	for _, e := range events {
		switch e.Type {
		case "archive":
			archived = true
		case "unarchive":
			archived = false
		}
	}
	return archived
}

// LoadAttempt reads one attempt's log + provenance + the ticket spec identity and
// derives its state.
func LoadAttempt(root store.Root, ticket, id string) (Attempt, error) {
	events, err := ticketlog.Read(root, ticket, id)
	if err != nil {
		return Attempt{}, err
	}
	spec, err := loadSpecMeta(root.SpecPath(ticket))
	if err != nil {
		return Attempt{}, err
	}
	am, err := attempt.LoadMeta(root, ticket, id)
	if err != nil {
		return Attempt{}, err
	}
	title := spec.Title
	if title == "" {
		title = ticket
	}
	state, open := Derive(events)
	return Attempt{
		Ticket: ticket, ID: id, Title: title, Assignee: spec.Assignee,
		Tool: am.Tool, Model: am.Model, Repo: am.Repo, Base: am.Base,
		State: state, Enabled: DeriveEnabled(events), Archived: DeriveArchived(events),
		Supervision: DeriveSupervision(events),
		Events:      events, OpenEscalations: open,
		Metrics: am.Metrics,
	}, nil
}

// LoadAll loads every attempt of every ticket under the root, sorted by ticket
// then attempt id. Each returned Attempt is one board card.
func LoadAll(root store.Root) ([]Attempt, error) {
	tickets, err := root.ListTickets()
	if err != nil {
		return nil, err
	}
	var out []Attempt
	for _, ticket := range tickets {
		ids, err := root.ListAttempts(ticket)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			a, err := LoadAttempt(root, ticket, id)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
	}
	return out, nil
}

func loadSpecMeta(specPath string) (SpecMeta, error) {
	block, ok, err := specFrontmatter(specPath)
	if err != nil || !ok {
		return SpecMeta{}, err
	}
	var m SpecMeta
	if err := yaml.Unmarshal([]byte(block), &m); err != nil {
		return SpecMeta{}, fmt.Errorf("parse spec frontmatter: %w", err)
	}
	return m, nil
}

// specFrontmatter returns the YAML text between a spec.md's leading `---` fences.
// ok is false (with a nil error) when the file is missing or opens straight into
// prose with no frontmatter — the tolerant framing shared by every spec reader:
// a doc without frontmatter simply carries no metadata, it is not an error.
func specFrontmatter(specPath string) (block string, ok bool, err error) {
	data, err := os.ReadFile(specPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read spec: %w", err)
	}
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return "", false, nil
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", false, nil
	}
	return rest[:end], true, nil
}

// RenderState renders the generated per-attempt state.md projection. It is a
// projection of the log and must never be read back as truth.
func RenderState(a Attempt, now time.Time) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "ticket: %s\n", a.Ticket)
	fmt.Fprintf(&b, "attempt: %s\n", a.ID)
	fmt.Fprintf(&b, "state: %s\n", a.State)
	fmt.Fprintf(&b, "enabled: %t\n", a.Enabled)
	fmt.Fprintf(&b, "open_escalations: %d\n", len(a.OpenEscalations))
	fmt.Fprintf(&b, "events: %d\n", len(a.Events))
	fmt.Fprintf(&b, "generated: %s\n", now.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	b.WriteString("<!-- GENERATED by `draiver status` — do not edit. Projection of the log. -->\n\n")
	fmt.Fprintf(&b, "# %s / %s — %s\n\n", a.Ticket, a.ID, a.Title)
	fmt.Fprintf(&b, "**State:** %s  ·  **Supervision:** %s\n\n", a.State, enabledLabel(a.Enabled))
	if len(a.OpenEscalations) > 0 {
		b.WriteString("## Open escalations\n\n")
		for _, e := range a.OpenEscalations {
			fmt.Fprintf(&b, "- #%d: %s\n", e.Seq, firstLine(e.Body))
		}
		b.WriteString("\n")
	}
	if len(a.Events) > 0 {
		last := a.Events[len(a.Events)-1]
		fmt.Fprintf(&b, "**Last activity:** #%d %s by %s at %s\n",
			last.Seq, last.Type, last.Actor, last.TS.UTC().Format(time.RFC3339))
	}
	return []byte(b.String())
}

// enabledLabel renders the supervision axis for human projections.
func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
