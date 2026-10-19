package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/bddartefact"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// testLog switches on the durable-record mode: refuse a dirty tree, run, and on
// all-green append one hash-chained test-result event. Bare (off) is the agent's
// fast iterative loop — run + report only, no write, works against a dirty tree.
var testLog bool

var testCmd = &cobra.Command{
	Use:   "test [RUNG]",
	Short: "Run the worktree's test pyramid; --log records a passing result",
	Long: "test is the executor for a repo's .test-pyramid.yaml. Run from inside an\n" +
		"attempt's worktree — the checkout is the code under test, and test derives\n" +
		"which attempt from the branch it stands on. It climbs from the base rung up to\n" +
		"RUNG, running each level's command. RUNG defaults to the highest `required`\n" +
		"rung (what review needs); a per-repo default_test_rung narrows that ceiling\n" +
		"down for a faster loop, and an explicit RUNG can climb up into advisory rungs.\n" +
		"\n" +
		"The climb halts at the first non-green *required* rung; a red advisory rung\n" +
		"(required: false) is reported and the climb continues past it, so it never\n" +
		"blocks a higher required rung or the review gate.\n" +
		"\n" +
		"Bare, it only runs and reports — the fast iterative loop, fine against a dirty\n" +
		"tree. With --log it refuses a dirty tree, then on a green target through RUNG\n" +
		"appends one durable test-result event carrying the rung and the HEAD commit —\n" +
		"so brief replays \"rung green @ sha\" for the next agent. A failing run logs\n" +
		"nothing.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}

		p, err := pyramid.Load(wd)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if p == nil {
			// A repo that declares no pyramid is a valid state, not an error: there is
			// simply nothing to run.
			fmt.Fprintf(out, "no %s in %s — nothing to test\n", pyramid.FileName, wd)
			return nil
		}

		// The default climb target is the highest *required* rung (drv-025): what the
		// review gate ultimately needs, which may sit below the file's top rung when
		// advisory rungs crown the ladder. The per-repo default_test_rung (drv-011),
		// when it names a real rung, narrows that ceiling *down* for a faster local
		// loop — it is purely the iteration ceiling now and never touches the gate.
		// Resolved best-effort from the worktree (branch → attempt → recorded repo →
		// config); any failure leaves the highest required rung, so `test` never breaks
		// on a config/identify error. An explicit RUNG wins over both and can climb
		// *up* into advisory rungs on demand.
		target := p.HighestRequired().Name
		if rr := repoRungForWorktree(ctx, wd); rr != "" {
			if _, ok := levelsUpTo(p, rr); ok {
				target = rr
			}
		}
		if len(args) == 1 {
			target = args[0]
		}
		levels, ok := levelsUpTo(p, target)
		if !ok {
			return fmt.Errorf("rung %q is not a level in %s (levels: %s)", target, pyramid.FileName, rungNames(p))
		}

		// --log demands a commit that fully captures the tree under test, so refuse a
		// dirty tree up front — before running anything — rather than run and then find
		// there is no clean commit to attribute the result to.
		if testLog {
			dirty, err := worktree.DirtyAt(ctx, wd)
			if err != nil {
				return err
			}
			if dirty {
				return fmt.Errorf("working tree is dirty: commit before `test --log` so the recorded result names a commit that captures the tree tested")
			}
		}

		// --detach hands the climb off to an out-of-band runner and returns at once, so
		// the agent's session is not held idle while the test runs (drvctl-055). It
		// records a launch marker, fires the detached runner, and exits 0 — the worker's
		// `&& draiver park` then suspends it until the result lands.
		if testDetach {
			return launchDetached(ctx, cmd, wd, target, levels)
		}

		// Climb base→target, streaming each rung's output. Each rung runs in its
		// environment (if it names one): up → healthchecks → run → (always) down.
		//
		// The climb halts only at the first non-green *required* rung (drv-025): its
		// failure maps to its control outcome (infra→escalate, dependency→block,
		// code→fault) via runRung's exit code, and higher rungs never run. A red
		// *advisory* rung — for any reason, a genuine failure or an unready environment
		// — is reported and the climb CONTINUES past it: higher suites on the V do not
		// depend on lower suites *passing*, and otherwise a required rung above an
		// advisory one would be unreachable and the gate unsatisfiable. A failing rung
		// logs nothing either way — the log holds only passing results by construction.
		targetGreen, advisoryReds, failErr := climb(ctx, cmd, wd, p, levels)
		if failErr != nil {
			return failErr
		}
		switch {
		case len(advisoryReds) == 0:
			fmt.Fprintf(out, "all green through %q\n", target)
		case targetGreen:
			fmt.Fprintf(out, "green through %q — advisory rung(s) not green, ignored: %s\n", target, strings.Join(advisoryReds, ", "))
		default:
			// The explicitly requested target rung is itself advisory and not green:
			// the climb ran everything but has no green target to attribute.
			fmt.Fprintf(out, "advisory rung(s) not green, ignored: %s\n", strings.Join(advisoryReds, ", "))
		}

		if !testLog {
			return nil
		}

		// --log records the target rung, so it needs the target itself green. A required
		// target that failed already returned above; only an advisory target left red
		// reaches here, and there is no green result to attribute — record nothing.
		if !targetGreen {
			fmt.Fprintf(out, "target rung %q not green — nothing recorded\n", target)
			return nil
		}

		// All green and the tree was clean: attribute the result to the exact commit
		// that was tested and append the one durable event.
		sha, err := worktree.HeadSHA(ctx, wd)
		if err != nil {
			return err
		}
		key, err := worktree.Identify(ctx, wd)
		if err != nil {
			return err
		}
		root, err := resolveRoot()
		if err != nil {
			return err
		}

		// If the target rung declares output artefacts (a BDD/acceptance rung), capture
		// them into the attempt's artefacts/ store under a per-run key and reference the
		// set from the test-result event, so the evidence is provenance-anchored and
		// audit-covered (drv-017). An ordinary rung declares none and this is a no-op.
		arts, runKey, err := captureBDDArtefacts(out, root, key, p.Levels[len(levels)-1], sha)
		if err != nil {
			return err
		}

		ev, _, err := appendEventAt(root, key.Ticket, key.Attempt, event.Event{
			Type:      "test-result",
			Rung:      target,
			Commit:    sha,
			Artefacts: arts,
			Body:      testResultBody(target, sha, runKey),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "recorded test-result #%d on %s/%s (rung %q @ %s)\n", ev.Seq, key.Ticket, key.Attempt, target, shortSHA(sha))
		return nil
	},
}

// captureBDDArtefacts captures a BDD/acceptance rung's declared output evidence into
// the attempt's artefacts/ store under a per-run key, enforces the configured
// retention cap, and returns the artefacts-relative refs for the test-result event
// plus the run key (empty when the rung declares no evidence — the ordinary case).
//
// The evidence is the rung's CucumberJSON report (drv-016's first-class field) plus
// any additional Artifacts it declares (embeddings/screenshots dir, script-output
// files). A rung is a capture rung if it declares either, so a BDD rung that only
// sets cucumber_json still has its report stored. runRung already consumes the
// report live for its summary; this captures it durably once the run is green.
//
// It is best-effort on context that later tickets fill in: the environment is read
// as a bare label (drv-012 lifecycle pending) and the healthcheck verdict is left
// blank until environments land.
func captureBDDArtefacts(out io.Writer, root store.Root, key worktree.Key, target pyramid.Level, sha string) ([]string, string, error) {
	cucumber := strings.TrimSpace(target.CucumberJSON)
	if cucumber == "" && len(target.Artifacts) == 0 {
		return nil, "", nil
	}
	// The cucumber-JSON leads the set (when declared), then the rung's extra outputs.
	var sources []bddartefact.Source
	if cucumber != "" {
		sources = append(sources, bddartefact.Source{Path: cucumber})
	}
	for _, a := range target.Artifacts {
		sources = append(sources, bddartefact.Source{Path: a})
	}
	rc := bddartefact.RunContext{
		Rung:        target.Name,
		Environment: target.Environment,
		Commit:      sha,
		Runstamp:    time.Now().UTC().Format("20060102T150405Z"),
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("resolve working directory: %w", err)
	}
	artDir := root.ArtefactsDir(key.Ticket, key.Attempt)
	runKey, refs, err := bddartefact.Capture(artDir, wd, rc, sources)
	if err != nil {
		return nil, "", fmt.Errorf("capture BDD artefacts: %w", err)
	}
	fmt.Fprintf(out, "captured %d BDD artefact(s) under %s\n", len(refs), runKey)

	cfg, err := config.Load("")
	if err != nil {
		return nil, "", err
	}
	removed, err := bddartefact.Prune(artDir, rc.Rung, rc.Env(), cfg.BDDArtefactKeep)
	if err != nil {
		return nil, "", fmt.Errorf("prune BDD artefacts: %w", err)
	}
	if len(removed) > 0 {
		fmt.Fprintf(out, "pruned %d old BDD run(s) (keep=%d)\n", len(removed), cfg.BDDArtefactKeep)
	}
	return refs, runKey, nil
}

// testResultBody renders the test-result event body, noting the captured run key
// when a BDD rung produced artefacts.
func testResultBody(target, sha, runKey string) string {
	if runKey == "" {
		return fmt.Sprintf("Rung `%s` green @ `%s`.", target, shortSHA(sha))
	}
	return fmt.Sprintf("Rung `%s` green @ `%s`. BDD artefacts captured under `%s`.", target, shortSHA(sha), runKey)
}

// climb runs the rungs base→target in order and reports the outcome without
// deciding what to do about it — the shared engine behind the synchronous `draiver
// test`, `--log`, and the detached runner (drvctl-055). It returns whether the
// target rung itself came up green, the advisory rungs that were red (reported but
// non-gating, drv-025), and failErr: the control-coded error of the first red
// *required* rung, at which point the climb halts and higher rungs never run.
//
// A caller that must preserve exit-code semantics (the synchronous CLI) returns
// failErr verbatim; the detached runner instead swallows it and records a failing
// result, since its whole point is to land a verdict on the log rather than exit.
func climb(ctx context.Context, cmd *cobra.Command, wd string, p *pyramid.Pyramid, levels []pyramid.Level) (targetGreen bool, advisoryReds []string, failErr error) {
	out := cmd.OutOrStdout()
	for i, lv := range levels {
		err := runRung(ctx, cmd, wd, p, lv)
		if err == nil {
			if i == len(levels)-1 {
				targetGreen = true
			}
			continue
		}
		if lv.IsRequired() {
			return false, advisoryReds, err
		}
		advisoryReds = append(advisoryReds, lv.Name)
		fmt.Fprintf(out, "== %s: advisory rung not green — recorded, climb continues (does not gate review): %v\n", lv.Name, err)
	}
	return targetGreen, advisoryReds, nil
}

// runRung executes one rung in its environment, mapping each kind of failure to
// its control outcome (drv-012). Order: env `up` → the env's healthchecks → the
// level's `run`, with the env's `down` always run afterwards if the env was
// entered. A rung with no environment is just its `run`, exactly as before.
//
//   - `up` non-zero → the harness could not stand the env up: an infrastructure
//     fault, not the agent's code → ExitEnvUp (escalate).
//   - a red healthcheck → a dependency the env only observes isn't ready → a block,
//     not a code fault → ExitBlocked (the pass-the-ball signal).
//   - `run` non-zero → the code is wrong at this rung → exit 1 (a plain error).
//
// `down` runs unconditionally once `up` has been attempted (the env may be partly
// up even when `up` failed) and is best-effort: a non-zero `down` is a warning and
// never changes the rung's verdict, so teardown trouble can never un-close a
// ticket.
func runRung(ctx context.Context, cmd *cobra.Command, wd string, p *pyramid.Pyramid, lv pyramid.Level) error {
	out := cmd.OutOrStdout()
	env, hasEnv := p.EnvironmentFor(lv)

	if hasEnv && strings.TrimSpace(env.Down) != "" {
		// Guaranteed teardown: scheduled before `up` runs so a failed/partial `up`
		// still gets torn down. Best-effort — a `down` failure is only warned about.
		defer func() {
			fmt.Fprintf(out, "== %s/%s: down: %s\n", lv.Name, env.Name, env.Down)
			if err := runShell(ctx, cmd, wd, env.Down); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: environment %q down failed (ignored): %v\n", env.Name, err)
			}
		}()
	}

	if hasEnv && strings.TrimSpace(env.Up) != "" {
		fmt.Fprintf(out, "== %s/%s: up: %s\n", lv.Name, env.Name, env.Up)
		if err := runShell(ctx, cmd, wd, env.Up); err != nil {
			return &exitError{code: ExitEnvUp, msg: fmt.Sprintf("environment %q up failed — infrastructure fault, not your code; escalate: %v", env.Name, err)}
		}
	}

	if hasEnv {
		for _, hc := range env.Healthchecks {
			fmt.Fprintf(out, "== %s/%s: healthcheck %s: %s\n", lv.Name, env.Name, hc.Name, hc.Script)
			if err := runShell(ctx, cmd, wd, hc.Script); err != nil {
				return &exitError{code: ExitBlocked, msg: fmt.Sprintf("environment %q healthcheck %q red — dependency not ready (blocked, not a code fault): %v", env.Name, hc.Name, err)}
			}
		}
	}

	fmt.Fprintf(out, "== %s: %s\n", lv.Name, lv.Run)
	runErr := runShell(ctx, cmd, wd, lv.Run)

	// A BDD/acceptance rung (drv-016) declares the cucumber-JSON its runner writes.
	// Capture it whether the run passed or failed — a red run's report is exactly
	// what drv-018 renders to show which scenarios broke — but never let capture
	// change the rung's verdict (best-effort, like `down`): the run's exit code
	// stays authoritative.
	if cj := strings.TrimSpace(lv.CucumberJSON); cj != "" {
		captureCucumber(cmd, wd, lv.Name, cj)
	}

	if runErr != nil {
		return fmt.Errorf("rung %q not green: %w", lv.Name, runErr)
	}
	return nil
}

// captureCucumber reads, parses, and summarises the cucumber-JSON a BDD rung's run
// emitted at rel (relative to wd), printing a one-line summary — draiver consuming
// the runner-agnostic interchange, not the tool. It is best-effort: a missing or
// invalid report is a warning that never changes the rung's verdict, so a green run
// whose runner forgot to write the report still passes (loudly). Durable storage of
// the report/embeddings is drv-017's concern; this only proves the JSON is present
// and consumable and surfaces what it contains.
func captureCucumber(cmd *cobra.Command, wd, rung, rel string) {
	path := filepath.Join(wd, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: rung %q cucumber-JSON %q not captured (ignored): %v\n", rung, rel, err)
		return
	}
	rep, err := cucumber.Parse(data)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: rung %q cucumber-JSON %q not valid (ignored): %v\n", rung, rel, err)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "== %s: captured cucumber-JSON %s — %s\n", rung, rel, rep.Summary())
}

// runShell runs one shell command in wd, wiring its stdio to the cobra command's
// streams — the single exec seam every rung step (up/healthcheck/run/down) goes
// through, so output ordering and interpretation are identical across them.
func runShell(ctx context.Context, cmd *cobra.Command, wd, script string) error {
	rc := exec.CommandContext(ctx, "sh", "-c", script)
	rc.Dir = wd
	rc.Stdout = cmd.OutOrStdout()
	rc.Stderr = cmd.ErrOrStderr()
	rc.Stdin = os.Stdin
	return rc.Run()
}

// repoRungForWorktree resolves the repo's configured default_test_rung (drv-011)
// for the attempt the worktree at wd belongs to, best-effort: it maps the checkout's
// branch to an attempt (worktree.Identify), reads that attempt's recorded repo, and
// resolves the per-repo setting through the gateway. Since drv-025 this is purely the
// `draiver test` iteration ceiling — it narrows the default climb *down* and never
// touches the review gate. It returns "" — leaving the caller on the highest required
// rung — on any failure (a non-attempt branch, a missing attempt, an unreadable
// config), so the fast `test` loop never breaks on config.
func repoRungForWorktree(ctx context.Context, wd string) string {
	key, err := worktree.Identify(ctx, wd)
	if err != nil {
		return ""
	}
	root, err := resolveRoot()
	if err != nil {
		return ""
	}
	meta, err := attempt.LoadMeta(root, key.Ticket, key.Attempt)
	if err != nil {
		return ""
	}
	rs, err := repo.New(root, resolveActor()).RepoSettings(meta.Repo)
	if err != nil {
		return ""
	}
	return rs.TestRung
}

// levelsUpTo returns the rungs from the base up to and including the one named
// target, in climb order. ok is false when no rung carries that name.
func levelsUpTo(p *pyramid.Pyramid, target string) ([]pyramid.Level, bool) {
	for i, lv := range p.Levels {
		if lv.Name == target {
			return p.Levels[:i+1], true
		}
	}
	return nil, false
}

// rungNames lists a pyramid's rung names in climb order, for an error that names
// what the caller could have asked for.
func rungNames(p *pyramid.Pyramid) string {
	names := make([]string, len(p.Levels))
	for i, lv := range p.Levels {
		names[i] = lv.Name
	}
	return strings.Join(names, ", ")
}

func init() {
	testCmd.Flags().BoolVar(&testLog, "log", false, "on all-green, append a durable test-result event (refuses a dirty tree)")
	testCmd.Flags().BoolVar(&testDetach, "detach", false, "run the climb out-of-band and land a durable pass/fail result on the attempt's log; returns at once so the session can `park` (drvctl-055)")
	rootCmd.AddCommand(testCmd)
}
