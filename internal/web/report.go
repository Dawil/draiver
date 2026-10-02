package web

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Dawil/draiver/internal/bddexec"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/report"
)

// This file adds the drv-018 BDD-report surfaces to the board: the attempt page
// embeds the latest captured run's cucumber report inline (with a selector among
// the side-by-side regenerations drv-017 stores), and two GET routes serve the
// report from the artefact store:
//
//   GET /ticket/{id}/{attempt}/report        → the rendered HTML report for a run
//   GET /ticket/{id}/{attempt}/report/asset  → one embedding's bytes (by-reference)
//
// The inline embed loads the report with inline=0, so its heavy screenshots are
// fetched lazily from the asset route rather than inlined into the attempt page
// (drv-017's store-growth note). The standalone view (inline unset) renders a
// single self-contained file with every screenshot base64-inlined — what drv-019
// will offer as a download. The report runner-agnostically parses standard
// cucumber-JSON (internal/report), so it needs no coupling to any one runner.

// reportPanelVM drives the "bdd-report" section on the attempt page. It is built
// on every full detail render; HasRuns is false (and the section hidden) until
// drv-017's capture has written at least one run for the attempt.
type reportPanelVM struct {
	HasRuns bool
	// IframeSrc is the by-reference report URL for the selected (latest) run shown
	// in the embedded iframe.
	IframeSrc string
	// StandaloneSrc is the self-contained report URL for the selected run (the file
	// drv-019 will download).
	StandaloneSrc string
	// DownloadSrc is the download URL for the selected run — the same self-contained
	// file served as an attachment (Content-Disposition) so the browser saves it.
	DownloadSrc string
	// CanRerun is true when the attempt's pyramid exposes a BDD rung to re-execute,
	// so the rerun affordance renders. False hides the button (nothing to rerun).
	CanRerun bool
	// RerunSrc is the POST URL of the rerun action for this attempt.
	RerunSrc string
	Groups   []reportGroupVM
}

// reportGroupVM is one regeneration group in the run selector: runs sharing
// rung/env/commit, offered as an <optgroup>.
type reportGroupVM struct {
	Label string
	Runs  []reportRunOptVM
}

// reportRunOptVM is one selectable run (one <option>): its by-reference report URL
// (Src, the iframe source) and self-contained URL (Standalone, the open link).
type reportRunOptVM struct {
	ID         string
	Label      string
	Src        string
	Standalone string
	Download   string
	Selected   bool
}

// reportPanel builds the attempt page's BDD-report view model by discovering the
// runs captured under the attempt's artefact store. A missing capture tree yields
// HasRuns=false, not an error.
func (s *Server) reportPanel(id, att string) (*reportPanelVM, error) {
	runs, err := report.DiscoverRuns(s.root, id, att)
	if err != nil {
		return nil, err
	}
	vm := &reportPanelVM{HasRuns: len(runs) > 0}
	if !vm.HasRuns {
		return vm, nil
	}
	selected := runs[0].ID()
	vm.IframeSrc = reportURL(id, att, selected, false)
	vm.StandaloneSrc = reportURL(id, att, selected, true)
	vm.DownloadSrc = downloadURL(id, att, selected)
	vm.CanRerun = s.attemptHasBDDRung(id, att)
	vm.RerunSrc = "/ticket/" + url.PathEscape(id) + "/" + url.PathEscape(att) + "/report/rerun"
	for _, g := range report.GroupRuns(runs) {
		gvm := reportGroupVM{Label: g.Rung + " · " + g.Env + " · " + shortCommit(g.Commit)}
		for _, run := range g.Runs {
			gvm.Runs = append(gvm.Runs, reportRunOptVM{
				ID:         run.ID(),
				Label:      run.Runstamp,
				Src:        reportURL(id, att, run.ID(), false),
				Standalone: reportURL(id, att, run.ID(), true),
				Download:   downloadURL(id, att, run.ID()),
				Selected:   run.ID() == selected,
			})
		}
		vm.Groups = append(vm.Groups, gvm)
	}
	return vm, nil
}

// attemptHasBDDRung reports whether the attempt's worktree carries a pyramid with
// a BDD rung to re-execute (drv-019 rerun). It is best-effort and never an error:
// a missing attempt/worktree/pyramid simply means "nothing to rerun" so the panel
// hides the button rather than failing the whole detail render.
func (s *Server) attemptHasBDDRung(id, att string) bool {
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil || strings.TrimSpace(a.Repo) == "" {
		return false
	}
	_, _, ok, err := bddexec.BoundBDDRung(a.Repo)
	return err == nil && ok
}

func shortCommit(c string) string {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}

// reportURL builds the report route URL for a run. inline=true renders the
// self-contained standalone file; inline=false references embeddings via the
// asset route so the inline embed stays light.
func reportURL(id, att, runID string, inline bool) string {
	q := url.Values{}
	q.Set("run", runID)
	if !inline {
		q.Set("inline", "0")
	}
	return "/ticket/" + url.PathEscape(id) + "/" + url.PathEscape(att) + "/report?" + q.Encode()
}

// downloadURL builds the download route URL for a run — the self-contained
// standalone report served as an attachment.
func downloadURL(id, att, runID string) string {
	q := url.Values{}
	q.Set("run", runID)
	return "/ticket/" + url.PathEscape(id) + "/" + url.PathEscape(att) + "/report/download?" + q.Encode()
}

// handleReport serves the rendered HTML report for a run (GET
// /ticket/{id}/{attempt}/report). ?run=<rel> selects a run; absent, the latest is
// used. Without ?inline=0 the report is fully self-contained (every screenshot
// base64-inlined) so it opens offline — the standalone file. With inline=0 the
// embeddings are referenced from the asset route, keeping the embedded iframe
// light.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	run, ok, err := s.resolveRun(id, att, r.URL.Query().Get("run"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	rep, err := run.LoadReport()
	if err != nil {
		s.fail(w, err)
		return
	}
	opts := report.Options{Title: id + " / " + att + " — " + run.Rung + " @ " + run.ShortCommit()}
	if r.URL.Query().Get("inline") == "0" {
		opts.Resolve = func(ref report.EmbeddingRef) string {
			return assetURL(id, att, run.ID(), ref)
		}
	}
	html, err := report.Render(rep, opts)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(html)
}

// handleReportDownload serves a run's standalone report as a file download (GET
// /ticket/{id}/{attempt}/report/download) — drv-019 deliverable #1. It renders the
// same fully self-contained document handleReport serves without ?inline=0 (every
// screenshot base64-inlined, so it opens offline), but attaches a
// Content-Disposition so the browser saves it under a descriptive filename rather
// than rendering it in place. ?run selects a run; absent, the latest is used.
func (s *Server) handleReportDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	run, ok, err := s.resolveRun(id, att, r.URL.Query().Get("run"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	rep, err := run.LoadReport()
	if err != nil {
		s.fail(w, err)
		return
	}
	// No Resolve hook → fully self-contained, exactly like the standalone view.
	opts := report.Options{Title: id + " / " + att + " — " + run.Rung + " @ " + run.ShortCommit()}
	html, err := report.Render(rep, opts)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+downloadFilename(id, att, run)+"\"")
	w.Write(html)
}

// downloadFilename builds a safe, descriptive filename for a downloaded report:
// bdd-report-<id>-<att>-<rung>-<shortcommit>-<runstamp>.html, with every segment
// sanitized to a conservative filename-safe set so a crafted rung/commit cannot
// break out of the Content-Disposition value.
func downloadFilename(id, att string, run report.Run) string {
	parts := []string{"bdd-report", id, att, run.Rung, run.ShortCommit(), run.Runstamp}
	for i, p := range parts {
		parts[i] = sanitizeFilenameSegment(p)
	}
	return strings.Join(parts, "-") + ".html"
}

// sanitizeFilenameSegment reduces a segment to [A-Za-z0-9._-], collapsing any
// other rune to '-', and never yields an empty segment.
func sanitizeFilenameSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}

// assetURL builds the asset-route URL addressing one embedding within a run.
func assetURL(id, att, runID string, ref report.EmbeddingRef) string {
	q := url.Values{}
	q.Set("run", runID)
	q.Set("f", strconv.Itoa(ref.Feature))
	q.Set("e", strconv.Itoa(ref.Element))
	q.Set("p", ref.Phase)
	q.Set("s", strconv.Itoa(ref.Step))
	q.Set("i", strconv.Itoa(ref.Index))
	return "/ticket/" + url.PathEscape(id) + "/" + url.PathEscape(att) + "/report/asset?" + q.Encode()
}

// handleReportAsset serves one embedding's decoded bytes from a run's
// cucumber-JSON (GET /ticket/{id}/{attempt}/report/asset). It is the by-reference
// source for the inline embed's screenshots. The Content-Type is forced to a safe
// allow-list (raster images / plain text / octet-stream): a cucumber-JSON is
// untrusted test output, so an attachment claiming text/html or image/svg+xml must
// never be served as active content on the board's own origin.
func (s *Server) handleReportAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	run, ok, err := report.FindRun(s.root, id, att, q.Get("run"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	rep, err := run.LoadReport()
	if err != nil {
		s.fail(w, err)
		return
	}
	emb, ok := locateEmbedding(rep, q)
	if !ok {
		http.NotFound(w, r)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(emb.Data))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct, attach := safeContentType(emb.MimeType)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if attach {
		w.Header().Set("Content-Disposition", "attachment")
	}
	w.Write(raw)
}

// resolveRun picks the run named by relID, or the latest when relID is empty.
func (s *Server) resolveRun(id, att, relID string) (report.Run, bool, error) {
	if relID == "" {
		return report.LatestRun(s.root, id, att)
	}
	return report.FindRun(s.root, id, att, relID)
}

// locateEmbedding navigates a report to the embedding addressed by the f/e/p/s/i
// query params, bounds-checking every index so a crafted URL can only 404.
func locateEmbedding(rep *report.Report, q url.Values) (report.Embedding, bool) {
	fi, ok1 := atoiOK(q.Get("f"))
	ei, ok2 := atoiOK(q.Get("e"))
	si, ok3 := atoiOK(q.Get("s"))
	ii, ok4 := atoiOK(q.Get("i"))
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return report.Embedding{}, false
	}
	if fi < 0 || fi >= len(rep.Features) {
		return report.Embedding{}, false
	}
	f := rep.Features[fi]
	if ei < 0 || ei >= len(f.Elements) {
		return report.Embedding{}, false
	}
	el := f.Elements[ei]
	var embs []report.Embedding
	switch q.Get("p") {
	case "before":
		if si < 0 || si >= len(el.Before) {
			return report.Embedding{}, false
		}
		embs = el.Before[si].Embeddings
	case "after":
		if si < 0 || si >= len(el.After) {
			return report.Embedding{}, false
		}
		embs = el.After[si].Embeddings
	case "step":
		if si < 0 || si >= len(el.Steps) {
			return report.Embedding{}, false
		}
		embs = el.Steps[si].Embeddings
	default:
		return report.Embedding{}, false
	}
	if ii < 0 || ii >= len(embs) {
		return report.Embedding{}, false
	}
	return embs[ii], true
}

func atoiOK(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// rerunResultVM drives the OOB "rerun-result" banner. Status is the executor's
// verdict (ok/blocked/env-fault/run-failed/no-bdd-rung); Class maps it to a styling
// hook; Message is the joined, human-readable report lines.
type rerunResultVM struct {
	Status  string
	Class   string
	Message string
}

// handleReportRerun re-executes the attempt's bound BDD rung from the board (POST
// /ticket/{id}/{attempt}/report/rerun) — drv-019 deliverable #2. It mirrors the
// drv-013 git-control seam (handleGitVerb): a same-origin guard, an attempt-exists
// check, an in-process delegate (here s.rerun → internal/bddexec, never the BDD
// tooling in the handler), and a one-body double swap — the re-rendered live
// fragment plus an OOB result banner. When the rerun captured a new run it also
// refreshes the embedded report panel out-of-band, so the fresh side-by-side
// appears without a full reload.
//
// The healthcheck precondition is enforced by the executor, not here: a red
// environment yields StatusBlocked and the "pass the ball" banner rather than a
// bogus report. This action is non-terminal — it produces evidence and never flips
// the Review gate (the gate note), exactly like push/sync.
func (s *Server) handleReportRerun(w http.ResponseWriter, r *http.Request) {
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
	outcome, err := s.rerun(r.Context(), a)
	if err != nil {
		s.fail(w, err)
		return
	}

	vm, err := s.detail(id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	live, err := s.executeLive(vm)
	if err != nil {
		s.fail(w, err)
		return
	}
	var buf bytes.Buffer
	buf.Write(live)
	// A captured run means the report changed — refresh the panel OOB. Blocked / env
	// fault / no-rung outcomes produce no run, so the panel is left untouched.
	if outcome.RunKey != "" {
		if err := s.tmpl.ExecuteTemplate(&buf, "bdd-report-slot", vm.Report); err != nil {
			s.fail(w, err)
			return
		}
	}
	banner := rerunResultVM{
		Status:  string(outcome.Status),
		Class:   rerunClass(outcome.Status),
		Message: strings.Join(outcome.Lines, "\n"),
	}
	if err := s.tmpl.ExecuteTemplate(&buf, "rerun-result", banner); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// rerunClass maps an executor status to a banner styling hook: a captured green run
// is "ok", a red healthcheck or missing rung is a neutral "warn" (the ball is back
// with the human, not a crash), and an env/run fault is "bad".
func rerunClass(st bddexec.Status) string {
	switch st {
	case bddexec.StatusOK:
		return "ok"
	case bddexec.StatusBlocked, bddexec.StatusNoRung:
		return "warn"
	default:
		return "bad"
	}
}

// safeContentType maps an embedding's declared MIME type to a Content-Type safe to
// serve same-origin. Raster images and plain text pass through; everything else —
// notably text/html and image/svg+xml, which can execute script when navigated to
// directly — is served as a downloadable octet-stream.
func safeContentType(mime string) (contentType string, attachment bool) {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/bmp":
		return mime, false
	case "text/plain", "application/json", "application/xml", "text/xml":
		return "text/plain; charset=utf-8", false
	default:
		return "application/octet-stream", true
	}
}
