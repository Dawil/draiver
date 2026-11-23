package acceptance

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_evals_drv065.go realises drv-065: agent-context evals are an ordinary advisory
// pyramid level bound to an environment. The fixture scenarios shell the freshly built
// draiver binary (overlayBin) against an isolated data root / config / cache and a
// codebase whose evals rung is a STUB run: — it writes a trace line and a fake report,
// no agent involved — so they prove the pyramid contract, not the eval suite. The last
// scenario reads this repository's real .test-pyramid.yaml.
func init() {
	steps := map[string]StepFunc{
		"a codebase whose pyramid has a stub advisory evals rung above a required unit rung":                  drv065GivenGreenEnv,
		"a codebase whose stub evals rung needs an agent CLI this box does not have":                          drv065GivenRedEnv,
		"I run a bare draiver test in the evals codebase":                                                     drv065WhenBare,
		"I run draiver test evals --only in the evals codebase":                                               drv065WhenOnly,
		"the unit rung ran and the evals rung did not":                                                        drv065ThenBareSkipsEvals,
		"only the evals rung ran and the run exits zero":                                                      drv065ThenOnlyRan,
		"the evals report is captured into the attempt artefact store":                                        drv065ThenReportCaptured,
		"the evals run records no test-result gate event":                                                     drv065ThenNoGate,
		"the evals rung is blocked by its environment healthcheck and never runs":                             drv065ThenBlocked,
		"this repository's own test pyramid":                                                                  drv065GivenRepoPyramid,
		"its evals rung is the top level, advisory, and bound to an environment that requires the claude CLI": drv065ThenRepoEvalsAdvisory,
		"its evals rung declares its report directory as an artifact":                                         drv065ThenRepoEvalsArtifact,
	}
	for text, fn := range steps {
		registry[text] = fn
	}
}

const (
	drv065Ticket = "EVL-1"
	drv065Att    = "0001"
	// drv065Trace is the untracked file each stub rung appends its name to, so a
	// scenario can see exactly which rungs ran.
	drv065Trace = ".climb-trace"
)

// drv065Pyramid: a required unit rung and, above it, an advisory evals rung bound to an
// `agent` env whose healthcheck is %s. The evals run is a stub that writes a fake report
// into the declared artifacts dir — the shape a real suite (evals/run.sh) has.
const drv065Pyramid = `environments:
  - name: agent
    healthchecks:
      - name: claude-cli
        script: '%s'
levels:
  - name: unit
    run: 'echo unit >> ` + drv065Trace + `'
  - name: evals
    run: 'echo evals >> ` + drv065Trace + ` && mkdir -p evals/_artifacts && printf "{\"stub\":true,\"passRate\":100}" > evals/_artifacts/report.json'
    environment: agent
    required: false
    artifacts:
      - evals/_artifacts
`

type drv065Fixture struct {
	dataRoot, configPath, cacheDir, checkout string
	out                                      string
	err                                      error
}

func drv065GivenGreenEnv(w *World, sr *StepRun) error { return drv065Seed(w, "true") }

// A healthcheck probing for a CLI that does not exist stands in for "no claude on this
// box": the env must block the rung (exit 7, dependency not ready), never pass it.
func drv065GivenRedEnv(w *World, sr *StepRun) error {
	return drv065Seed(w, "command -v draiver-evals-no-such-agent >/dev/null")
}

func drv065Seed(w *World, healthcheck string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not on PATH")
	}
	f := &drv065Fixture{}
	f.cacheDir = mustTemp(w, "drv065-cache-")
	drv047SetEnv(w, "XDG_CACHE_HOME", f.cacheDir)
	f.dataRoot = mustTemp(w, "drv065-data-")
	f.configPath = filepath.Join(mustTemp(w, "drv065-cfg-"), "config.json")

	repo := mustTemp(w, "drv065-repo-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if err := gitc(repo, args...); err != nil {
			return err
		}
	}
	wm, err := worktree.NewManager(repo)
	if err != nil {
		return err
	}
	wt, err := wm.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: drv065Ticket, Attempt: drv065Att}})
	if err != nil {
		return err
	}
	f.checkout = wt.Path
	if err := os.WriteFile(filepath.Join(f.checkout, ".test-pyramid.yaml"), []byte(fmt.Sprintf(drv065Pyramid, healthcheck)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "pyramid with a stub evals rung"}} {
		if err := gitcAcc(f.checkout, args...); err != nil {
			return err
		}
	}

	root := store.Root{Dir: f.dataRoot}
	if err := root.EnsureAttemptDirs(drv065Ticket, drv065Att); err != nil {
		return err
	}
	spec := "---\nid: " + drv065Ticket + "\ntitle: drv-065 evals rung\n---\n\n# drv-065 evals rung\n"
	if err := os.WriteFile(root.SpecPath(drv065Ticket), []byte(spec), 0o644); err != nil {
		return err
	}
	meta := "---\nid: " + drv065Att + "\nticket: " + drv065Ticket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(drv065Ticket, drv065Att), []byte(meta), 0o644); err != nil {
		return err
	}
	if _, err := ticketlog.Append(root, drv065Ticket, drv065Att, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return err
	}
	w.set("drv065", f)
	return nil
}

func drv065Get(w *World) (*drv065Fixture, error) {
	f, _ := w.get("drv065").(*drv065Fixture)
	if f == nil {
		return nil, fmt.Errorf("no drv-065 evals codebase set up in scope")
	}
	return f, nil
}

func drv065Run(w *World, args ...string) error {
	f, err := drv065Get(w)
	if err != nil {
		return err
	}
	bin, err := overlayBin()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = f.checkout
	cmd.Env = drv047OnlyChildEnv(&drv047Only{dataRoot: f.dataRoot, configPath: f.configPath, cacheDir: f.cacheDir})
	out, runErr := cmd.CombinedOutput()
	f.out, f.err = string(out), runErr
	return nil
}

func drv065WhenBare(w *World, sr *StepRun) error { return drv065Run(w, "test") }
func drv065WhenOnly(w *World, sr *StepRun) error { return drv065Run(w, "test", "evals", "--only") }

func drv065Trail(f *drv065Fixture) []string {
	raw, _ := os.ReadFile(filepath.Join(f.checkout, drv065Trace))
	return strings.Fields(string(raw))
}

func drv065ThenBareSkipsEvals(w *World, sr *StepRun) error {
	f, err := drv065Get(w)
	if err != nil {
		return err
	}
	attachText(sr, "bare-draiver-test", f.out)
	if f.err != nil {
		return fmt.Errorf("bare `draiver test` failed: %v\n%s", f.err, f.out)
	}
	if got := strings.Join(drv065Trail(f), ","); got != "unit" {
		return fmt.Errorf("rungs run by a bare climb = %q, want only \"unit\"\n%s", got, f.out)
	}
	if strings.Contains(f.out, "== evals") || strings.Contains(f.out, "evals/agent") {
		return fmt.Errorf("bare climb touched the evals rung or its environment:\n%s", f.out)
	}
	if !strings.Contains(f.out, `all green through "unit"`) {
		return fmt.Errorf("bare climb should stop at the highest required rung (unit):\n%s", f.out)
	}
	return nil
}

func drv065ThenOnlyRan(w *World, sr *StepRun) error {
	f, err := drv065Get(w)
	if err != nil {
		return err
	}
	attachText(sr, "draiver-test-evals-only", f.out)
	if f.err != nil {
		return fmt.Errorf("`draiver test evals --only` exited non-zero: %v\n%s", f.err, f.out)
	}
	if got := strings.Join(drv065Trail(f), ","); got != "evals" {
		return fmt.Errorf("rungs run by --only = %q, want only \"evals\"\n%s", got, f.out)
	}
	if !strings.Contains(f.out, "healthcheck claude-cli") {
		return fmt.Errorf("the evals rung did not go through its agent environment's healthcheck:\n%s", f.out)
	}
	return nil
}

func drv065ThenReportCaptured(w *World, sr *StepRun) error {
	f, err := drv065Get(w)
	if err != nil {
		return err
	}
	artDir := store.Root{Dir: f.dataRoot}.ArtefactsDir(drv065Ticket, drv065Att)
	var found []string
	_ = filepath.WalkDir(artDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "report.json" {
			found = append(found, p)
		}
		return nil
	})
	if len(found) == 0 {
		return fmt.Errorf("no evals report.json captured under %s\n%s", artDir, f.out)
	}
	body, err := os.ReadFile(found[0])
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), `"stub":true`) {
		return fmt.Errorf("captured report is not the rung's report: %s", body)
	}
	rel, _ := filepath.Rel(artDir, found[0])
	attachText(sr, "captured-evals-report", "artefacts/"+rel+"\n"+string(body))
	return nil
}

func drv065ThenNoGate(w *World, sr *StepRun) error {
	f, err := drv065Get(w)
	if err != nil {
		return err
	}
	events, err := ticketlog.Read(store.Root{Dir: f.dataRoot}, drv065Ticket, drv065Att)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if ev.Type == "test-result" {
			return fmt.Errorf("`test evals --only` recorded a test-result gate event (#%d)", ev.Seq)
		}
	}
	return nil
}

func drv065ThenBlocked(w *World, sr *StepRun) error {
	f, err := drv065Get(w)
	if err != nil {
		return err
	}
	attachText(sr, "evals-blocked", f.out)
	if f.err == nil {
		return fmt.Errorf("a red agent healthcheck must not let the evals rung pass:\n%s", f.out)
	}
	if ee, ok := f.err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
		return fmt.Errorf("want exit 7 (ExitBlocked: dependency not ready), got %v\n%s", f.err, f.out)
	}
	if !strings.Contains(f.out, "dependency not ready") {
		return fmt.Errorf("the block does not explain the dependency is not ready:\n%s", f.out)
	}
	if trail := drv065Trail(f); len(trail) != 0 {
		return fmt.Errorf("rungs ran despite the block: %v", trail)
	}
	return nil
}

// ---- this repository's own declaration ------------------------------------------

func drv065GivenRepoPyramid(w *World, sr *StepRun) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	// The suite runs from the repo root (the rung) or internal/acceptance (go test):
	// walk up to the module root.
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
	p, err := pyramid.Load(dir)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("this repository has no %s", pyramid.FileName)
	}
	w.set("drv065-pyramid", p)
	return nil
}

func drv065RepoEvals(w *World) (*pyramid.Pyramid, pyramid.Level, error) {
	p, _ := w.get("drv065-pyramid").(*pyramid.Pyramid)
	if p == nil {
		return nil, pyramid.Level{}, fmt.Errorf("repository pyramid not loaded")
	}
	for _, lv := range p.Levels {
		if lv.Name == "evals" {
			return p, lv, nil
		}
	}
	return nil, pyramid.Level{}, fmt.Errorf("this repository's pyramid has no evals level")
}

func drv065ThenRepoEvalsAdvisory(w *World, sr *StepRun) error {
	p, lv, err := drv065RepoEvals(w)
	if err != nil {
		return err
	}
	if top := p.Levels[len(p.Levels)-1].Name; top != "evals" {
		return fmt.Errorf("top level = %q, want evals", top)
	}
	if lv.IsRequired() {
		return fmt.Errorf("the evals rung must be advisory (required: false)")
	}
	if p.HighestRequired().Name == "evals" {
		return fmt.Errorf("a bare climb would reach evals")
	}
	env, ok := p.EnvironmentFor(lv)
	if !ok {
		return fmt.Errorf("the evals rung names no environment")
	}
	var hcs []string
	needsClaude := false
	for _, hc := range env.Healthchecks {
		hcs = append(hcs, hc.Name+": "+hc.Script)
		if strings.Contains(hc.Script, "claude") {
			needsClaude = true
		}
	}
	if !needsClaude {
		return fmt.Errorf("environment %q has no healthcheck requiring the claude CLI: %v", env.Name, hcs)
	}
	attachText(sr, "repo-evals-rung", fmt.Sprintf("level: %s\nrun: %s\nrequired: false\nenvironment: %s\nup: %s\nhealthchecks:\n  %s\ndefault climb target: %s",
		lv.Name, lv.Run, env.Name, env.Up, strings.Join(hcs, "\n  "), p.HighestRequired().Name))
	return nil
}

func drv065ThenRepoEvalsArtifact(w *World, sr *StepRun) error {
	_, lv, err := drv065RepoEvals(w)
	if err != nil {
		return err
	}
	if len(lv.Artifacts) == 0 {
		return fmt.Errorf("the evals rung declares no artifacts")
	}
	attachText(sr, "repo-evals-artifacts", strings.Join(lv.Artifacts, "\n"))
	return nil
}
