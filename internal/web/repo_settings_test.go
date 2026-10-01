package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/config"
)

// isolateConfig points config resolution ($DRAIVER_CONFIG) at a temp file so the
// per-repo settings handler never reads or writes the operator's real
// ~/.draiver/config.json during a test. It returns the path for assertions.
func isolateConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DRAIVER_CONFIG", path)
	return path
}

// postRepoSettings posts the repo-settings form the way the panel does, same-origin.
func postRepoSettings(t *testing.T, h http.Handler, id, att, remote, branch, rung string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"remote": {remote}, "branch": {branch}, "rung": {rung}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/"+id+"/"+att+"/repo-settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func loadRepoSettings(t *testing.T, path, repoPath string) config.RepoSettings {
	t.Helper()
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return c.Repos[repoPath]
}

// TestRepoSettingsPanelRendersRightOfProvenance pins the panel onto the attempt page
// beside provenance, with the scope note that makes the broader blast radius explicit
// and the three keyed inputs prefilled from the repo's stored entry.
func TestRepoSettingsPanelRendersRightOfProvenance(t *testing.T) {
	path := isolateConfig(t)
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	// Seed a stored entry so the panel prefills.
	rung := "integration"
	if _, err := config.SetRepoSettings(path, "/tmp/repo", config.RepoSettingsUpdate{DefaultTestRung: &rung}); err != nil {
		t.Fatal(err)
	}

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	body := get(t, s.Handler(), "/ticket/PROJ-3/0001").Body.String()

	for _, want := range []string{
		`data-testid="repo-settings"`,
		`data-testid="repo-settings-scope"`,
		`data-testid="repo-settings-remote"`,
		`data-testid="repo-settings-branch"`,
		`data-testid="repo-settings-rung"`,
		`/ticket/PROJ-3/0001/repo-settings`,
		`value="integration"`,    // the stored rung prefills
		`<code>/tmp/repo</code>`, // scope names the repo
	} {
		if !strings.Contains(body, want) {
			t.Errorf("repo-settings panel missing %q", want)
		}
	}
	// It renders to the RIGHT of provenance: both inside the panel-row, provenance first.
	if !strings.Contains(body, `data-testid="panel-row"`) {
		t.Error("panels should share a panel-row wrapper")
	}
	if pi, ri := strings.Index(body, `data-testid="provenance"`), strings.Index(body, `data-testid="repo-settings"`); pi < 0 || ri < 0 || pi > ri {
		t.Errorf("repo-settings should follow provenance (provenance@%d, repo-settings@%d)", pi, ri)
	}
}

// TestRepoSettingsNoRepoDisablesPanel pins that an attempt with no recorded repo
// shows the disabled hint, not an editor keyed on nothing.
func TestRepoSettingsNoRepoDisablesPanel(t *testing.T) {
	isolateConfig(t)
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	// PROJ-1/0002 records no attempt.md → no repo.
	body := get(t, s.Handler(), "/ticket/PROJ-1/0002").Body.String()
	if !strings.Contains(body, `data-testid="repo-settings-disabled"`) {
		t.Error("an attempt with no repo should show the disabled hint")
	}
	if strings.Contains(body, `data-testid="repo-settings-form"`) {
		t.Error("an attempt with no repo must not render an editor form")
	}
}

// TestRepoSettingsSaveWritesGlobalConfig is the end-to-end write: the POST goes
// through the gateway into config.json (not attempt.md), and the response re-renders
// the panel with the saved values and a flash.
func TestRepoSettingsSaveWritesGlobalConfig(t *testing.T) {
	path := isolateConfig(t)
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/checkout", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rr := postRepoSettings(t, h, "PROJ-3", "0001", "forgejo", "trunk", "e2e")
	if rr.Code != 200 {
		t.Fatalf("POST repo-settings = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	got := loadRepoSettings(t, path, "/tmp/checkout")
	want := config.RepoSettings{DefaultRemote: "forgejo", DefaultBranch: "trunk", DefaultTestRung: "e2e"}
	if got != want {
		t.Errorf("config.json not written through the gateway: got %+v, want %+v", got, want)
	}
	body := rr.Body.String()
	for _, w := range []string{`value="forgejo"`, `value="trunk"`, `value="e2e"`, `data-testid="repo-settings-saved"`} {
		if !strings.Contains(body, w) {
			t.Errorf("save response missing %q", w)
		}
	}
}

// TestRepoSettingsSaveSharedAcrossAttempts is the ticket's reason to exist: a write
// from ONE attempt's panel is observed by ANOTHER attempt on the same repo, because
// the store is the one global config, not per-attempt attempt.md.
func TestRepoSettingsSaveSharedAcrossAttempts(t *testing.T) {
	isolateConfig(t)
	root := seedBoard(t)
	// Two attempts on the SAME repo path.
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/shared", "main")
	writeAttemptMeta(t, root, "PROJ-2", "0001", "/tmp/shared", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Set the default remote from PROJ-3/0001's panel.
	if rr := postRepoSettings(t, h, "PROJ-3", "0001", "forgejo", "", ""); rr.Code != 200 {
		t.Fatalf("POST = %d\n%s", rr.Code, rr.Body.String())
	}
	// PROJ-2/0001's page (a different attempt) now prefills the same value.
	other := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	if !strings.Contains(other, `value="forgejo"`) {
		t.Errorf("a second attempt on the same repo should observe the one write\n%s", other)
	}
}

// TestRepoSettingsBlankLeavesStoredUnchanged pins the prefill-diff: posting a field's
// prefilled value back is a no-op, so editing only one field never clears the others.
func TestRepoSettingsBlankLeavesStoredUnchanged(t *testing.T) {
	path := isolateConfig(t)
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	remote, branch := "forgejo", "trunk"
	if _, err := config.SetRepoSettings(path, "/tmp/repo", config.RepoSettingsUpdate{DefaultRemote: &remote, DefaultBranch: &branch}); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Change only the rung; post the prefilled remote/branch back verbatim.
	if rr := postRepoSettings(t, s.Handler(), "PROJ-3", "0001", "forgejo", "trunk", "integration"); rr.Code != 200 {
		t.Fatalf("POST = %d\n%s", rr.Code, rr.Body.String())
	}
	got := loadRepoSettings(t, path, "/tmp/repo")
	want := config.RepoSettings{DefaultRemote: "forgejo", DefaultBranch: "trunk", DefaultTestRung: "integration"}
	if got != want {
		t.Errorf("unchanged fields were clobbered: got %+v, want %+v", got, want)
	}
}

// TestRepoSettingsClearFallsBack pins that clearing a set field (prefill → blank)
// writes the empty string, so the resolver falls back to the global default.
func TestRepoSettingsClearFallsBack(t *testing.T) {
	path := isolateConfig(t)
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	remote := "forgejo"
	if _, err := config.SetRepoSettings(path, "/tmp/repo", config.RepoSettingsUpdate{DefaultRemote: &remote}); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Clear the remote (prefill "forgejo" → submitted "").
	if rr := postRepoSettings(t, s.Handler(), "PROJ-3", "0001", "", "", ""); rr.Code != 200 {
		t.Fatalf("POST = %d\n%s", rr.Code, rr.Body.String())
	}
	if got := loadRepoSettings(t, path, "/tmp/repo").DefaultRemote; got != "" {
		t.Errorf("stored remote = %q, want cleared", got)
	}
}

// TestRepoSettingsGuards covers the route's guards: cross-origin is refused, an
// unknown attempt 404s, and an attempt with no repo is a 400 (no key to write under).
func TestRepoSettingsGuards(t *testing.T) {
	isolateConfig(t)
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Cross-origin.
	form := url.Values{"remote": {"evil"}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-3/0001/repo-settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", rr.Code)
	}

	// Unknown attempt.
	if rr := postRepoSettings(t, h, "PROJ-3", "9999", "x", "", ""); rr.Code != http.StatusNotFound {
		t.Errorf("unknown attempt = %d, want 404", rr.Code)
	}

	// No repo recorded → 400.
	if rr := postRepoSettings(t, h, "PROJ-1", "0002", "x", "", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("no-repo POST = %d, want 400", rr.Code)
	}
}
