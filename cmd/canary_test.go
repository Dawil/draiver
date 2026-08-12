package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSession writes a synthetic metered session for an attempt: a meter.json
// with an active totals block and a stream.jsonl whose first usage frame is the
// turn-1 evidence the canary reads.
func writeSession(t *testing.T, root, ticket string, t1read, t1create, t1input int) {
	t.Helper()
	dir := filepath.Join(root, ticket, "attempts", "0001", "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meter := fmt.Sprintf(`{"totals":{"input_tokens":%d,"output_tokens":200,"cache_read_tokens":500000,"cache_creation_tokens":20000}}`, t1input)
	if err := os.WriteFile(filepath.Join(dir, "meter.json"), []byte(meter), 0o644); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"assistant","message":{"usage":{"input_tokens":%d,"output_tokens":5,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`, t1input, t1read, t1create)
	if err := os.WriteFile(filepath.Join(dir, "stream.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// twoAttemptRepo creates two tickets sharing one repo path (baseline first, so its
// created-event timestamp is earliest) and writes each a session.
func twoAttemptRepo(t *testing.T, baselineRead, baselineCreate, secondRead, secondCreate, secondInput int) string {
	t.Helper()
	dir := t.TempDir()
	repo := t.TempDir() // the shared repo path both attempts target
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "BASE-1", "--title", "b", "--repo", repo); code != 0 {
		t.Fatalf("new BASE-1 exited %d", code)
	}
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "SND-2", "--title", "s", "--repo", repo); code != 0 {
		t.Fatalf("new SND-2 exited %d", code)
	}
	writeSession(t, dir, "BASE-1", baselineRead, baselineCreate, 500)
	writeSession(t, dir, "SND-2", secondRead, secondCreate, secondInput)
	return dir
}

func TestCanaryQuietOnHealthyFleet(t *testing.T) {
	// Baseline cold-writes the prefix; the second attempt turn-1 reads it.
	dir := twoAttemptRepo(t, 0, 20000, 20000, 1000, 50)
	out, code := run(t, "--data", dir, "canary")
	if code != 0 {
		t.Fatalf("healthy fleet should exit 0, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "OK  ") {
		t.Errorf("expected an OK repo line, got:\n%s", out)
	}
}

func TestCanaryReadCreationColumn(t *testing.T) {
	// The reuse attempt reads 20000 and cold-writes 1000 on turn-1 → 20.00x (2000%),
	// the unbounded amortization signal the ">100% cache hit" goal refers to.
	dir := twoAttemptRepo(t, 0, 20000, 20000, 1000, 50)
	out, code := run(t, "--data", dir, "canary")
	if code != 0 {
		t.Fatalf("healthy fleet should exit 0, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "t1-rd:cr") {
		t.Errorf("expected the t1-rd:cr header, got:\n%s", out)
	}
	if !strings.Contains(out, "20.00x") {
		t.Errorf("expected read:creation 20.00x for the reuse attempt, got:\n%s", out)
	}
}

func TestCanaryFiresOnBustedFleet(t *testing.T) {
	// The second attempt re-creates the prefix on turn-1 instead of reading it.
	dir := twoAttemptRepo(t, 0, 20000, 0, 20000, 500)
	out, code := run(t, "--data", dir, "canary")
	if code != ExitCanaryFired {
		t.Fatalf("busted fleet should exit %d, got %d\n%s", ExitCanaryFired, code, out)
	}
	if !strings.Contains(out, "FIRE") || !strings.Contains(out, "did not reuse the shared prefix") {
		t.Errorf("expected a FIRE line and a cold finding, got:\n%s", out)
	}
}

func TestCanaryRepoFilter(t *testing.T) {
	dir := twoAttemptRepo(t, 0, 20000, 0, 20000, 500) // busted
	// Filtering to a repo path that doesn't exist yields nothing to scan, exit 0.
	out, code := run(t, "--data", dir, "canary", "--repo", "/nonexistent")
	if code != 0 {
		t.Fatalf("filtering out the busted repo should exit 0, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "nothing to scan") {
		t.Errorf("expected 'nothing to scan', got:\n%s", out)
	}
}
