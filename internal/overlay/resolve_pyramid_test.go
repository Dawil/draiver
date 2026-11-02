package overlay

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/worktree"
)

const minimalPyramid = `levels:
  - name: unit
    run: "true"
`

func writePyramid(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, pyramid.FileName), []byte(minimalPyramid), 0o644); err != nil {
		t.Fatalf("write pyramid: %v", err)
	}
}

// TestResolvePyramid_InRepoWins: a codebase worktree carrying its own pyramid resolves
// to that pyramid with no overlay and no extraEnv — even when an explicit overlay is
// configured, in-repo wins outright (step 1), so a local pyramid runs byte-for-byte as
// before.
func TestResolvePyramid_InRepoWins(t *testing.T) {
	wd := t.TempDir()
	writePyramid(t, wd)
	key := worktree.Key{Ticket: "T-1", Attempt: "0001"}

	p, active, env, err := ResolvePyramid(context.Background(), wd, "/codebases/api", "/some/explicit/overlay", "/overlays", key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected the in-repo pyramid to load")
	}
	if active != nil {
		t.Fatalf("in-repo wins must not resolve an overlay, got %+v", active)
	}
	if env != nil {
		t.Fatalf("in-repo pyramid must carry no overlay script-env, got %v", env)
	}
}

// TestResolvePyramid_NoInRepoNoOverlay_NoOp: no in-repo pyramid and no resolvable overlay
// is the "nothing to run" no-op (nil pyramid, nil error) — the terminal case every caller
// maps to its own terminal state (`draiver test` a success, promote.Run StatusNoPromotion).
// This is the case drv-044 preserves while no longer firing it on every overlay case.
func TestResolvePyramid_NoInRepoNoOverlay_NoOp(t *testing.T) {
	wd := t.TempDir() // empty — no .test-pyramid.yaml
	key := worktree.Key{Ticket: "T-1", Attempt: "0001"}

	p, active, env, err := ResolvePyramid(context.Background(), wd, "/codebases/api", "", "", key)
	if err != nil {
		t.Fatalf("no-op must not error, got %v", err)
	}
	if p != nil || active != nil || env != nil {
		t.Fatalf("expected the (nil,nil,nil,nil) no-op, got p=%v active=%v env=%v", p, active, env)
	}
}
