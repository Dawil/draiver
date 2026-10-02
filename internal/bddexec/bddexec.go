// Package bddexec is the in-process controlplane executor the board's "rerun"
// affordance (drv-019) delegates to. It re-executes a repo's bound BDD rung over
// its environment (drv-012: up → healthcheck → run → down) and captures a fresh
// side-by-side artefact set (drv-017 bddartefact), so a human can regenerate the
// evidence a report renders (drv-018) without leaving the board.
//
// It is the BDD counterpart to internal/land's git-over-the-gateway seam: the web
// layer never runs the BDD tooling itself — it calls Rerun, exactly as the git
// controls call land.Push/land.Sync. The healthcheck precondition lives here, not
// in the handler, so a red environment yields StatusBlocked ("pass the ball")
// rather than a bogus report.
//
// Scope note: `draiver test`'s climb (cmd/test.go) captures once, post-climb,
// gated on all-green + --log; a rerun instead executes a single rung and captures
// its output every time. The control flow genuinely differs, so this re-expresses
// the thin up/healthcheck/run/capture orchestration rather than sharing cmd's — the
// reuse is at the building-block level (pyramid, bddartefact). Converging cmd onto
// this is worth a follow-up once drv-016/017/018 settle on main.
package bddexec

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/bddartefact"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// Status is the verdict of a rerun, mirroring `draiver test`'s exit-code taxonomy
// (cmd/root.go): a red healthcheck blocks (not a code fault), a failed env `up` is
// infrastructure, a failed run is a code fault, and "no BDD rung" means there is
// nothing to rerun.
type Status string

const (
	// StatusOK: the bound rung ran green and a fresh artefact set was captured.
	StatusOK Status = "ok"
	// StatusBlocked: a healthcheck was red — the environment is not ready, so the
	// rerun was refused before running (pass the ball), no report produced.
	StatusBlocked Status = "blocked"
	// StatusEnvFault: the environment's `up` failed — an infrastructure fault.
	StatusEnvFault Status = "env-fault"
	// StatusRunFailed: the rung executed but exited non-zero (a code fault). A report
	// may still have been captured — failing evidence is evidence worth keeping.
	StatusRunFailed Status = "run-failed"
	// StatusNoRung: the pyramid exposes no BDD rung — nothing to rerun.
	StatusNoRung Status = "no-bdd-rung"
)

// Outcome is the structured result the web handler maps to a banner and an optional
// report-panel refresh. Lines are human-readable report lines (joined for display).
// RunKey is the artefacts-relative key of the newly captured run, "" when none was
// captured (blocked / env fault / no rung).
type Outcome struct {
	Status Status
	Rung   string
	Env    string
	RunKey string
	Lines  []string
}

// BoundBDDRung resolves the BDD rung a rerun would re-execute for the worktree at
// wd: the highest (closest-to-target) rung declaring a cucumber_json report, plus
// its resolved environment (nil for the ambient context). ok is false — not an
// error — when there is no pyramid or no BDD rung, so callers can cheaply gate a
// "rerun" affordance. A malformed .test-pyramid.yaml is a real error.
func BoundBDDRung(wd string) (pyramid.Level, *pyramid.Environment, bool, error) {
	p, err := pyramid.Load(wd)
	if err != nil {
		return pyramid.Level{}, nil, false, err
	}
	if p == nil {
		return pyramid.Level{}, nil, false, nil
	}
	lv, ok := lastBDDRung(p)
	if !ok {
		return pyramid.Level{}, nil, false, nil
	}
	env, _ := p.EnvironmentFor(lv)
	return lv, env, true, nil
}

// lastBDDRung returns the highest-indexed rung declaring a cucumber_json — the BDD
// rung nearest the pyramid's target, which is the one a rerun regenerates.
func lastBDDRung(p *pyramid.Pyramid) (pyramid.Level, bool) {
	for i := len(p.Levels) - 1; i >= 0; i-- {
		if strings.TrimSpace(p.Levels[i].CucumberJSON) != "" {
			return p.Levels[i], true
		}
	}
	return pyramid.Level{}, false
}

// Rerun re-executes the attempt's bound BDD rung over its environment and captures
// a fresh side-by-side artefact set. It never returns an error for an ordinary
// red/failed outcome — those are carried in the Outcome so the board can explain
// them; a non-nil error is reserved for a genuinely malformed pyramid or a capture
// I/O fault the caller should surface as a 500.
func Rerun(ctx context.Context, root store.Root, wd, ticket, att string) (Outcome, error) {
	lv, env, ok, err := BoundBDDRung(wd)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{
			Status: StatusNoRung,
			Lines:  []string{"No BDD rung in `.test-pyramid.yaml` — there is nothing to rerun."},
		}, nil
	}
	out := Outcome{Rung: lv.Name, Env: lv.Environment}

	// Bring the environment up (drv-012). A non-zero `up` is infrastructure, not code.
	if env != nil && strings.TrimSpace(env.Up) != "" {
		if err := runShell(ctx, wd, env.Up); err != nil {
			out.Status = StatusEnvFault
			out.Lines = []string{
				fmt.Sprintf("Environment %q failed to come up: %v.", env.Name, err),
				"This is an infrastructure fault, not a code failure — the rerun was not attempted.",
			}
			return out, nil
		}
		// Tear down unconditionally, best-effort, once we have brought it up.
		if strings.TrimSpace(env.Down) != "" {
			defer runShell(ctx, wd, env.Down)
		}
	}

	// Healthcheck precondition (drv-012): a red probe blocks the rerun — pass the
	// ball back rather than capture a bogus report.
	if env != nil {
		for _, hc := range env.Healthchecks {
			if err := runShell(ctx, wd, hc.Script); err != nil {
				out.Status = StatusBlocked
				out.Lines = []string{
					fmt.Sprintf("Healthcheck %q for environment %q is red — rerun blocked.", hc.Name, env.Name),
					"The bound environment is not ready; this is a dependency not up, not a code failure. Pass the ball back rather than capture a bogus report.",
				}
				return out, nil
			}
		}
	}

	// Execute the rung, then capture its output as a fresh run regardless of the
	// run's own verdict — a failing run's report is evidence worth keeping.
	runErr := runShell(ctx, wd, lv.Run)

	sha := "nocommit"
	if s, err := worktree.HeadSHA(ctx, wd); err == nil && strings.TrimSpace(s) != "" {
		sha = strings.TrimSpace(s)
	}
	runKey, refs, capErr := capture(root, wd, ticket, att, lv, sha)
	out.RunKey = runKey

	switch {
	case capErr != nil:
		// The run produced no capturable report (e.g. it never wrote cucumber_json).
		out.Status = StatusRunFailed
		out.Lines = []string{"The BDD rung ran but no report could be captured: " + capErr.Error() + "."}
		if runErr != nil {
			out.Lines = append(out.Lines, "The run also exited non-zero: "+runErr.Error()+".")
		}
		out.RunKey = ""
	case runErr != nil:
		out.Status = StatusRunFailed
		out.Lines = []string{
			fmt.Sprintf("Reran `%s` on %s @ %s — the run failed (%v).", lv.Name, envLabel(env), short(sha), runErr),
			fmt.Sprintf("Captured the failing run (%d artefact(s)) under `%s`; the refreshed report shows the failures.", len(refs), runKey),
		}
	default:
		out.Status = StatusOK
		out.Lines = []string{
			fmt.Sprintf("Reran `%s` on %s @ %s — green.", lv.Name, envLabel(env), short(sha)),
			fmt.Sprintf("Captured %d artefact(s) under `%s`; the embedded report has been refreshed.", len(refs), runKey),
		}
	}
	return out, nil
}

// capture stores the rung's cucumber_json (and any declared extra artefacts) as a
// fresh, newly-stamped side-by-side run under the attempt's artefact store (drv-017),
// then prunes to the configured retention — mirroring `draiver test`'s capture.
func capture(root store.Root, wd, ticket, att string, lv pyramid.Level, sha string) (string, []string, error) {
	var sources []bddartefact.Source
	if c := strings.TrimSpace(lv.CucumberJSON); c != "" {
		sources = append(sources, bddartefact.Source{Path: c})
	}
	for _, a := range lv.Artifacts {
		sources = append(sources, bddartefact.Source{Path: a})
	}
	rc := bddartefact.RunContext{
		Rung:        lv.Name,
		Environment: lv.Environment,
		Commit:      sha,
		Runstamp:    time.Now().UTC().Format("20060102T150405Z"),
	}
	artDir := root.ArtefactsDir(ticket, att)
	runKey, refs, err := bddartefact.Capture(artDir, wd, rc, sources)
	if err != nil {
		return "", nil, err
	}
	// Retention: keep the newest N per group, mirroring cmd/test.go. Best-effort —
	// a prune fault must not fail a successful capture.
	if cfg, err := config.Load(""); err == nil {
		bddartefact.Prune(artDir, rc.Rung, rc.Env(), cfg.BDDArtefactKeep)
	}
	return runKey, refs, nil
}

// runShell runs a `sh -c` command string in wd and returns its run error (nil on
// exit 0) — the same exec seam `draiver test` uses for up/down/healthcheck/run.
func runShell(ctx context.Context, wd, script string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = wd
	return cmd.Run()
}

func envLabel(env *pyramid.Environment) string {
	if env == nil {
		return "the ambient environment"
	}
	return "environment `" + env.Name + "`"
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
