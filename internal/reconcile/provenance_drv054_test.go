package reconcile_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/reconcile"
)

// A newly cut attempt records its metrics provenance in attempt.md (drv-054): the
// exact commit its worktree was cut from, the repo identity, and the draiver build.
func TestAdmitRecordsCutProvenance(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	r, err := reconcile.New(reconcile.Options{
		Root: w.root, Adapters: f.adapters, Actor: "agent:claude-code", Proc: newProc(),
		DraiverVersion: "9.9.9-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	out, err := exec.Command("git", "-C", w.repo, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(out))
	meta, err := attempt.LoadMeta(w.root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	if meta.BaseCommit != head {
		t.Errorf("base_commit = %q, want %q", meta.BaseCommit, head)
	}
	if meta.RepoID == "" {
		t.Error("repo_id not recorded")
	}
	if meta.DraiverVersion != "9.9.9-test" {
		t.Errorf("draiver_version = %q", meta.DraiverVersion)
	}
}
