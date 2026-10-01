package web

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
	Groups        []reportGroupVM
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
	for _, g := range report.GroupRuns(runs) {
		gvm := reportGroupVM{Label: g.Rung + " · " + g.Env + " · " + shortCommit(g.Commit)}
		for _, run := range g.Runs {
			gvm.Runs = append(gvm.Runs, reportRunOptVM{
				ID:         run.ID(),
				Label:      run.Runstamp,
				Src:        reportURL(id, att, run.ID(), false),
				Standalone: reportURL(id, att, run.ID(), true),
				Selected:   run.ID() == selected,
			})
		}
		vm.Groups = append(vm.Groups, gvm)
	}
	return vm, nil
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
