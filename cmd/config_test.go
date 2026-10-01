package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/config"
)

// TestConfigRepoWriteThenRead exercises the write form (flags set keys) and the bare
// read form (resolved values), going through the same gateway the webui uses.
func TestConfigRepoWriteThenRead(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DRAIVER_CONFIG", cfgPath)

	// Write all three.
	out, code := run(t, "config", "repo", "/home/dev/app", "--remote", "forgejo", "--branch", "main", "--rung", "integration")
	if code != 0 {
		t.Fatalf("config repo write exited %d: %s", code, out)
	}
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := c.Repos["/home/dev/app"]
	want := config.RepoSettings{DefaultRemote: "forgejo", DefaultBranch: "main", DefaultTestRung: "integration"}
	if got != want {
		t.Fatalf("stored = %+v, want %+v", got, want)
	}

	// Bare read prints the resolved values, annotated per-repo.
	out, code = run(t, "config", "repo", "/home/dev/app")
	if code != 0 {
		t.Fatalf("config repo read exited %d: %s", code, out)
	}
	for _, w := range []string{"forgejo", "main", "integration", "per-repo"} {
		if !strings.Contains(out, w) {
			t.Errorf("read output missing %q:\n%s", w, out)
		}
	}
}

// TestConfigRepoPartialUpdatePreserves pins that setting one key leaves the others
// untouched — the same partial-update discipline `attempt set` has.
func TestConfigRepoPartialUpdatePreserves(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DRAIVER_CONFIG", cfgPath)

	if _, code := run(t, "config", "repo", "/r", "--branch", "main"); code != 0 {
		t.Fatal("seed write failed")
	}
	if _, code := run(t, "config", "repo", "/r", "--rung", "e2e"); code != 0 {
		t.Fatal("second write failed")
	}
	c, _ := config.Load(cfgPath)
	got := c.Repos["/r"]
	if got.DefaultBranch != "main" || got.DefaultTestRung != "e2e" {
		t.Errorf("partial updates should accrete: got %+v", got)
	}
}

// TestConfigRepoReadFallsBackToGlobal pins the resolution order in the read form: an
// unset remote resolves to the global primary_remote, labelled as a global default.
func TestConfigRepoReadFallsBackToGlobal(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DRAIVER_CONFIG", cfgPath)
	// A global primary_remote and a repo that sets only its branch: the read should
	// resolve the remote to the global and label it as such, while the branch shows as
	// the repo's own.
	if err := os.WriteFile(cfgPath, []byte(`{"primary_remote": "origin", "repos": {"/r": {"default_branch": "main"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := run(t, "config", "repo", "/r")
	if code != 0 {
		t.Fatalf("read exited %d: %s", code, out)
	}
	if !strings.Contains(out, "origin") || !strings.Contains(out, "global default") {
		t.Errorf("expected the remote to resolve to origin via global fallback:\n%s", out)
	}
	if !strings.Contains(out, "main") || !strings.Contains(out, "per-repo") {
		t.Errorf("expected the per-repo branch to show:\n%s", out)
	}
}
