package web

import (
	"net/http"
	"strings"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/systemlog"
)

// handleProvenance sets an attempt's repo/base from the detail page's provenance
// panel (POST /ticket/{id}/{attempt}/provenance) — the one UI affordance for the
// merge/sync-gating fields, so a human can unblock an attempt wedged for want of a
// base without leaving the board. It calls the in-process gateway
// (s.gw.SetProvenance, drv-008 — the same write the CLI's `draiver attempt set`
// runs, drvctl-028), which owns the attempt.md write and the field validation; the
// web layer never touches attempt.md itself (the package's write invariant). The panel prefills
// the current values, so a blank input means "leave unchanged": a blank field is
// omitted from the shell-out, and an all-blank submit is a 400 here rather than a
// 500 from the verb's "nothing to set". Only repo and base are read from the form
// — the tool/model fields `attempt set` also accepts are deliberately out of the
// UI's scope, the allow-list discipline the compose box uses for event types. On
// success it re-renders the panel in place (htmx outerHTML swap) with the saved
// values and a confirmation.
func (s *Server) handleProvenance(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	// Only repo/base are accepted; a blank field maps to "no change" (an omitted
	// Provenance field is left untouched), so the gateway receives only the fields
	// the human actually filled. An all-blank submit changes nothing — reject it as
	// a bad request here rather than let the gateway's "nothing to set" surface as a
	// 500.
	repoVal := strings.TrimSpace(r.FormValue("repo"))
	baseVal := strings.TrimSpace(r.FormValue("base"))
	if repoVal == "" && baseVal == "" {
		http.Error(w, "draiver: set a repo or a base to save", http.StatusBadRequest)
		return
	}
	// A blank field maps to "no change" (a nil Provenance pointer); the panel has
	// already rejected the all-blank submit. Base is passed as typed — the webui
	// never triggers the empty-base→current-branch defaulting the CLI's `attempt
	// set` does (that path is git-side and only fires on an omitted flag).
	var p repo.Provenance
	if repoVal != "" {
		p.Repo = &repoVal
	}
	if baseVal != "" {
		p.Base = &baseVal
	}
	// Capture the pre-edit values (best-effort) so the system-log line can show the
	// from→to the spec asks for; a load failure just omits the "from" side.
	var oldRepo, oldBase string
	if prev, err := project.LoadAttempt(s.root, id, att); err == nil {
		oldRepo, oldBase = prev.Repo, prev.Base
	}
	if _, err := s.gw.SetProvenance(id, att, p); err != nil {
		s.fail(w, err)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// drv-045: record the provenance edit as a system event (the operational view;
	// the gateway owns the durable attempt.md write). One line naming the field(s)
	// changed and, where available, old→new.
	s.emitSystem(id, att, provenanceSystemEvent(repoVal, baseVal, oldRepo, oldBase))
	s.render(w, "provenance-panel", provenanceVM{Attempt: a, Saved: true})
}

// provenanceSystemEvent composes the drv-045 system-log record for a provenance
// edit: a ClassProvenance event whose message names each field the submit changed
// with its old→new (an unchanged or newly-set-from-blank field reads naturally), and
// whose Fields carry the structured repo/base from→to for a machine reader. Only the
// fields actually submitted (non-blank) are reported — a blank input means "leave
// unchanged" and is omitted here exactly as it is omitted from the gateway write.
func provenanceSystemEvent(newRepo, newBase, oldRepo, oldBase string) systemlog.SystemEvent {
	fields := map[string]string{}
	var parts []string
	if newRepo != "" {
		parts = append(parts, "repo "+fromTo(oldRepo, newRepo))
		fields["repo_from"], fields["repo_to"] = oldRepo, newRepo
	}
	if newBase != "" {
		parts = append(parts, "base "+fromTo(oldBase, newBase))
		fields["base_from"], fields["base_to"] = oldBase, newBase
	}
	return systemlog.SystemEvent{
		Class:   systemlog.ClassProvenance,
		Message: "provenance updated: " + strings.Join(parts, ", "),
		Fields:  fields,
	}
}

// fromTo renders an old→new transition for a system-log message, reading naturally
// when the old side was blank (a first-time set).
func fromTo(old, new string) string {
	if strings.TrimSpace(old) == "" {
		return "set to `" + new + "`"
	}
	return "`" + old + "` → `" + new + "`"
}

// handleRepoSettings sets a repo's per-repo defaults from the detail page's
// repo-settings panel (POST /ticket/{id}/{attempt}/repo-settings) — drv-011's write
// half. Unlike handleProvenance (which edits ONE attempt's attempt.md), this edits
// the global config.json keyed by the attempt's repo, so the change applies to every
// attempt on that repo — the broader blast radius the panel copy makes explicit. It
// writes through the in-process gateway (s.gw.SetRepoSettings), the same merging
// atomic writer the `draiver config repo` verb calls; the web layer never pokes
// config.json directly, the same invariant that keeps attempt.md behind SetProvenance.
//
// The panel prefills the stored values, so a blank input means "leave unchanged": a
// blank field is sent as an empty value but only applied when it differs from the
// stored one, so a no-op blur never clears a set key. A submit that changes nothing
// (every field posting its prefill back, or an all-blank submit on a repo with
// nothing stored) writes nothing and re-renders the panel without the Saved flash —
// not an error. A missing repo (no key to write under) is a 400. On a change it
// re-renders the panel in place (htmx outerHTML swap) with a flash.
func (s *Server) handleRepoSettings(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	if a.Repo == "" {
		http.Error(w, "draiver: this attempt records no repo, so there is nothing to key per-repo settings on", http.StatusBadRequest)
		return
	}
	// Prefill-aware diffing: the form always posts all three fields (prefilled from the
	// stored entry). To honour "blank = leave unchanged" while still allowing a
	// deliberate clear, apply a field only when its submitted value differs from what is
	// stored — so an untouched field (posting its prefill back) is a no-op, and clearing
	// a set field (prefill "x" → submitted "") writes the empty string. This reads the
	// stored entry once, through the config the gateway also writes.
	stored := config.RepoSettings{}
	storedOverlayDir := ""
	if cfg, cerr := config.Load(""); cerr == nil {
		stored = cfg.Repos[a.Repo]
		storedOverlayDir = cfg.OverlayDir
	}
	remote := strings.TrimSpace(r.FormValue("remote"))
	branch := strings.TrimSpace(r.FormValue("branch"))
	rung := strings.TrimSpace(r.FormValue("rung"))
	reviewEnv := strings.TrimSpace(r.FormValue("review_env"))
	overlayLoc := strings.TrimSpace(r.FormValue("overlay"))
	var upd config.RepoSettingsUpdate
	if remote != stored.DefaultRemote {
		upd.DefaultRemote = &remote
	}
	if branch != stored.DefaultBranch {
		upd.DefaultBranch = &branch
	}
	if rung != stored.DefaultTestRung {
		upd.DefaultTestRung = &rung
	}
	if reviewEnv != stored.ReviewEnvironment {
		upd.ReviewEnvironment = &reviewEnv
	}
	// The explicit per-codebase overlay (drv-041) is a twin of review_env: a *string in
	// the same partial update, written only when the submitted value differs from stored.
	if overlayLoc != stored.Overlay {
		upd.Overlay = &overlayLoc
	}
	changed, err := s.gw.SetRepoSettings(a.Repo, upd)
	if err != nil {
		s.fail(w, err)
		return
	}
	// overlay_dir is GLOBAL, not per-repo, so it goes through the dedicated global writer
	// (drv-041) rather than the per-repo update — but it is edited from the same panel
	// (there is no separate global-settings page), applied with the same "blank = leave,
	// changed = write" diffing. A change to either the per-repo or the global field
	// flashes Saved.
	overlayDir := strings.TrimSpace(r.FormValue("overlay_dir"))
	if overlayDir != storedOverlayDir {
		dirChanged, derr := s.gw.SetOverlayDir(overlayDir)
		if derr != nil {
			s.fail(w, derr)
			return
		}
		changed = changed || dirChanged
	}
	// Re-read the attempt so the swapped-in panel reflects the persisted values; Saved
	// confirms the write only when something actually changed (a pure no-op submit
	// still re-renders, just without the flash).
	a, err = project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm := s.repoSettingsPanel(a)
	vm.Saved = changed
	s.render(w, "repo-settings-panel", vm)
}

// handleSupervision sets a Capability's coordinator supervision mode from the
// capability panel's dial (POST /ticket/{id}/{attempt}/supervision) — the write
// half of drvweb-017. It writes through the in-process gateway (s.gw.Supervision,
// drv-008) — the same library op the `draiver ctl supervision` verb calls
// (drvctl-042) — which owns the `supervision` log-event append; the web layer never
// appends the event itself (the package's write invariant, matching handleEnable). The
// write is gated on the ticket being a Capability (it carries `wants:` edges) — the
// same gate the panel renders behind, so the affordance and the effect agree; a
// POST to a non-Capability is a 400 before any write. The mode is re-validated here
// so a bad value is a 400 rather than a 500 from the verb. On success it re-renders
// the panel in place (htmx outerHTML swap on #capability) with the persisted mode
// selected and a confirmation, so the change is reflected without a reload.
func (s *Server) handleSupervision(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	// Gate on capability-ness before writing: the dial only renders for a Capability,
	// so a POST for one that carries no `wants:` is a bad request, not a 500 later.
	edges, err := project.LoadEdges(s.root, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(edges.Wants) == 0 {
		http.Error(w, "draiver: "+id+" is not a Capability (no wants:)", http.StatusBadRequest)
		return
	}
	// Validate the mode at the edge so a typo/forged value is a 400 here rather than
	// surfacing as a 500 from the shelled verb.
	mode, err := project.ParseSupervision(r.FormValue("mode"))
	if err != nil {
		http.Error(w, "draiver: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.gw.Supervision(id, att, mode); err != nil {
		s.fail(w, err)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm, err := s.capability(a)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm.Saved = true
	s.render(w, "capability-panel", vm)
}

// handleEdges rewrites a ticket's dependency edges from the attempts-index page's
// edge editor (POST /ticket/{id}/edges) — the ticket-level surface, since edges
// live in spec.md and are shared across attempts (drvweb-016). It calls the
// in-process gateway (s.gw.SetEdges, drv-008), which owns the spec.md frontmatter
// write and the self-edge/cycle refusal; the web layer never writes spec.md
// itself. The
// editor is WYSIWYG: all three relations are posted every save, each free-text
// field parsed into an id set, so an emptied field clears that relation. A cycle
// or self-edge is surfaced inline — the panel re-renders (200) with the attempted
// values and the refusal reason, and because the verb refuses before writing,
// nothing changed on disk. On success it re-renders the panel with a confirmation.
func (s *Server) handleEdges(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if !s.root.Exists(id) {
		http.NotFound(w, r)
		return
	}
	want := project.Edges{
		Wants:    parseEdgeField(r.FormValue("wants")),
		After:    parseEdgeField(r.FormValue("after")),
		Requires: parseEdgeField(r.FormValue("requires")),
	}
	if err := s.gw.SetEdges(id, want); err != nil {
		// A refusal (self-edge/cycle) is user-facing, not a server fault: re-render
		// the panel in place with the values the human tried and the reason, so the
		// edit is not lost and the refusal reads inline. The verb wrote nothing.
		vm := edgesVM{
			Ticket:   id,
			Wants:    strings.Join(want.Wants, ", "),
			After:    strings.Join(want.After, ", "),
			Requires: strings.Join(want.Requires, ", "),
			Error:    refusalMessage(err),
		}
		s.render(w, "edges-panel", vm)
		return
	}
	e, err := project.LoadEdges(s.root, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm := edgesPanel(id, e)
	vm.Saved = true
	s.render(w, "edges-panel", vm)
}
