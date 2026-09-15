package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// pyramidAttempt sets up a ticket whose attempt has a materialized managed
// worktree carrying a committed .test-pyramid.yaml built from name→run rungs. The
// pyramid is committed so the tree is clean (a precondition of `test --log`) and
// pyramid.Load resolves it. It returns the data root, the base repo, and the
// checkout path — the substrate the review gate folds over.
func pyramidAttempt(t *testing.T, rungs [][2]string) (data, repo, checkout string) {
	t.Helper()
	repo = landRepo(t) // sets XDG_CACHE_HOME so managed checkouts stay in a temp dir
	data = t.TempDir()
	if _, code := run(t, "--data", data, "--actor", "human:test", "new", "PROJ-1", "--title", "T", "--repo", repo); code != 0 {
		t.Fatalf("new exited %d", code)
	}
	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}})
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	var b strings.Builder
	b.WriteString("levels:\n")
	for _, r := range rungs {
		fmt.Fprintf(&b, "  - name: %s\n    run: %s\n", r[0], r[1])
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".test-pyramid.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", ".test-pyramid.yaml")
	gitIn(t, wt.Path, "commit", "-q", "-m", "add pyramid")
	return data, repo, wt.Path
}

// reviewEventCount returns how many review events the attempt's log holds — the
// probe for "did a refused claim leave an event behind" (it must not).
func reviewEventCount(t *testing.T, data string) int {
	t.Helper()
	events, err := ticketlog.Read(store.Root{Dir: data}, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Type == "review" {
			n++
		}
	}
	return n
}

// With a pyramid but no test-result at HEAD at all, the target rung was never
// climbed: review is refused, names the target rung and the exact fix, and writes
// no event.
func TestReviewGate_RefusesWhenTargetNeverClimbed(t *testing.T) {
	data, _, _ := pyramidAttempt(t, [][2]string{{"unit", "true"}, {"integration", "true"}})
	out, code, err := runE(t, "--data", data, "--actor", "agent:test", "review", "PROJ-1", "ready")
	if code == 0 {
		t.Fatalf("expected the gate to refuse the claim, got exit 0: %s", out)
	}
	msg := errText(err)
	if !strings.Contains(msg, "integration") {
		t.Errorf("refusal should name the target rung: %q", msg)
	}
	if !strings.Contains(msg, "draiver test --log") {
		t.Errorf("refusal should name the exact fix `draiver test --log`: %q", msg)
	}
	if n := reviewEventCount(t, data); n != 0 {
		t.Errorf("a refused claim must leave no review event, found %d", n)
	}
}

// A logged rung *below* the target is present-but-insufficient: the gate still
// refuses, because the highest rung green at HEAD is not the target.
func TestReviewGate_RefusesWhenOnlyLowerRungClimbed(t *testing.T) {
	data, _, wt := pyramidAttempt(t, [][2]string{{"unit", "true"}, {"integration", "true"}})
	t.Chdir(wt)
	if out, code := run(t, "--data", data, "--actor", "agent:test", "test", "unit", "--log"); code != 0 {
		t.Fatalf("test unit --log exited %d: %s", code, out)
	}
	out, code, err := runE(t, "--data", data, "--actor", "agent:test", "review", "PROJ-1", "ready")
	if code == 0 {
		t.Fatalf("a below-target logged rung must still be refused, got exit 0: %s", out)
	}
	msg := errText(err)
	if !strings.Contains(msg, "unit") || !strings.Contains(msg, "integration") {
		t.Errorf("refusal should name both the highest green rung and the target: %q", msg)
	}
	if n := reviewEventCount(t, data); n != 0 {
		t.Errorf("a refused claim must leave no review event, found %d", n)
	}
}

// The target rung logged green at the current HEAD admits the claim.
func TestReviewGate_AdmitsWhenTargetGreenAtHEAD(t *testing.T) {
	data, _, wt := pyramidAttempt(t, [][2]string{{"unit", "true"}, {"integration", "true"}})
	t.Chdir(wt)
	if out, code := run(t, "--data", data, "--actor", "agent:test", "test", "--log"); code != 0 {
		t.Fatalf("test --log exited %d: %s", code, out)
	}
	out, code := run(t, "--data", data, "--actor", "agent:test", "review", "PROJ-1", "ready")
	if code != 0 {
		t.Fatalf("target green at HEAD should admit the claim, got exit %d: %s", code, out)
	}
	if n := reviewEventCount(t, data); n != 1 {
		t.Errorf("an admitted claim should append exactly one review event, found %d", n)
	}
}

// A result logged before a further commit is stale: the later commit moves HEAD,
// so the logged commit no longer equals HEAD and the claim is refused. This is the
// invalidation the gate exists to catch.
func TestReviewGate_RefusesStaleResultAfterFurtherCommit(t *testing.T) {
	data, _, wt := pyramidAttempt(t, [][2]string{{"unit", "true"}, {"integration", "true"}})
	t.Chdir(wt)
	if out, code := run(t, "--data", data, "--actor", "agent:test", "test", "--log"); code != 0 {
		t.Fatalf("test --log exited %d: %s", code, out)
	}
	// A further commit after logging moves HEAD, invalidating the recorded result.
	commitFile(t, wt, "after.txt", "more work")
	out, code, err := runE(t, "--data", data, "--actor", "agent:test", "review", "PROJ-1", "ready")
	if code == 0 {
		t.Fatalf("a result staled by a later commit must be refused, got exit 0: %s", out)
	}
	if !strings.Contains(errText(err), "draiver test --log") {
		t.Errorf("refusal should name the exact fix: %q", errText(err))
	}
}

// errText renders an error for substring assertions, tolerating a nil error.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A repo that declares no .test-pyramid.yaml is not gated at all: review proceeds
// unchanged. reviewAttempt materializes a checkout with no pyramid.
func TestReviewGate_NoPyramidIsNotGated(t *testing.T) {
	data, _, _ := reviewAttempt(t, false) // no pyramid in the checkout; already claims review, exit 0
	if n := reviewEventCount(t, data); n != 1 {
		t.Errorf("with no pyramid the claim should be admitted, found %d review events", n)
	}
}
