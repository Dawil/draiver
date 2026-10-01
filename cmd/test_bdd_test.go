package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// bddPyramidCheckout makes a committed checkout whose top rung is a BDD/acceptance
// rung: it names its cucumber-JSON report (drv-016's first-class cucumber_json) and
// declares an extra output artefact (a screenshots dir), its `run` a crystallised
// step script that produces both. The rung binds the `local` environment, which the
// file declares (drv-012 validates that every referenced environment exists). The
// outputs are gitignored — reproducible evidence, not version-controlled — so the
// tree stays clean across repeated `--log` runs and the dirty-tree guard never trips.
// It chdirs the test into the checkout and returns its path.
func bddPyramidCheckout(t *testing.T, ticket, attempt string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitInDir(t, dir, "init", "-q", "-b", "main")
	gitInDir(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	gitInDir(t, dir, "checkout", "-q", "-b", "draiver/"+ticket+"/"+attempt)

	// A minimal but valid cucumber-JSON document (a top-level array), so runRung's
	// live consume parses cleanly and the capture stores a real report.
	cucumberJSON := `[{"uri":"f.feature","keyword":"Feature","name":"X",` +
		`"elements":[{"keyword":"Scenario","name":"works","type":"scenario",` +
		`"steps":[{"keyword":"Given ","name":"a step","result":{"status":"passed"}}]}]}]`
	mustWrite(t, filepath.Join(dir, "bdd.sh"),
		"cat > cucumber.json <<'JSON'\n"+cucumberJSON+"\nJSON\n"+
			"mkdir -p shots\n"+
			"printf img > shots/a.png\n")
	mustWrite(t, filepath.Join(dir, ".gitignore"), "cucumber.json\nshots/\n")
	mustWrite(t, filepath.Join(dir, ".test-pyramid.yaml"),
		"levels:\n"+
			"  - name: unit\n    run: \"true\"\n"+
			"  - name: bdd\n"+
			"    environment: local\n"+
			"    run: sh bdd.sh\n"+
			"    cucumber_json: cucumber.json\n"+
			"    artifacts:\n"+
			"      - shots\n"+
			"environments:\n"+
			"  - name: local\n")
	gitInDir(t, dir, "add", "-A")
	gitInDir(t, dir, "commit", "-q", "-m", "bdd rung + crystallised script")
	t.Chdir(dir)
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two BDD `--log` runs at the same commit/env produce two side-by-side artefact
// sets (never clobbering), the test-result event references the captured set,
// `audit` still passes, and `brief` lists the artefacts (deliverable 4).
func TestBDDCaptureSideBySideAuditBrief(t *testing.T) {
	// Isolate config so a stray ~/.draiver/config.json can't enable pruning.
	t.Setenv("DRAIVER_CONFIG", filepath.Join(t.TempDir(), "absent.json"))

	data := newTicket(t) // PROJ-1/0001
	root := store.Root{Dir: data}
	bddPyramidCheckout(t, "PROJ-1", "0001")

	// First run.
	if out, code := run(t, "--data", data, "--actor", "agent:x", "test", "--log"); code != 0 {
		t.Fatalf("first test --log exited %d: %s", code, out)
	}
	// Second run at the same commit/env.
	out, code := run(t, "--data", data, "--actor", "agent:x", "test", "--log")
	if code != 0 {
		t.Fatalf("second test --log exited %d: %s", code, out)
	}
	if !strings.Contains(out, "captured") {
		t.Errorf("second run did not report a capture: %q", out)
	}

	// Two side-by-side run sets under the per-run key group bdd/bdd/local/<commit>/*.
	head := headOf(t, ".", "HEAD")
	group := filepath.Join(root.ArtefactsDir("PROJ-1", "0001"), "bdd", "bdd", "local", head)
	entries, err := os.ReadDir(group)
	if err != nil {
		t.Fatalf("read run group %s: %v", group, err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 side-by-side run sets, got %d under %s", len(entries), group)
	}
	// Each set holds the captured evidence + run.json manifest.
	for _, e := range entries {
		run := filepath.Join(group, e.Name())
		for _, want := range []string{"cucumber.json", "run.json", filepath.Join("shots", "a.png")} {
			if _, err := os.Stat(filepath.Join(run, want)); err != nil {
				t.Errorf("run %s missing %s: %v", e.Name(), want, err)
			}
		}
	}

	// The latest test-result event references the captured set.
	events, _ := ticketlog.Read(root, "PROJ-1", "0001")
	last := events[len(events)-1]
	if last.Type != "test-result" {
		t.Fatalf("last event = %q, want test-result", last.Type)
	}
	if len(last.Artefacts) == 0 {
		t.Fatal("test-result event references no artefacts")
	}
	foundJSON, foundMeta := false, false
	for _, a := range last.Artefacts {
		if strings.HasSuffix(a, "/cucumber.json") {
			foundJSON = true
		}
		if strings.HasSuffix(a, "/run.json") {
			foundMeta = true
		}
		// refs must be under the per-run key for this commit
		if !strings.Contains(a, "bdd/bdd/local/"+head+"/") {
			t.Errorf("artefact ref %q not under the per-run key", a)
		}
	}
	if !foundJSON || !foundMeta {
		t.Errorf("artefact refs %v missing cucumber.json/run.json", last.Artefacts)
	}

	// audit still passes over the tampered-nothing chain.
	if out, code := run(t, "--data", data, "audit", "PROJ-1"); code != 0 {
		t.Errorf("audit failed after capture: %d\n%s", code, out)
	}

	// brief lists the captured artefacts.
	brief, code := run(t, "--data", data, "brief", "PROJ-1")
	if code != 0 {
		t.Fatalf("brief exited %d: %s", code, brief)
	}
	if !strings.Contains(brief, "[artefact: bdd/bdd/local/") {
		t.Errorf("brief did not list captured artefacts:\n%s", brief)
	}
}

// A BDD run honours the retention cap: with keep=1, each `--log` run prunes older
// run sets in the group down to the newest one.
func TestBDDCaptureRetentionCap(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	mustWrite(t, cfgPath, `{"bdd_artefact_keep": 1}`)
	t.Setenv("DRAIVER_CONFIG", cfgPath)

	data := newTicket(t)
	root := store.Root{Dir: data}
	bddPyramidCheckout(t, "PROJ-1", "0001")

	for i := 0; i < 3; i++ {
		if out, code := run(t, "--data", data, "--actor", "agent:x", "test", "--log"); code != 0 {
			t.Fatalf("run %d exited %d: %s", i, code, out)
		}
	}

	head := headOf(t, ".", "HEAD")
	group := filepath.Join(root.ArtefactsDir("PROJ-1", "0001"), "bdd", "bdd", "local", head)
	entries, err := os.ReadDir(group)
	if err != nil {
		t.Fatalf("read run group: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("keep=1 left %d run sets, want 1", len(entries))
	}
}
