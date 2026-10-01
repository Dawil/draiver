package bddartefact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeFile writes content to a path under dir, creating parents.
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A capture writes the declared files + a dir recursively under the per-run key and
// returns refs (sorted, artefacts-relative) covering them plus run.json.
func TestCaptureWritesSetAndRefs(t *testing.T) {
	wt := t.TempDir()
	art := t.TempDir()
	writeFile(t, wt, "cucumber.json", `{"ok":true}`)
	writeFile(t, wt, "shots/a.png", "img-a")
	writeFile(t, wt, "shots/b.png", "img-b")

	rc := RunContext{Rung: "bdd", Environment: "local", Commit: "abc123", Runstamp: "20260101T120000Z"}
	key, refs, err := Capture(art, wt, rc, []Source{{Path: "cucumber.json"}, {Path: "shots"}})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	wantKey := "bdd/bdd/local/abc123/20260101T120000Z"
	if key != wantKey {
		t.Errorf("key = %q, want %q", key, wantKey)
	}
	want := []string{
		wantKey + "/cucumber.json",
		wantKey + "/run.json",
		wantKey + "/shots",
	}
	if !sort.StringsAreSorted(refs) {
		t.Errorf("refs not sorted: %v", refs)
	}
	if strings.Join(refs, ",") != strings.Join(want, ",") {
		t.Errorf("refs = %v, want %v", refs, want)
	}

	// The captured bytes landed on disk, dir recursively.
	if b, _ := os.ReadFile(filepath.Join(art, filepath.FromSlash(wantKey), "cucumber.json")); string(b) != `{"ok":true}` {
		t.Errorf("cucumber.json not captured: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(art, filepath.FromSlash(wantKey), "shots", "b.png")); string(b) != "img-b" {
		t.Errorf("shots/b.png not captured: %q", b)
	}
}

// run.json records the reproducibility context and the captured manifest.
func TestCaptureWritesRunMeta(t *testing.T) {
	wt := t.TempDir()
	art := t.TempDir()
	writeFile(t, wt, "cucumber.json", "{}")

	rc := RunContext{Rung: "bdd", Environment: "qa", Commit: "deadbeef", Runstamp: "20260101T120000Z", Healthcheck: "green"}
	key, _, err := Capture(art, wt, rc, []Source{{Path: "cucumber.json"}})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(art, filepath.FromSlash(key), runMetaName))
	if err != nil {
		t.Fatalf("read run meta: %v", err)
	}
	var got RunContext
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal run meta: %v", err)
	}
	if got.Rung != "bdd" || got.Environment != "qa" || got.Commit != "deadbeef" ||
		got.Runstamp != "20260101T120000Z" || got.Healthcheck != "green" {
		t.Errorf("run meta context = %+v", got)
	}
	if len(got.Captured) != 1 || got.Captured[0] != "cucumber.json" {
		t.Errorf("run meta captured = %v, want [cucumber.json]", got.Captured)
	}
}

// Two captures at the *same* rung/env/commit/runstamp coexist side by side — the
// second never clobbers the first (deliverable 4).
func TestCaptureNeverClobbers(t *testing.T) {
	wt := t.TempDir()
	art := t.TempDir()
	writeFile(t, wt, "cucumber.json", "run-1")
	rc := RunContext{Rung: "bdd", Environment: "local", Commit: "abc", Runstamp: "20260101T120000Z"}

	key1, _, err := Capture(art, wt, rc, []Source{{Path: "cucumber.json"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, wt, "cucumber.json", "run-2")
	key2, _, err := Capture(art, wt, rc, []Source{{Path: "cucumber.json"}})
	if err != nil {
		t.Fatal(err)
	}

	if key1 == key2 {
		t.Fatalf("second capture reused the first key %q", key1)
	}
	if b, _ := os.ReadFile(filepath.Join(art, filepath.FromSlash(key1), "cucumber.json")); string(b) != "run-1" {
		t.Errorf("first run clobbered: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(art, filepath.FromSlash(key2), "cucumber.json")); string(b) != "run-2" {
		t.Errorf("second run not captured: %q", b)
	}
}

// A declared source that does not exist is surfaced as an error, not swallowed.
func TestCaptureMissingSourceErrors(t *testing.T) {
	wt := t.TempDir()
	art := t.TempDir()
	rc := RunContext{Rung: "bdd", Commit: "abc", Runstamp: "20260101T120000Z"}
	if _, _, err := Capture(art, wt, rc, []Source{{Path: "nope.json"}}); err == nil {
		t.Fatal("expected an error for a missing declared source")
	}
}

// A blank environment keys under "none" so a rung with no binding still gets a
// stable key.
func TestCaptureBlankEnvKeysNone(t *testing.T) {
	wt := t.TempDir()
	art := t.TempDir()
	writeFile(t, wt, "cucumber.json", "{}")
	rc := RunContext{Rung: "bdd", Commit: "abc", Runstamp: "20260101T120000Z"}
	key, _, err := Capture(art, wt, rc, []Source{{Path: "cucumber.json"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "bdd/bdd/none/") {
		t.Errorf("blank env did not key under none: %q", key)
	}
}

// capture is a test helper: one capture at an explicit runstamp.
func capture(t *testing.T, art, wt string, rc RunContext) string {
	t.Helper()
	writeFile(t, wt, "cucumber.json", "x")
	key, _, err := Capture(art, wt, rc, []Source{{Path: "cucumber.json"}})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// Prune keeps the newest `keep` runs per (rung,env) and removes older ones; it
// never touches another group.
func TestPruneKeepsNewestPerGroup(t *testing.T) {
	wt := t.TempDir()
	art := t.TempDir()

	// three runs in group (bdd, local), newest last by runstamp
	capture(t, art, wt, RunContext{Rung: "bdd", Environment: "local", Commit: "c1", Runstamp: "20260101T100000Z"})
	k2 := capture(t, art, wt, RunContext{Rung: "bdd", Environment: "local", Commit: "c2", Runstamp: "20260101T110000Z"})
	k3 := capture(t, art, wt, RunContext{Rung: "bdd", Environment: "local", Commit: "c3", Runstamp: "20260101T120000Z"})
	// a different group must be untouched
	kOther := capture(t, art, wt, RunContext{Rung: "bdd", Environment: "qa", Commit: "c1", Runstamp: "20260101T100000Z"})

	removed, err := Prune(art, "bdd", "local", 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %v, want exactly the oldest run", removed)
	}
	// newest two survive
	for _, k := range []string{k2, k3, kOther} {
		if _, err := os.Stat(filepath.Join(art, filepath.FromSlash(k))); err != nil {
			t.Errorf("kept run missing: %s (%v)", k, err)
		}
	}
	// oldest is gone
	if _, err := os.Stat(filepath.Join(art, filepath.FromSlash(removed[0]))); !os.IsNotExist(err) {
		t.Errorf("pruned run still present: %s", removed[0])
	}
}

// keep <= 0 is the default: keep everything, no GC.
func TestPruneKeepAllByDefault(t *testing.T) {
	wt := t.TempDir()
	art := t.TempDir()
	capture(t, art, wt, RunContext{Rung: "bdd", Environment: "local", Commit: "c1", Runstamp: "20260101T100000Z"})
	capture(t, art, wt, RunContext{Rung: "bdd", Environment: "local", Commit: "c2", Runstamp: "20260101T110000Z"})
	removed, err := Prune(art, "bdd", "local", 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("keep=0 pruned %v, want nothing", removed)
	}
}

// Prune on a group with fewer runs than the cap (or none) is a clean no-op.
func TestPruneUnderCapNoop(t *testing.T) {
	art := t.TempDir()
	removed, err := Prune(art, "bdd", "local", 5)
	if err != nil {
		t.Fatalf("prune empty: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("empty group pruned %v", removed)
	}
}
