package web

import (
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// onePxPNG is a real, decodable 1x1 transparent PNG (shared shape with the report
// package's own tests; duplicated here to keep the web test self-contained).
const onePxPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

const cukeJSON = `[{"uri":"features/login.feature","keyword":"Feature","name":"Login","elements":[
  {"keyword":"Scenario","name":"Happy path","type":"scenario","steps":[
    {"keyword":"Given ","name":"a user","result":{"status":"passed","duration":1000000}}]},
  {"keyword":"Scenario","name":"Bad password","type":"scenario","steps":[
    {"keyword":"When ","name":"wrong pw","result":{"status":"failed","duration":2000000,"error_message":"got 401"},
     "embeddings":[{"mime_type":"image/png","data":"` + onePxPNG + `","name":"shot.png"}]}]}]}]`

// seedReportRoot builds a one-attempt root with a given set of BDD runs captured
// under the drv-017 per-run key.
func seedReportRoot(t *testing.T) store.Root {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("BDD-1", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("BDD-1"), []byte("---\nid: BDD-1\ntitle: bdd\n---\n# bdd\n"), 0o644)
	if _, err := ticketlog.Append(root, "BDD-1", "0001", event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	return root
}

func seedBDDRun(t *testing.T, root store.Root, rung, env, commit, stamp string) {
	t.Helper()
	dir := filepath.Join(root.ArtefactsDir("BDD-1", "0001"), report.BDDArtefactSubdir, rung, env, commit, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cucumber.json"), []byte(cukeJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptPageHidesReportWhenNoRuns(t *testing.T) {
	h := newServerOver(t, seedReportRoot(t))
	rr := get(t, h, "/ticket/BDD-1/0001")
	if rr.Code != 200 {
		t.Fatalf("GET attempt = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), `data-testid="bdd-report"`) {
		t.Error("report section should be hidden when no runs are captured")
	}
}

func TestAttemptPageEmbedsReportAndSelector(t *testing.T) {
	root := seedReportRoot(t)
	// Two regenerations at one commit + one at another → selector with options.
	seedBDDRun(t, root, "integration", "ci", "abc12345", "20260101T090000Z")
	seedBDDRun(t, root, "integration", "ci", "abc12345", "20260101T120000Z")
	seedBDDRun(t, root, "integration", "ci", "def67890", "20260101T080000Z")
	h := newServerOver(t, root)

	rr := get(t, h, "/ticket/BDD-1/0001")
	if rr.Code != 200 {
		t.Fatalf("GET attempt = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="bdd-report"`) {
		t.Fatal("report section missing")
	}
	if !strings.Contains(body, `data-testid="bdd-report-frame"`) {
		t.Error("report iframe missing")
	}
	// The iframe must be sandboxed (allow-scripts only, no same-origin).
	if !strings.Contains(body, `sandbox="allow-scripts"`) {
		t.Error("report iframe is not sandboxed")
	}
	// The embed loads by-reference (inline=0), newest run selected by default. The
	// run id is a slash-bearing path, so url.Values.Encode percent-encodes its
	// slashes (%2F) — the handler decodes them back via Query().Get, so this is the
	// robust stdlib form, not a bug.
	unescaped := html.UnescapeString(body)
	if !strings.Contains(unescaped, "/ticket/BDD-1/0001/report?inline=0&run=bdd%2Fintegration%2Fci%2Fabc12345%2F20260101T120000Z") {
		t.Errorf("iframe src not the newest by-reference report; body:\n%s", unescaped)
	}
	// Selector: 2 optgroups (two commits), 3 run options (three runstamps). Count
	// run options by their unique data-standalone attribute — a page-wide "<option"
	// count would also catch the log-compose form's note/gotcha/decision options.
	if n := strings.Count(body, "<optgroup"); n != 2 {
		t.Errorf("optgroups = %d, want 2", n)
	}
	if n := strings.Count(body, "data-standalone="); n != 3 {
		t.Errorf("run options = %d, want 3", n)
	}
}

func TestReportRouteStandaloneSelfContained(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "unit", "local", "c0ffee12", "20260101T100000Z")
	h := newServerOver(t, root)

	// No ?run → latest; no inline=0 → fully self-contained standalone file.
	rr := get(t, h, "/ticket/BDD-1/0001/report")
	if rr.Code != 200 {
		t.Fatalf("GET report = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	doc := rr.Body.String()
	// Content present.
	for _, want := range []string{"Happy path", "Bad password", "got 401"} {
		if !strings.Contains(doc, want) {
			t.Errorf("report missing %q", want)
		}
	}
	// Standalone must inline the screenshot as a data: URI and carry no external refs.
	if !strings.Contains(html.UnescapeString(doc), "data:image/png;base64,"+onePxPNG) {
		t.Error("standalone report did not inline the screenshot")
	}
	if strings.Contains(doc, "/report/asset") {
		t.Error("standalone report must not reference the asset route")
	}
}

func TestReportRouteByReferenceUsesAssetRoute(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "unit", "local", "c0ffee12", "20260101T100000Z")
	h := newServerOver(t, root)

	rr := get(t, h, "/ticket/BDD-1/0001/report?inline=0")
	if rr.Code != 200 {
		t.Fatalf("GET report inline=0 = %d", rr.Code)
	}
	doc := html.UnescapeString(rr.Body.String())
	if strings.Contains(doc, onePxPNG) {
		t.Error("by-reference report must not inline the base64 screenshot")
	}
	if !strings.Contains(doc, "/ticket/BDD-1/0001/report/asset?") {
		t.Error("by-reference report did not point at the asset route")
	}
}

func TestReportAssetRouteServesImageBytes(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "unit", "local", "c0ffee12", "20260101T100000Z")
	h := newServerOver(t, root)

	// The screenshot is on feature 0, element 1 (Bad password), step 0, embedding 0.
	rel := "bdd/unit/local/c0ffee12/20260101T100000Z"
	rr := get(t, h, "/ticket/BDD-1/0001/report/asset?run="+rel+"&f=0&e=1&p=step&s=0&i=0")
	if rr.Code != 200 {
		t.Fatalf("GET asset = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("asset Content-Type = %q, want image/png", ct)
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("asset missing nosniff header")
	}
	// PNG magic bytes prove it decoded the base64 to real image bytes.
	if got := rr.Body.Bytes(); len(got) < 8 || got[0] != 0x89 || got[1] != 'P' || got[2] != 'N' || got[3] != 'G' {
		t.Errorf("asset body is not a PNG (len=%d)", len(got))
	}
}

func TestReportAssetRouteBoundsAndTraversal(t *testing.T) {
	root := seedReportRoot(t)
	seedBDDRun(t, root, "unit", "local", "c0ffee12", "20260101T100000Z")
	h := newServerOver(t, root)
	rel := "bdd/unit/local/c0ffee12/20260101T100000Z"

	cases := []string{
		"/ticket/BDD-1/0001/report/asset?run=" + rel + "&f=9&e=0&p=step&s=0&i=0",      // feature OOB
		"/ticket/BDD-1/0001/report/asset?run=" + rel + "&f=0&e=0&p=step&s=0&i=0",      // happy path step has no embedding
		"/ticket/BDD-1/0001/report/asset?run=" + rel + "&f=0&e=1&p=bogus&s=0&i=0",     // bad phase
		"/ticket/BDD-1/0001/report/asset?run=bdd/../../../etc&f=0&e=0&p=step&s=0&i=0", // traversal
		"/ticket/BDD-1/0001/report/asset?run=&f=0&e=0&p=step&s=0&i=0",                 // no run
	}
	for _, path := range cases {
		rr := get(t, h, path)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rr.Code)
		}
	}
}

func TestReportRoute404s(t *testing.T) {
	root := seedReportRoot(t) // no runs captured
	h := newServerOver(t, root)
	if rr := get(t, h, "/ticket/BDD-1/0001/report"); rr.Code != http.StatusNotFound {
		t.Errorf("GET report with no runs = %d, want 404", rr.Code)
	}
	if rr := get(t, h, "/ticket/NOPE/0001/report"); rr.Code != http.StatusNotFound {
		t.Errorf("GET report for missing attempt = %d, want 404", rr.Code)
	}
}
