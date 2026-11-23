package acceptance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// steps_focus_drv063.go realises drv-063's acceptance criteria
// (features/acceptance-focus/): focused acceptance runs (`draiver test acceptance
// --tags/--dir`, the runner's --name) and the review refusal that names the failing
// rung/test and whether it was inherited from main. Each scenario stands up an
// isolated codebase attempt with ovSeedCodebase and shells the real draiver binary.

const (
	d63MainTicket = "MAIN-1"
	d63MainAtt    = "0001"
)

func init() {
	for text, fn := range map[string]StepFunc{
		"a drv-063 scratch attempt whose acceptance rung is the real runner over two tagged capability folders": d63GivenFocusAttempt,
		"the drv-063 focus probe passes":                               d63ProbePasses,
		"I run draiver test acceptance focused on the drv-946 tag":     d63Draiver("test", "acceptance", "--tags", "@drv-946"),
		"I run draiver test acceptance focused on the beta folder":     d63Draiver("test", "acceptance", "--dir", "features/beta"),
		"I run draiver test acceptance focused on the drv-946 tag with --log": d63Draiver("test", "acceptance", "--tags", "@drv-946", "--log"),
		`I run the acceptance runner directly with a name filter for "shown"`: d63WhenRunnerName,
		"only the drv-946 scenarios ran":                                       d63ThenRan("Alpha plain", "Alpha shown in the UI"),
		"only the beta folder scenarios ran":                                   d63ThenRan("Beta shown in the UI", "Beta silent"),
		"only the scenarios named shown ran":                                   d63ThenRan("Alpha shown in the UI", "Beta shown in the UI"),
		"the lower unit rung did not run":                                      d63ThenUnitNotRun,
		"the drv-063 attempt has no test-result recorded":                      d63ThenNoResult,
		"the focused run is refused because it can never be a gate result":     d63ThenFocusRefused,

		"a drv-063 attempt whose unit rung fails TestInheritedRed":                    d63GivenRedAttempt,
		"a detached climb has recorded the red unit rung":                             d63GivenDetachedRed,
		"main's own attempt recorded the unit rung red at the merge-base":             d63GivenMainResult(false),
		"main's own attempt recorded the acceptance rung green at the merge-base":     d63GivenMainResult(true),
		"I claim review on the drv-063 attempt":                                       d63Draiver("review", ovTicket, "ready"),
		"the review is refused naming rung unit and failing test TestInheritedRed":    d63ThenRefusedWith(`rung "unit": failing TestInheritedRed`),
		"the refusal says the acceptance rung was not reached":                        d63ThenRefusalHas(`rung "acceptance": not reached — the climb halted at red rung "unit"`),
		"the refusal says the red rung is inherited from main":                        d63ThenRefusalHas("Inherited from main, not introduced by this branch"),
		"the refusal says this branch introduced the failure":                         d63ThenRefusalHas("This branch introduced the failure"),
	} {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

// d63RunnerBin builds the real acceptance runner (./features/acceptance) once per run,
// so a scratch attempt's acceptance rung executes the same runner and registry the
// repo's own rung does.
var d63RunnerBin = sync.OnceValues(func() (string, error) {
	root, err := ovRepoRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(root, "features", "_artifacts", ".bin", "acceptance-runner")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", bin, "./features/acceptance")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build acceptance runner: %w\n%s", err, stderr.String())
	}
	return bin, nil
})

// d63Features are the scratch attempt's two capability folders. Every scenario runs
// the one probe step, so what ran is read straight off the emitted cucumber-JSON.
var d63Features = map[string]string{
	"features/alpha/alpha.feature": `@drv-946
Feature: Alpha

  Scenario: Alpha plain
    Given the drv-063 focus probe passes

  @ui
  Scenario: Alpha shown in the UI
    Given the drv-063 focus probe passes
`,
	"features/beta/beta.feature": `@drv-947
Feature: Beta

  @ui
  Scenario: Beta shown in the UI
    Given the drv-063 focus probe passes

  Scenario: Beta silent
    Given the drv-063 focus probe passes
`,
}

func d63ProbePasses(w *World, sr *StepRun) error {
	attachText(sr, "drv-063-probe", "the focus probe ran")
	return nil
}

func d63GivenFocusAttempt(w *World, sr *StepRun) error {
	bin, err := d63RunnerBin()
	if err != nil {
		return err
	}
	pyr := fmt.Sprintf(`levels:
  - name: unit
    run: "echo UNIT_RAN; false"
  - name: acceptance
    run: %q
    cucumber_json: features/_artifacts/cucumber.json
`, bin)
	o, err := ovSeedCodebase(w, pyr)
	if err != nil {
		return err
	}
	for rel, body := range d63Features {
		p := filepath.Join(o.checkout, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// d63Draiver shells the real draiver binary in the attempt's checkout and keeps its
// combined output and error for the Then steps, attaching the output as evidence.
func d63Draiver(args ...string) StepFunc {
	return func(w *World, sr *StepRun) error {
		o, err := ovGet(w)
		if err != nil {
			return err
		}
		o.runOut, o.runErr = ovRunDraiver(o, args...)
		attachText(sr, "draiver-"+strings.Join(args, "-"), fmt.Sprintf("$ draiver %s\n%s\n(exit: %v)", strings.Join(args, " "), o.runOut, o.runErr))
		return nil
	}
}

func d63WhenRunnerName(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	bin, err := d63RunnerBin()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "--name", "shown")
	cmd.Dir = o.checkout
	cmd.Env = ovChildEnv(o)
	out, err := cmd.CombinedOutput()
	o.runOut, o.runErr = string(out), err
	attachText(sr, "acceptance-runner-name", fmt.Sprintf("$ go run ./features/acceptance --name shown\n%s\n(exit: %v)", out, err))
	return nil
}

// d63ThenRan asserts the scratch run's cucumber-JSON holds exactly want.
func d63ThenRan(want ...string) StepFunc {
	return func(w *World, sr *StepRun) error {
		o, err := ovGet(w)
		if err != nil {
			return err
		}
		if o.runErr != nil {
			return fmt.Errorf("focused run failed: %v\n%s", o.runErr, o.runOut)
		}
		data, err := os.ReadFile(filepath.Join(o.checkout, "features", "_artifacts", "cucumber.json"))
		if err != nil {
			return err
		}
		rep, err := cucumber.Parse(data)
		if err != nil {
			return err
		}
		var got []string
		for _, f := range rep {
			for _, el := range f.Elements {
				got = append(got, el.Name)
			}
		}
		sort.Strings(got)
		sort.Strings(want)
		attachText(sr, "scenarios-ran", strings.Join(got, "\n"))
		if strings.Join(got, "|") != strings.Join(want, "|") {
			return fmt.Errorf("ran %q, want %q", got, want)
		}
		if !strings.Contains(o.runOut, "focused run") {
			return fmt.Errorf("output does not announce a focused run:\n%s", o.runOut)
		}
		return nil
	}
}

func d63ThenUnitNotRun(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	if strings.Contains(o.runOut, "UNIT_RAN") {
		return fmt.Errorf("the focused run climbed into the unit rung:\n%s", o.runOut)
	}
	return nil
}

func d63ThenNoResult(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	events, err := ticketlog.Read(store.Root{Dir: o.dataRoot}, ovTicket, ovAtt)
	if err != nil {
		return err
	}
	var types []string
	for _, e := range events {
		types = append(types, fmt.Sprintf("#%d %s", e.Seq, e.Type))
		if e.Type == "test-result" {
			return fmt.Errorf("a focused run recorded test-result #%d", e.Seq)
		}
	}
	attachText(sr, "attempt-log", "events on "+ovTicket+"/"+ovAtt+":\n"+strings.Join(types, "\n"))
	return nil
}

func d63ThenFocusRefused(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	if o.runErr == nil {
		return fmt.Errorf("focused --log was accepted:\n%s", o.runOut)
	}
	if !strings.Contains(o.runOut, "never be a gate result") {
		return fmt.Errorf("refusal does not say why:\n%s", o.runOut)
	}
	if strings.Contains(o.runOut, "== acceptance") {
		return fmt.Errorf("the rung ran before the refusal:\n%s", o.runOut)
	}
	return nil
}

// ---- review refusal ----

func d63GivenRedAttempt(w *World, sr *StepRun) error {
	_, err := ovSeedCodebase(w, `levels:
  - name: unit
    run: "echo '--- FAIL: TestInheritedRed (0.01s)'; false"
  - name: acceptance
    run: "true"
`)
	return err
}

// d63GivenDetachedRed runs the real detached runner synchronously (what `draiver test
// --detach` spawns), so the fail result carries failed_rung/failed_tests exactly as
// production records them.
func d63GivenDetachedRed(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	out, _ := ovRunDraiver(o, "__detached-test", "--ticket", ovTicket, "--attempt", ovAtt,
		"--rung", "acceptance", "--workdir", o.checkout, "--launch-seq", "1", "--logrel", "detached/drv063.log")
	events, err := ticketlog.Read(store.Root{Dir: o.dataRoot}, ovTicket, ovAtt)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e.Type == "test-result" && e.Outcome == "fail" && e.FailedRung == "unit" {
			return nil
		}
	}
	return fmt.Errorf("the detached climb recorded no failing unit result:\n%s", out)
}

// d63GivenMainResult records a result on a second attempt (standing in for the one
// that tested main) at the merge-base of the drv-063 attempt with main.
func d63GivenMainResult(green bool) StepFunc {
	return func(w *World, sr *StepRun) error {
		o, err := ovGet(w)
		if err != nil {
			return err
		}
		mb, err := exec.Command("git", "-C", o.checkout, "merge-base", "HEAD", "main").Output()
		if err != nil {
			return fmt.Errorf("merge-base: %w", err)
		}
		root := store.Root{Dir: o.dataRoot}
		if err := root.EnsureAttemptDirs(d63MainTicket, d63MainAtt); err != nil {
			return err
		}
		spec := "---\nid: " + d63MainTicket + "\ntitle: main\n---\n\n# main\n"
		if err := os.WriteFile(root.SpecPath(d63MainTicket), []byte(spec), 0o644); err != nil {
			return err
		}
		meta := "---\nid: " + d63MainAtt + "\nticket: " + d63MainTicket + "\nrepo: " + o.repoDir + "\nbase: main\n---\n\n"
		if err := os.WriteFile(root.AttemptMetaPath(d63MainTicket, d63MainAtt), []byte(meta), 0o644); err != nil {
			return err
		}
		e := event.Event{Type: "test-result", Actor: "agent:acceptance", Rung: "acceptance", Commit: strings.TrimSpace(string(mb)), Body: "main green"}
		if !green {
			e.Outcome, e.FailedRung, e.FailedTests, e.Body = "fail", "unit", []string{"TestInheritedRed"}, "main red"
		}
		_, err = ticketlog.Append(root, d63MainTicket, d63MainAtt, e)
		return err
	}
}

func d63ThenRefusedWith(want string) StepFunc {
	return func(w *World, sr *StepRun) error {
		o, err := ovGet(w)
		if err != nil {
			return err
		}
		if o.runErr == nil {
			return errors.New("review was admitted, want a refusal")
		}
		return d63ThenRefusalHas(want)(w, sr)
	}
}

func d63ThenRefusalHas(want string) StepFunc {
	return func(w *World, sr *StepRun) error {
		o, err := ovGet(w)
		if err != nil {
			return err
		}
		if !strings.Contains(o.runOut, want) {
			return fmt.Errorf("refusal missing %q:\n%s", want, o.runOut)
		}
		return nil
	}
}
