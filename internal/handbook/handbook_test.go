package handbook_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/handbook"
)

// TestContentNonEmpty guards the embed: a build that fails to embed handbook.md
// would yield empty content, silently shipping no protocol above the wall.
func TestContentNonEmpty(t *testing.T) {
	if strings.TrimSpace(handbook.Content()) == "" {
		t.Fatal("handbook.Content() is empty — handbook.md failed to embed")
	}
	if !strings.Contains(handbook.Content(), "draiver brief") {
		t.Errorf("handbook content is missing the cold-start protocol; got:\n%s", handbook.Content())
	}
}

// TestByteInvariant asserts the append carries nothing per-ticket — the rung-E
// hard constraint (docs/prompt-caching.md). A ticket id, an ISO timestamp, or a
// filesystem attempt path leaking into the text would re-fragment the shared
// prefix across tickets. Placeholders like <TICKET> are fine; a concrete id is not.
func TestByteInvariant(t *testing.T) {
	c := handbook.Content()
	// A placeholder is <TICKET>, never an interpolated concrete id; a concrete
	// ticket id or attempt path in the text would re-fragment the shared prefix.
	for _, bad := range []string{"drvctl-034", "drvweb-013", "/attempts/0001"} {
		if strings.Contains(c, bad) {
			t.Errorf("handbook content contains per-ticket datum %q — breaks byte-invariance", bad)
		}
	}
}

// TestVersionStability: Version is deterministic, non-empty, tracks the content,
// and equals VersionOf(Content()). VersionOf("") is empty so an un-appended
// session records no version.
func TestVersionStability(t *testing.T) {
	if handbook.Version() == "" {
		t.Fatal("Version() is empty for non-empty content")
	}
	if handbook.Version() != handbook.VersionOf(handbook.Content()) {
		t.Errorf("Version() = %q, want VersionOf(Content()) = %q", handbook.Version(), handbook.VersionOf(handbook.Content()))
	}
	if got := handbook.VersionOf(""); got != "" {
		t.Errorf("VersionOf(\"\") = %q, want empty", got)
	}
	if handbook.VersionOf("a") == handbook.VersionOf("b") {
		t.Error("VersionOf must differ for differing text")
	}
	if !strings.HasPrefix(handbook.Version(), "sha256:") {
		t.Errorf("Version() = %q, want a sha256: prefix", handbook.Version())
	}
}

// TestSyncWithOnboardingSkill is the drift guard: handbook.md is the onboarding
// SKILL.md body (frontmatter stripped), so the interactive skill and the cacheable
// system-prompt append never diverge. Compared trimmed, so trailing-newline and
// leading-blank differences do not trip it. Skipped (not failed) when the skill
// file is absent — e.g. an out-of-tree build with only the module — so the guard
// runs in-repo where it matters without breaking elsewhere.
func TestSyncWithOnboardingSkill(t *testing.T) {
	skill := filepath.Join(repoRoot(t), ".claude", "plugins", "draiver", "skills", "draiver-onboarding", "SKILL.md")
	data, err := os.ReadFile(skill)
	if err != nil {
		t.Skipf("onboarding SKILL.md not present (%v); skipping drift guard", err)
	}
	body := skillBody(string(data))
	if strings.TrimSpace(body) != strings.TrimSpace(handbook.Content()) {
		t.Errorf("handbook.md has drifted from the onboarding SKILL.md body.\n"+
			"Re-sync: strip the SKILL.md frontmatter into internal/handbook/handbook.md.\n"+
			"skill body len=%d, handbook len=%d", len(strings.TrimSpace(body)), len(strings.TrimSpace(handbook.Content())))
	}
}

// skillBody returns a SKILL.md's markdown body — everything after the closing
// frontmatter fence. A file without frontmatter is returned unchanged.
func skillBody(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	rest := s[len("---\n"):]
	if i := strings.Index(rest, "\n---\n"); i >= 0 {
		return rest[i+len("\n---\n"):]
	}
	return s
}

// repoRoot walks up from this test file to the module root (the dir holding
// go.mod), so the drift guard finds the checked-in skill regardless of the test's
// working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller for repo root")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test file")
		}
		dir = parent
	}
}
