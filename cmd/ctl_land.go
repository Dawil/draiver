package cmd

// ctl_land.go adds the terminal bookend of the branch lifecycle `ctl` already
// owns (create → work → **land** → reclaim): `ctl merge` lands an attempt's branch
// into its base (fast-forward only) and records `done` on success, so Done comes
// to mean *the code is in the target branch*, not *someone clicked Done*; `ctl
// sync` back-merges the base into the branch additively. Both derive the base from
// the attempt's recorded `base:` field, never a hardcoded trunk (drvctl-021).
//
// Two orthogonal axes govern them:
//   - direction/strategy: merge vs sync; and on a diverged merge, refuse (default)
//     vs --sync (sync-then-ff in one shot).
//   - failure disposition: --escalate raises a durable escalation (→ Needs me →
//     halt), --no-escalate exits nonzero with a message and no durable event.
//     Default is by actor kind: an agent's failed land must reach the board, a
//     human at the terminal is already the escalation target.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/land"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

var (
	mergeDryRun    bool
	mergeSync      bool   // on divergence, sync-then-ff in one shot instead of refusing
	mergeRemote    string // --remote[=NAME]: reconcile from a remote after an external PR merge
	landEscalate   bool   // force the escalate disposition on failure
	landNoEscalate bool   // force the error-only disposition on failure
)

// remoteBareSentinel is the NoOptDefVal for --remote: a bare `--remote` (no value)
// yields it, so the resolver knows to pick the default remote rather than treating
// it as an explicit NAME. It contains a space, which git forbids in a remote name,
// so it can never collide with a real --remote=NAME.
const remoteBareSentinel = "<default remote>"

var ctlMergeCmd = &cobra.Command{
	Use:   "merge <ticket[@attempt]>",
	Short: "Land an attempt's branch into its base (fast-forward only) and record done",
	Long: "merge lands the attempt's per-attempt branch into its recorded base branch, " +
		"fast-forward only — the terminal of the branch lifecycle ctl owns. It is gated " +
		"to a stopped Review attempt with a clean checkout (the drvctl-014 ethos: the " +
		"branch holds all the work), keeps the branch until the land is durable, and on a " +
		"clean land records `done` so control state follows reality (Review → Done because " +
		"the code landed).\n\n" +
		"A fast-forward is possible only when the base is an ancestor of the branch. When " +
		"the branch has diverged, merge refuses and points to `ctl sync` (which back-merges " +
		"the base in, making the branch ff-landable); --sync runs that sync then the ff in " +
		"one shot. It never rebases and never forces.\n\n" +
		"--dry-run reports mergeability (ff-landable? would a merge conflict?) without " +
		"mutating anything, via --is-ancestor and merge-tree.\n\n" +
		"On failure --escalate raises a durable escalation (→ Needs me, halt exit 3) and " +
		"--no-escalate exits nonzero with just a message; the default is by actor kind " +
		"(agent → escalate, human → error).\n\n" +
		"--remote[=NAME] is the external twin: when the change landed via a PR merged on " +
		"the forge, it fetches the recorded base from the remote (read-only, never pushes), " +
		"verifies the branch is contained upstream, records `done`, and best-effort " +
		"fast-forwards the local base. Bare --remote uses the sole remote (or the config's " +
		"primary_remote when there are several); --remote=NAME overrides. A failed containment " +
		"check takes the same escalate/no-escalate disposition; a failed local pull is only a " +
		"warning and never un-closes the ticket.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		lc, err := loadLandContext(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		// Remote mode is keyed off the flag value, not cmd.Flags().Changed: the test
		// harness reuses the singleton root command and cobra never resets Changed
		// between runs. Absence leaves mergeRemote "" (plain local merge); a bare
		// --remote yields the sentinel; --remote=NAME yields NAME.
		if mergeRemote != "" {
			if mergeDryRun {
				return fmt.Errorf("--dry-run reports local mergeability and --remote reconciles from a remote; use one or the other")
			}
			return lc.mergeRemote(cmd)
		}
		if mergeDryRun {
			return lc.dryRun(cmd)
		}
		return lc.merge(cmd)
	},
}

var ctlSyncCmd = &cobra.Command{
	Use:   "sync <ticket[@attempt]>",
	Short: "Back-merge the base into an attempt's branch (additive; no rebase)",
	Long: "sync back-merges the attempt's recorded base branch into its per-attempt branch " +
		"with a plain, additive `git merge` — existing commits keep their SHAs and at most " +
		"one merge commit lands on top, so a resume re-attaches to the same durable branch " +
		"and just continues on the updated tip. After a sync the branch becomes ff-landable, " +
		"so it is both the fix for a diverged `ctl merge` and independently useful mid-attempt " +
		"(pulling the base's changes in). It never rebases and never forces.\n\n" +
		"It operates on the attempt's checkout (materializing one if the daemon already " +
		"reclaimed it) and requires that checkout clean. On conflict it aborts the merge, " +
		"restoring the pre-sync state, then escalates or errors per the failure-disposition " +
		"flags (default by actor kind).",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		lc, err := loadLandContext(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return lc.sync(cmd)
	},
}

// landContext is the resolved handle merge/sync share: the target attempt, its
// derived state, the base branch, and the worktree Manager for its repo.
type landContext struct {
	root    store.Root
	ticket  string
	attempt string
	att     project.Attempt
	base    string
	wm      *worktree.Manager
	key     worktree.Key
}

// loadLandContext resolves the target attempt, loads its provenance and derived
// state, and builds the worktree Manager for its repo — the shared preamble of
// both verbs. It fails early if the attempt records no base (nothing to land into)
// or no repo (no worktree to land from).
func loadLandContext(ctx context.Context, arg string) (*landContext, error) {
	root, err := resolveRoot()
	if err != nil {
		return nil, err
	}
	ticket, att, err := resolveCtlTarget(root, arg)
	if err != nil {
		return nil, err
	}
	// land.Load is the single implementation of the base/repo/worktree resolution,
	// shared with the merge --remote path (and the webui).
	h, err := land.Load(root, ticket, att)
	if err != nil {
		return nil, err
	}
	return &landContext{
		root: h.Root, ticket: h.Ticket, attempt: h.Attempt, att: h.Att, base: h.Base, wm: h.WM,
		key: h.Key,
	}, nil
}

// dryRun reports mergeability without mutating anything. A failure here is a plain
// error, never an escalation: a durable escalation event would itself be a
// mutation, breaking the "reports without mutating anything" contract.
func (lc *landContext) dryRun(cmd *cobra.Command) error {
	mg, err := lc.wm.Mergeable(cmd.Context(), lc.key, lc.base)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	switch {
	case mg.FastForward:
		fmt.Fprintf(out, "%s/%s: ff-landable now — `ctl merge` would fast-forward %s into %s\n", lc.ticket, lc.attempt, mg.Branch, lc.base)
	case mg.Clean():
		fmt.Fprintf(out, "%s/%s: diverged but clean — `ctl sync` would merge %s in without conflict, then it becomes ff-landable\n", lc.ticket, lc.attempt, lc.base)
	default:
		fmt.Fprintf(out, "%s/%s: diverged with conflicts in %d file(s): %s — resolve by syncing and fixing the conflicts\n",
			lc.ticket, lc.attempt, len(mg.Conflicts), strings.Join(mg.Conflicts, ", "))
	}
	return nil
}

// merge lands the branch ff-only, gated to a stopped Review attempt with a clean
// checkout, and records `done` on success. On divergence it refuses (or, with
// --sync, back-merges the base first and retries the ff).
func (lc *landContext) merge(cmd *cobra.Command) error {
	if err := lc.gateForLand(cmd.Context()); err != nil {
		return lc.fail(cmd, "merge", err)
	}

	landed, err := lc.wm.Merge(cmd.Context(), lc.key, lc.base)
	if errors.Is(err, worktree.ErrDiverged) && mergeSync {
		// sync-then-ff: back-merge the base in to flip the ancestry, then land.
		if _, serr := lc.wm.Sync(cmd.Context(), lc.key, lc.base); serr != nil {
			return lc.fail(cmd, "merge --sync (sync step)", serr)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: synced %s in to make the branch ff-landable; landing…\n", lc.ticket, lc.attempt, lc.base)
		landed, err = lc.wm.Merge(cmd.Context(), lc.key, lc.base)
	}
	if err != nil {
		return lc.fail(cmd, "merge", err)
	}

	// The land is durable now (the base ref advanced). Record `done` so control
	// state follows reality — Review → Done because the code is in the base.
	body := fmt.Sprintf("Landed %s into %s (fast-forward, tip %s) via `ctl merge`.", landed.Branch, landed.Base, shortSHA(landed.Tip))
	if landed.AlreadyUpToDate {
		body = fmt.Sprintf("`ctl merge`: %s was already contained in %s (nothing to land); recording done.", landed.Branch, landed.Base)
	}
	e, err := repo.New(lc.root, resolveActor()).Done(lc.ticket, lc.attempt, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "landed %s/%s: %s → %s and recorded done (seq %d)\n", lc.ticket, lc.attempt, landed.Branch, landed.Base, e.Seq)
	return nil
}

// mergeRemote is the external twin of merge: it reconciles from the remote rather
// than landing the local branch, via the shared internal/land orchestration (the
// same operation the webui's "Merged elsewhere" button calls in-process). It
// fetches the recorded base, verifies the branch is contained upstream (the
// load-bearing gate for `done`), records `done`, then best-effort fast-forwards the
// local base. A remote/config resolution problem is a plain error (land tags it
// *PlainError); the containment/fetch/gate failure takes the escalate/no-escalate
// disposition; the local pull's failure is only a warning.
func (lc *landContext) mergeRemote(cmd *cobra.Command) error {
	// Translate the --remote flag to land's selector: the bare sentinel means "the
	// default remote" (""), an explicit --remote=NAME passes NAME.
	remote := ""
	if mergeRemote != remoteBareSentinel {
		remote = strings.TrimSpace(mergeRemote)
	}
	res, err := land.MergeRemote(cmd.Context(), repo.New(lc.root, resolveActor()), lc.ticket, lc.attempt, remote, ctlConfigPath)
	if err != nil {
		// A PlainError is a resolution/bookkeeping problem — surface it as-is (exit 1),
		// never through the escalate disposition; a land failure takes the disposition.
		var pe *land.PlainError
		if errors.As(err, &pe) {
			return err
		}
		return lc.fail(cmd, "merge --remote", err)
	}
	out := cmd.OutOrStdout()
	for _, line := range land.FormatRemoteReport(lc.ticket, lc.attempt, res) {
		fmt.Fprintln(out, line)
	}
	return nil
}

// sync back-merges the base into the branch additively.
func (lc *landContext) sync(cmd *cobra.Command) error {
	synced, err := lc.wm.Sync(cmd.Context(), lc.key, lc.base)
	if err != nil {
		return lc.fail(cmd, "sync", err)
	}
	if synced.AlreadyUpToDate {
		fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: already up to date with %s (nothing to sync)\n", lc.ticket, lc.attempt, lc.base)
		return nil
	}
	// A back-merge is a real, durable change to the branch; note it so a resumed
	// agent sees the tip moved and why.
	body := fmt.Sprintf("Synced %s into %s (merge commit, tip %s) via `ctl sync`; the branch is now ff-landable.", synced.Base, synced.Branch, shortSHA(synced.Tip))
	if _, err := repo.New(lc.root, resolveActor()).AppendTyped(lc.ticket, lc.attempt, event.Event{
		Type: "note", Body: body,
	}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "synced %s/%s: merged %s into %s\n", lc.ticket, lc.attempt, synced.Base, synced.Branch)
	return nil
}

// gateForLand enforces the merge preconditions: the attempt must be in Review (a
// land is the Review → Done transition), stopped (no live session racing the
// land), and its checkout clean (all work committed to the branch — drvctl-014).
func (lc *landContext) gateForLand(ctx context.Context) error {
	if lc.att.State != project.Review {
		return fmt.Errorf("merge lands a Review attempt, but %s/%s is %s — claim `review` first, or use the plain `done` verb for a docs-only / externally-landed ticket", lc.ticket, lc.attempt, lc.att.State)
	}
	live, pid, err := attemptSessionLive(lc.root, lc.ticket, lc.attempt)
	if err != nil {
		return err
	}
	if live {
		return fmt.Errorf("%s/%s has a live session (pid %d); stop it with `ctl stop` before landing", lc.ticket, lc.attempt, pid)
	}
	dirty, err := lc.wm.Dirty(ctx, lc.key)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("%s/%s has a dirty checkout — commit the work to the branch (or discard it) before landing; the land carries only what is committed", lc.ticket, lc.attempt)
	}
	return nil
}

// fail applies the failure-disposition axis: raise a durable escalation, or exit
// nonzero with just a message. The choice is --escalate / --no-escalate, defaulting
// by actor kind (agent → escalate so the block lands on the board; human → error).
func (lc *landContext) fail(cmd *cobra.Command, op string, cause error) error {
	msg := fmt.Sprintf("`ctl %s` failed on %s/%s: %v", op, lc.ticket, lc.attempt, cause)
	if !lc.escalateOnFailure() {
		return &exitError{code: 1, msg: msg}
	}
	// Append to the *resolved* attempt (lc.attempt), not via appendEvent's
	// latest/env fallback, so an explicit `@attempt` target escalates on itself.
	e, err := repo.New(lc.root, resolveActor()).AppendTyped(lc.ticket, lc.attempt, event.Event{
		Type: "escalation", Body: msg,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "escalated %s/%s #%d — halting (exit %d). Resolve with: draiver resolve %s %d \"...\" --attempt %s\n",
		lc.ticket, lc.attempt, e.Seq, ExitEscalated, lc.ticket, e.Seq, lc.attempt)
	return &exitError{code: ExitEscalated}
}

// escalateOnFailure resolves the failure disposition: an explicit flag wins
// (--escalate over --no-escalate if both are somehow set), otherwise it defaults
// by actor kind — an agent actor escalates, a human errors.
func (lc *landContext) escalateOnFailure() bool {
	switch {
	case landEscalate:
		return true
	case landNoEscalate:
		return false
	default:
		return strings.HasPrefix(resolveActor(), "agent:")
	}
}

// attemptSessionLive reports whether the attempt has a live recorded session
// process — the "stopped" half of the land gate. It delegates to land.SessionLive,
// the shared implementation the remote-land gate also uses.
func attemptSessionLive(root store.Root, ticket, att string) (bool, int, error) {
	return land.SessionLive(root, ticket, att)
}

// shortSHA trims a commit oid to a readable 12-char prefix for log bodies.
func shortSHA(sha string) string {
	return land.ShortSHA(sha)
}

func init() {
	ctlMergeCmd.Flags().BoolVar(&mergeDryRun, "dry-run", false, "report mergeability (ff-landable? would a merge conflict?) without mutating anything")
	ctlMergeCmd.Flags().BoolVar(&mergeSync, "sync", false, "on divergence, back-merge the base in (sync) then fast-forward, instead of refusing")
	ctlMergeCmd.Flags().StringVar(&mergeRemote, "remote", "", "reconcile from a git remote after an external PR merge instead of landing locally: fetch the base, verify the branch is contained upstream, record done, best-effort fast-forward the local base. Bare --remote uses the sole remote or the config's primary_remote; --remote=NAME overrides")
	ctlMergeCmd.Flags().Lookup("remote").NoOptDefVal = remoteBareSentinel
	for _, c := range []*cobra.Command{ctlMergeCmd, ctlSyncCmd} {
		c.Flags().BoolVar(&landEscalate, "escalate", false, "on failure, raise a durable escalation (→ Needs me, halt exit 3); default for an agent actor")
		c.Flags().BoolVar(&landNoEscalate, "no-escalate", false, "on failure, exit nonzero with just a message, no durable event; default for a human actor")
	}
	ctlCmd.AddCommand(ctlMergeCmd, ctlSyncCmd)
}
