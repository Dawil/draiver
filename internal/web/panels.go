package web

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/project"
)

// capabilityVM drives the "capability-panel" partial (drvweb-017): a Capability's
// `wants:` sub-fleet at a glance plus the supervision dial. It is built only for a
// Capability attempt (its ticket carries `wants:` edges); an ordinary attempt gets
// a nil Capability and no panel. Attempt supplies the POST target (Ticket/ID);
// Children are the sub-fleet rows; the dial fields (Mode/Default/Overridden/Modes)
// project the per-ticket supervision override folded over the config default. Saved
// is true only in the successful-POST response so the panel can confirm the write
// after an htmx swap (the full-page render leaves it false).
type capabilityVM struct {
	Attempt  project.Attempt
	Children []capChildVM
	// Mode is the *effective* mode shown selected — the per-ticket override folded
	// over the config default (project.Effective), always a concrete mode.
	Mode project.Supervision
	// Default is the config default the override falls back to; Overridden is true
	// when a per-ticket `supervision` event set the mode (so the panel can say it is
	// inheriting the default when it is not).
	Default    project.Supervision
	Overridden bool
	Modes      []capModeVM
	Saved      bool

	// WakeChild and QueueDepth are the manager's OWN current action (drvweb-023): the
	// child its latest in-flight `woken-by` activation names and the doorbells queued
	// behind it. The panel surfaces the coordinator's own action line, not only the
	// children's states — the same CoordinatorSignal the board card reads. WakeChild
	// is empty when the manager is not currently woken.
	WakeChild  string
	QueueDepth int
}

// capChildVM is one row of a Capability's sub-fleet: a `wants:` child with its
// latest attempt's control state and, when that state is Pending, the gate reason
// holding it (WaitingReason). HasAttempt is false for a wanted ticket with no
// attempt yet (the daemon would mint one) — the row shows a quiet placeholder
// rather than a state badge.
type capChildVM struct {
	Ticket        string
	Href          string
	HasAttempt    bool
	AttemptID     string
	State         project.State
	StateLabel    string
	WaitingReason string // only when Pending; the "waiting on X" gate reason
}

// capModeVM is one option of the supervision dial: a concrete mode with its
// human-facing label and one-line description, and whether it is the current
// (effective) selection.
type capModeVM struct {
	Value    project.Supervision
	Label    string
	Desc     string
	Selected bool
}

// supervisionModeVMs projects the two shipped supervision modes into dial options,
// marking the one matching the effective mode selected. The order is floor→forward
// (passthrough then pre-digest), matching the design's dial; the deferred
// auto-execute is intentionally absent (project.ParseSupervision rejects it).
func supervisionModeVMs(selected project.Supervision) []capModeVM {
	defs := []struct {
		v           project.Supervision
		label, desc string
	}{
		{project.SupervisionPassthrough, "Passthrough",
			"A sub-ticket escalation goes straight to Needs-me; no coordinator wakes."},
		{project.SupervisionPreDigest, "Pre-digest",
			"A sub-ticket escalation wakes the coordinator to assess across children and post one consolidated recommendation."},
	}
	out := make([]capModeVM, 0, len(defs))
	for _, d := range defs {
		out = append(out, capModeVM{Value: d.v, Label: d.label, Desc: d.desc, Selected: d.v == selected})
	}
	return out
}

// capability builds the capability panel view model for an attempt, or nil when
// the attempt's ticket is not a Capability (no `wants:` edges) — the same gate the
// panel renders behind and handleSupervision writes behind, so the affordance and
// the effect agree (mirroring canEnable/canArchive). For a Capability it lists each
// `wants:` child with its latest attempt's control state (Pending folded in via
// DeriveDesired + the live probe, exactly as board() does) and, when Pending, the
// gate reason (WaitingReason); and it resolves the supervision dial's selection to
// the effective mode (the attempt's per-ticket override folded over the config
// default). It loads the whole fleet + edge set once, like board().
func (s *Server) capability(a project.Attempt) (*capabilityVM, error) {
	edges, err := project.LoadEdges(s.root, a.Ticket)
	if err != nil {
		return nil, err
	}
	if len(edges.Wants) == 0 {
		return nil, nil // not a Capability — no panel
	}
	all, err := project.LoadAll(s.root)
	if err != nil {
		return nil, err
	}
	allEdges, err := project.LoadAllEdges(s.root)
	if err != nil {
		return nil, err
	}
	desired := project.DeriveDesired(all, allEdges)
	// Latest attempt per ticket: LoadAll is sorted by ticket then attempt id, so the
	// last entry seen for a ticket is its latest attempt (the one transitive enable /
	// a satisfied gate would target — docs §Attempt admission).
	latest := map[string]project.Attempt{}
	for _, at := range all {
		latest[at.Ticket] = at
	}

	vm := &capabilityVM{Attempt: a}
	// The manager's own current action line (drvweb-023): its latest in-flight
	// activation's child + the doorbells queued behind it, derived from the same logs
	// the board card reads — one source, shown on both surfaces.
	sig := project.DeriveCoordinatorSignal(a, all, allEdges)
	vm.WakeChild = sig.WakeChild
	vm.QueueDepth = sig.QueueDepth
	for _, child := range edges.Wants {
		row := capChildVM{Ticket: child, Href: "/ticket/" + child}
		if ca, ok := latest[child]; ok {
			ca.Desired = desired[project.Ref{Ticket: ca.Ticket, Attempt: ca.ID}]
			ca.Live = s.attemptLive(ca)
			st := ca.Control()
			row.HasAttempt = true
			row.AttemptID = ca.ID
			row.State = st
			row.StateLabel = stateLabel(st)
			if st == project.Pending {
				row.WaitingReason = project.WaitingReason(child, all, allEdges)
			}
		}
		vm.Children = append(vm.Children, row)
	}

	override := a.Supervision
	vm.Overridden = override == project.SupervisionPassthrough || override == project.SupervisionPreDigest
	vm.Default = s.supDefault
	vm.Mode = project.Effective(override, s.supDefault)
	vm.Modes = supervisionModeVMs(vm.Mode)
	return vm, nil
}

// provenanceVM drives the "provenance-panel" partial: the repo/base editor on the
// attempt detail page. Attempt supplies the prefill values (Repo/Base) and the
// gating bits (State/Ticket/ID); Saved is true only in the response to a
// successful save, so the panel can confirm the write after an htmx swap (the
// full-page render leaves it false).
type provenanceVM struct {
	Attempt project.Attempt
	Saved   bool
}

// repoSettingsVM drives the "repo-settings-panel" partial: the per-repo defaults
// editor sitting immediately right of provenance on the attempt detail page
// (drv-011). It mirrors provenanceVM's mechanics (inline inputs, post-on-change,
// outerHTML swap, a Saved flash) but edits a DIFFERENT store with a BROADER scope:
// provenance writes this one attempt's attempt.md, this writes the global
// config.json's per-repo section, so every attempt on Repo reads the change. Repo is
// the config key; HasRepo gates the whole panel (no repo recorded → nothing to key
// on, so the panel renders a disabled hint instead of an editor). Remote/Branch/Rung
// prefill from the repo's STORED entry (blank when unset, a deliberate "leave
// unchanged" on save); the *Hint fields carry the fallback that applies while a field
// is unset, surfaced as the input placeholder so the resolution chain is legible.
type repoSettingsVM struct {
	Attempt    project.Attempt
	HasRepo    bool
	Remote     string
	Branch     string
	Rung       string
	ReviewEnv  string
	RemoteHint string
	BranchHint string
	RungHint   string
	// ReviewEnvHint names the review-environment setting's unset meaning, surfaced as
	// the input placeholder so the panel reads "blank = no review env offered" (drv-020).
	ReviewEnvHint string
	// Overlay is the explicit per-codebase test-pyramid overlay repo location (drv-041);
	// OverlayDir is the GLOBAL folder of overlay repos looked up by codebase basename.
	// Both are editable here (overlay_dir is global but has no separate settings page);
	// the *Hint fields name their unset meaning as placeholders, mirroring ReviewEnv.
	Overlay        string
	OverlayHint    string
	OverlayDir     string
	OverlayDirHint string
	Saved          bool
}

// repoSettingsPanel projects an attempt into the repo-settings editor view model,
// reading the repo's stored entry and resolved fallback from the global config. It
// is best-effort presentation like sessionDot/pyramidBadge: a config read error
// yields a blank (but still editable) panel rather than failing the page render. An
// attempt with no recorded repo yields HasRepo=false — the panel shows why it is
// disabled rather than an editor keyed on nothing.
func (s *Server) repoSettingsPanel(a project.Attempt) repoSettingsVM {
	vm := repoSettingsVM{Attempt: a}
	if a.Repo == "" {
		return vm
	}
	vm.HasRepo = true
	cfg, err := config.Load("")
	if err != nil {
		return vm // blank editor; the operator can still set values
	}
	stored := cfg.Repos[a.Repo]
	vm.Remote, vm.Branch, vm.Rung = stored.DefaultRemote, stored.DefaultBranch, stored.DefaultTestRung
	vm.ReviewEnv = stored.ReviewEnvironment
	vm.Overlay = stored.Overlay
	vm.OverlayDir = cfg.OverlayDir
	res := cfg.RepoSettings(a.Repo)
	// Placeholder hints name the fallback in force while a field is unset, so the
	// panel reads "blank, but inheriting X" rather than just blank.
	if vm.Remote == "" {
		if res.Remote != "" {
			vm.RemoteHint = "inherits primary_remote: " + res.Remote
		} else {
			vm.RemoteHint = "unset — git falls back to the sole remote"
		}
	}
	if vm.Branch == "" {
		vm.BranchHint = "unset — defaults to the repo's current branch"
	}
	if vm.Rung == "" {
		vm.RungHint = "unset — `draiver test` iteration ceiling; defaults to the pyramid's highest required rung"
	}
	if vm.ReviewEnv == "" {
		vm.ReviewEnvHint = "unset — no review environment offered at Review"
	}
	// Overlay hints name the resolution chain in force while a field is unset (drv-041):
	// an explicit overlay wins; else <overlay_dir>/<basename> is tried; else no overlay.
	if vm.Overlay == "" {
		if vm.OverlayDir != "" {
			vm.OverlayHint = "unset — falls back to " + vm.OverlayDir + "/" + filepath.Base(a.Repo)
		} else {
			vm.OverlayHint = "unset — no overlay (in-repo .test-pyramid.yaml wins if present)"
		}
	}
	if vm.OverlayDir == "" {
		vm.OverlayDirHint = "global — a folder of overlay repos looked up by codebase basename"
	}
	return vm
}

// edgesVM drives the "edges-panel" partial: the dependency-edge editor on the
// attempts-index page (GET /ticket/{id}). Unlike provenanceVM this is
// ticket-level, not per-attempt — edges live in spec.md, shared across attempts.
// Each relation prefills as a comma-joined id list the human edits in place; the
// three fields are always posted together (a whole-set replace, see
// handleEdges → s.gw.SetEdges). Saved is true only on a successful-save response so the panel
// can confirm the write after an htmx swap. Error carries a self-edge or cycle
// refusal to render inline — the verb wrote nothing, so the panel re-shows the
// values the human tried and the reason they were rejected.
type edgesVM struct {
	Ticket   string
	Wants    string
	After    string
	Requires string
	Saved    bool
	Error    string
}

// edgesPanel projects a ticket's authored edges into the editor view model,
// joining each relation's id set into the single comma-separated string the
// inline input prefills from.
func edgesPanel(ticket string, e project.Edges) edgesVM {
	return edgesVM{
		Ticket:   ticket,
		Wants:    strings.Join(e.Wants, ", "),
		After:    strings.Join(e.After, ", "),
		Requires: strings.Join(e.Requires, ", "),
	}
}

// parseEdgeField splits one relation's free-text input (the human types
// comma- or whitespace-separated ids) into a cleaned id list: trimmed, blanks
// dropped, first-seen order preserved. It is deliberately lenient about the
// separator so "a, b" and "a b\nc" both parse; the CLI re-validates on write.
func parseEdgeField(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}
