// Package cmd wires the draiver CLI verbs — the shared human<->agent protocol —
// onto the append-only per-attempt ticket log.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/store"
)

const (
	// ExitEscalated signals that an escalation was raised: the agent must halt.
	// Distinct from a plain failure so a supervising loop can branch on
	// "blocked" versus "errored".
	ExitEscalated = 3
	// ExitAuditFailed signals a broken hash chain.
	ExitAuditFailed = 4
	// ExitCanaryFired signals the silent-invalidator canary found a busted shared
	// prompt-cache prefix on at least one repo (drvctl-036). Distinct from a plain
	// failure so a CI/cron loop can branch on "cache regressed" versus "errored".
	ExitCanaryFired = 5
)

var (
	dataFlag    string
	actorFlag   string
	attemptFlag string
)

// version is the resolved build version, single-sourced from the embedded
// CHANGELOG.md by main at startup via SetVersion (see version.go at the repo
// root). It stays "dev" until set — e.g. in cmd unit tests, which exercise the
// commands without the top-level embed — so the daemon banners always have a
// non-empty version to print.
var version = "dev"

// SetVersion records the resolved version as both cobra's --version string and
// the value the long-running daemon banners (ctl up, webui) prefix their startup
// line with, keeping every surface on the one changelog-derived source. An empty
// value is ignored so the "dev" fallback survives.
func SetVersion(v string) {
	if v == "" {
		return
	}
	version = v
	rootCmd.Version = v
}

// exitError carries a specific process exit code out of a command.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

var rootCmd = &cobra.Command{
	Use:   "draiver",
	Short: "Coordination substrate for supervising AI coding agents at the ticket level",
	Long: "draiver externalizes a ticket's valuable context — spec, decisions, " +
		"escalations — into an append-only, hash-chained log on disk. A ticket holds " +
		"one or more attempts; each attempt is a journey a fresh agent can pick up.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dataFlag, "data", "", "data root (default $DRAIVER_DATA or ~/.draiver/data)")
	rootCmd.PersistentFlags().StringVar(&actorFlag, "actor", "", "actor as kind:name (default $DRAIVER_ACTOR or human:$USER)")
	rootCmd.PersistentFlags().StringVar(&attemptFlag, "attempt", "", "attempt id (default $DRAIVER_ATTEMPT or the ticket's latest attempt)")
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	err := rootCmd.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, "draiver:", ee.msg)
		}
		return ee.code
	}
	fmt.Fprintln(os.Stderr, "draiver:", err)
	return 1
}

// resolveRoot resolves the data root from the --data flag / env / default.
func resolveRoot() (store.Root, error) {
	return store.Resolve(dataFlag)
}

// resolveActor resolves the acting identity: --actor, then $DRAIVER_ACTOR, then
// human:$USER.
func resolveActor() string {
	if actorFlag != "" {
		return actorFlag
	}
	if v := os.Getenv("DRAIVER_ACTOR"); v != "" {
		return v
	}
	u := os.Getenv("USER")
	if u == "" {
		u = "unknown"
	}
	return "human:" + u
}

// resolveAttempt resolves which attempt of a ticket a command targets:
// --attempt, then $DRAIVER_ATTEMPT, then the ticket's latest attempt.
func resolveAttempt(root store.Root, id string) (string, error) {
	if attemptFlag != "" {
		return attemptFlag, nil
	}
	if v := os.Getenv("DRAIVER_ATTEMPT"); v != "" {
		return v, nil
	}
	latest, ok, err := attempt.Latest(root, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("ticket %q has no attempts", id)
	}
	return latest, nil
}

// appendEvent resolves root + target attempt (--attempt / $DRAIVER_ATTEMPT /
// latest), stamps the actor, and appends the event, returning the persisted event
// and the attempt it landed on.
func appendEvent(id string, e event.Event) (event.Event, string, error) {
	root, err := resolveRoot()
	if err != nil {
		return event.Event{}, "", err
	}
	if !root.Exists(id) {
		return event.Event{}, "", fmt.Errorf("ticket %q not found under %s", id, root.Dir)
	}
	att, err := resolveAttempt(root, id)
	if err != nil {
		return event.Event{}, "", err
	}
	return appendEventAt(root, id, att, e)
}

// appendEventAt appends e to an already-resolved (ticket, attempt) — the shared
// core of appendEvent, split out for verbs (archive/unarchive) that resolve a
// `ticket[@attempt]` target and load its derived state before writing, yet still
// want the same attempt-existence check, review-link validation, and actor
// stamping every append goes through. Those write semantics now live once in
// internal/repo (drv-008); this is the CLI's thin adapter over that gateway,
// stamping the actor resolved from the CLI's --actor/env/$USER context.
func appendEventAt(root store.Root, id, att string, e event.Event) (event.Event, string, error) {
	ev, err := repo.New(root, resolveActor()).AppendAt(id, att, e)
	return ev, att, err
}
