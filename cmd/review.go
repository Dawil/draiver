package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

var (
	reviewURLs  []string
	reviewLinks []string
)

var reviewCmd = &cobra.Command{
	Use:   "review TICKET [CLAIM]",
	Short: "Claim a ticket is done (a claim, not a fact, for a human to verify)",
	Long: "review claims the ticket is complete for a human to verify. Attach the\n" +
		"review link — the draft PR, merge request, or diff URL where the change can\n" +
		"be seen — with --url (rel defaults to pr) or --link rel=uri for other rels.\n" +
		"\n" +
		"Push your branch to a git remote first so the link resolves: review only\n" +
		"logs and validates the link, it does not push or open a PR. With one remote\n" +
		"use it; with several, push to the config's primary_remote. Prefer a compare\n" +
		"URL (--link compare=<base>/compare/<main>...<branch>) when there is no PR.\n" +
		"\n" +
		"If the attempt's repo declares a .test-pyramid.yaml, review is gated: the\n" +
		"target (top) rung must be logged green at the current HEAD before a claim is\n" +
		"admitted. Climb it with `draiver test --log`. A repo with no pyramid is not\n" +
		"gated.",
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		body := "Agent claims the ticket is complete; ready for review."
		if len(args) == 2 {
			body = args[1]
		}
		links, err := buildLinks(reviewURLs, reviewLinks)
		if err != nil {
			return err
		}

		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}
		att, err := resolveAttempt(root, id)
		if err != nil {
			return err
		}

		// The review gate: enforce-by-process-control seam closing the test-pyramid
		// epic (drv-009). A claim may not be raised while the target rung was not
		// climbed at HEAD. It runs before the append so a refused claim leaves no
		// event behind.
		if err := reviewGate(cmd.Context(), root, id, att); err != nil {
			return err
		}

		e, _, err := appendEventAt(root, id, att, event.Event{Type: "review", Body: body, Links: links})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "review claimed on %s/%s (seq %d)\n", id, att, e.Seq)
		return nil
	},
}

// reviewGate refuses a claim whose attempt has not climbed its pyramid's target
// rung at the current HEAD. It is the read-only twin of merge's clean-checkout
// gate: it resolves the attempt's checkout attempt-addressed (via the worktree
// Manager, from the recorded repo) and folds the attempt's log — never cutting a
// worktree, never touching cwd, because review is TICKET-addressed and may be run
// from anywhere.
//
// It is deliberately permissive about *whether there is anything to gate* — an
// attempt with no recorded repo, no live checkout, or no .test-pyramid.yaml is
// simply not gated (a pass, not a failure). It refuses only when a pyramid exists
// and the log proves the target rung is not green at HEAD.
func reviewGate(ctx context.Context, root store.Root, ticket, att string) error {
	a, err := project.LoadAttempt(root, ticket, att)
	if err != nil {
		return err
	}
	if a.Repo == "" {
		// No repo recorded — there is no checkout to resolve, so nothing to gate.
		return nil
	}
	wm, err := worktree.NewManager(a.Repo)
	if err != nil {
		// The recorded repo does not resolve to a git base (e.g. a docs-only or
		// externally-managed ticket whose "repo" is a bare path). There is no managed
		// checkout to fold — treat it as the "no live checkout" skip, not a hard
		// error: the gate only ever *adds* a refusal on positive proof, it must never
		// break a review it has nothing to enforce against.
		return nil
	}
	wt, ok, err := wm.Locate(ctx, worktree.Key{Ticket: ticket, Attempt: att})
	if err != nil {
		return err
	}
	if !ok {
		// No live checkout for the attempt (never created, or retired) — nothing to
		// fold HEAD/pyramid against.
		return nil
	}
	p, err := pyramid.Load(wt.Path)
	if err != nil {
		return err
	}
	if p == nil {
		// No .test-pyramid.yaml — no gate (not a failure).
		return nil
	}

	head, err := worktree.HeadSHA(ctx, wt.Path)
	if err != nil {
		return err
	}

	// Map the attempt's test-result events onto the pyramid's neutral Result pair;
	// only green results are logged (drvctl-048), so the fold is "highest logged
	// rung at HEAD" with no failing state to reconcile.
	var results []pyramid.Result
	for _, e := range a.Events {
		if e.Type == "test-result" {
			results = append(results, pyramid.Result{Rung: e.Rung, Commit: e.Commit})
		}
	}

	target := p.Target()
	highest, ok := p.HighestAtHEAD(results, head)
	if ok && highest.Name == target.Name {
		// The target rung is green at this exact HEAD — the claim is admitted.
		return nil
	}

	have := "no rung is green at HEAD"
	if ok {
		have = fmt.Sprintf("the highest green rung at HEAD is %q", highest.Name)
	}
	return fmt.Errorf(
		"review refused: target rung %q of %s is not green at HEAD %s (%s) — climb it with `draiver test --log`, then claim review; a result logged before a later commit is stale and does not count",
		target.Name, pyramid.FileName, shortSHA(head), have)
}

func init() {
	addLinkFlags(reviewCmd, &reviewURLs, &reviewLinks)
	rootCmd.AddCommand(reviewCmd)
}
