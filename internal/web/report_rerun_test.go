package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/bddexec"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// serverOver builds a *Server (not just its Handler) so a test can inject the
// rerun executor stub, mirroring how alive/pyramidGit are overridden.
func serverOver(t *testing.T, root store.Root) *Server {
	t.Helper()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// postOrigin issues a same-origin POST (Origin header matching Host) so the CSRF
// guard passes — the shape a real htmx button sends.
func postOrigin(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestReportDownloadServesSelfContainedFile(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "unit", "local", "c0ffee12", "20260101T100000Z")
	h := newServerOver(t, root)

	rr := get(t, h, "/ticket/BDD-1/0001/report/download")
	if rr.Code != 200 {
		t.Fatalf("GET download = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="bdd-report-BDD-1-0001-unit-c0ffee12-`) || !strings.HasSuffix(cd, `.html"`) {
		t.Errorf("Content-Disposition = %q, want an attachment with a descriptive .html filename", cd)
	}
	doc := html.UnescapeString(rr.Body.String())
	// The download is the self-contained file: the screenshot is inlined and nothing
	// is referenced from the asset route.
	if !strings.Contains(doc, "data:image/png;base64,"+onePxPNG) {
		t.Error("downloaded report did not inline the screenshot")
	}
	if strings.Contains(doc, "/report/asset") {
		t.Error("downloaded report must be self-contained, not reference the asset route")
	}
}

func TestReportDownload404s(t *testing.T) {
	root := seedReportRoot(t) // no runs captured
	h := newServerOver(t, root)
	if rr := get(t, h, "/ticket/BDD-1/0001/report/download"); rr.Code != http.StatusNotFound {
		t.Errorf("download with no runs = %d, want 404", rr.Code)
	}
	if rr := get(t, h, "/ticket/NOPE/0001/report/download"); rr.Code != http.StatusNotFound {
		t.Errorf("download for missing attempt = %d, want 404", rr.Code)
	}
}

func TestReportRerunGreenRefreshesReport(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "integration", "ci", "abc12345", "20260101T090000Z")
	s := serverOver(t, root)
	// Stub the executor: a green rerun captures a fresh side-by-side regeneration
	// (same rung/env/commit, newer runstamp) and reports StatusOK.
	s.rerun = func(ctx context.Context, a project.Attempt) (bddexec.Outcome, error) {
		seedBDDRun(t, root, "integration", "ci", "abc12345", "20260101T120000Z")
		return bddexec.Outcome{
			Status: bddexec.StatusOK,
			Rung:   "integration",
			RunKey: "bdd/integration/ci/abc12345/20260101T120000Z",
			Lines:  []string{"Reran `integration` — green.", "Captured a fresh run."},
		}, nil
	}
	h := s.Handler()

	rr := postOrigin(t, h, "/ticket/BDD-1/0001/report/rerun")
	if rr.Code != 200 {
		t.Fatalf("POST rerun = %d", rr.Code)
	}
	body := rr.Body.String()
	// The OOB result banner carries the outcome, status ok.
	if !strings.Contains(body, `data-testid="rerun-result"`) || !strings.Contains(body, `data-status="ok"`) {
		t.Errorf("response missing ok rerun-result banner:\n%s", body)
	}
	// The report panel was refreshed out-of-band (slot swap) and now shows BOTH
	// regenerations — the new side-by-side run appeared without a full reload.
	if !strings.Contains(body, `id="bdd-report-slot" hx-swap-oob="true"`) {
		t.Error("green rerun did not refresh the report panel out-of-band")
	}
	if n := strings.Count(body, "data-standalone="); n != 2 {
		t.Errorf("refreshed panel shows %d runs, want 2 (side-by-side)", n)
	}
}

func TestReportRerunBlockedPassesTheBall(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "integration", "ci", "abc12345", "20260101T090000Z")
	s := serverOver(t, root)
	// Stub a red-healthcheck block: no run captured, the "pass the ball" message.
	s.rerun = func(ctx context.Context, a project.Attempt) (bddexec.Outcome, error) {
		return bddexec.Outcome{
			Status: bddexec.StatusBlocked,
			Lines:  []string{"Healthcheck \"ready\" is red — rerun blocked.", "Pass the ball back rather than capture a bogus report."},
		}, nil
	}
	h := s.Handler()

	rr := postOrigin(t, h, "/ticket/BDD-1/0001/report/rerun")
	if rr.Code != 200 {
		t.Fatalf("POST rerun = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-status="blocked"`) {
		t.Errorf("blocked rerun did not report a blocked banner:\n%s", body)
	}
	if !strings.Contains(body, "Pass the ball") {
		t.Error("blocked banner does not explain / pass the ball")
	}
	// A block produces no new run, so the report panel must NOT be swapped.
	if strings.Contains(body, `id="bdd-report-slot" hx-swap-oob="true"`) {
		t.Error("a blocked rerun must not refresh the report panel (no new evidence)")
	}
}

func TestReportRerunCrossOriginRefused(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "integration", "ci", "abc12345", "20260101T090000Z")
	s := serverOver(t, root)
	called := false
	s.rerun = func(ctx context.Context, a project.Attempt) (bddexec.Outcome, error) {
		called = true
		return bddexec.Outcome{}, nil
	}
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/ticket/BDD-1/0001/report/rerun", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin rerun = %d, want 403", rr.Code)
	}
	if called {
		t.Error("executor was invoked despite the cross-origin refusal")
	}
}

// TestAttemptPageShowsRerunAndDownloadAffordances wires a real worktree with a BDD
// rung so CanRerun resolves true, and asserts the page carries both drv-019
// affordances next to the drv-018 report.
func TestAttemptPageShowsRerunAndDownloadAffordances(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "integration", "ci", "abc12345", "20260101T090000Z")

	// Point the attempt at a worktree carrying a BDD pyramid rung (CanRerun gate).
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, ".test-pyramid.yaml"), []byte(greenWebPyramid), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAttemptMeta(t, root, "BDD-1", "0001", wd, "main")

	h := newServerOver(t, root)
	rr := get(t, h, "/ticket/BDD-1/0001")
	if rr.Code != 200 {
		t.Fatalf("GET attempt = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="bdd-report-download"`) {
		t.Error("download affordance missing from the report panel")
	}
	if !strings.Contains(body, `data-testid="bdd-rerun"`) {
		t.Error("rerun affordance missing despite a bound BDD rung")
	}
	if !strings.Contains(body, "/ticket/BDD-1/0001/report/rerun") {
		t.Error("rerun button does not post to the rerun route")
	}
}

const greenWebPyramid = `environments:
  - name: ci
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: integration
    run: "true"
    environment: ci
    cucumber_json: cucumber.json
`
