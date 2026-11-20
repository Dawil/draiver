package metrics

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// mergedRepo builds features/metrics/fixtures/merged-repo.sh into a temp dir.
func mergedRepo(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "features", "metrics", "fixtures", "merged-repo.sh")
	dir := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("sh", script, dir).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	return dir
}

func TestInferBaseCommitMergedShapes(t *testing.T) {
	repo := mergedRepo(t)
	for _, c := range []struct{ ticket, baseTag, file string }{
		{"drv-801", "m801-base", "a801.txt"}, // main merged into the branch, then landed
		{"drv-802", "m802-base", "a802.txt"}, // landed, branch fast-forwarded onto the landing merge
	} {
		want, _ := git(repo, "rev-parse", c.baseTag)
		base, head, err := InferBaseCommit(repo, c.ticket, "0001", "main")
		if err != nil {
			t.Fatalf("%s: %v", c.ticket, err)
		}
		if base != want {
			t.Errorf("%s: base = %s, want %s (%s)", c.ticket, base, want, c.baseTag)
		}
		d, err := FinalDiff(repo, base, head)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Files) != 1 || !d.Files[c.file] {
			t.Errorf("%s: diff files = %v, want only %s", c.ticket, d.Files, c.file)
		}
	}
}
