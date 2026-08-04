package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/agent/claudecode"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/worktree"
)

var (
	ctlRepo     string
	ctlInterval time.Duration
	ctlPermMode string
	ctlModel    string
)

// ctlCmd is the draiverctld client surface — the reconciling supervisor half of
// draiver (the "systemctl" to the daemon's "PID 1"). Tier 0 ships `up` (run the
// reconcile loop) and `status` (the live board).
var ctlCmd = &cobra.Command{
	Use:   "ctl",
	Short: "draiverctld: the reconciling supervisor for AI coding sessions",
	Long: "ctl is draiver's active half — the supervisor daemon that brings up a " +
		"session for every enabled ticket in an isolated worktree, watches and gates " +
		"it, and survives its own restart by re-adopting what is still running.",
}

var ctlUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Start draiverctld and reconcile the enabled fleet until interrupted",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := newReconciler()
		if err != nil {
			return err
		}
		// A ctld restart must not disturb running sessions; Ctrl-C / SIGTERM drains
		// (detaches) rather than reaping, so a later `up` re-adopts them.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		fmt.Fprintf(cmd.OutOrStdout(), "draiverctld up — repo %s, tick every %s (Ctrl-C to drain)\n", ctlRepo, ctlInterval)
		return r.Run(ctx, ctlInterval)
	},
}

var ctlStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the desired/actual view: control state and live sessions per attempt",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := newReconciler()
		if err != nil {
			return err
		}
		// Adopt (without running the loop) so the snapshot reflects processes that
		// are actually alive on this machine, not just what disk records.
		if err := r.Adopt(cmd.Context()); err != nil {
			return err
		}
		snap, err := r.Snapshot()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, s := range snap {
			sessionCol := "-"
			switch {
			case s.Adopted:
				sessionCol = fmt.Sprintf("adopted pid=%d", s.Identity.PID)
			case s.Running:
				sessionCol = fmt.Sprintf("running pid=%d ctx=%d", s.Identity.PID, s.Meter.Usage.ContextTokens)
			}
			fmt.Fprintf(out, "%-14s %-10s desired=%-5t session=%s\n",
				s.Key.Ticket+"/"+s.Key.Attempt, s.State, s.Desired, sessionCol)
		}
		return nil
	},
}

// newReconciler builds a Reconciler from the resolved data root, the target repo,
// and the Claude Code adapter — the one adapter Tier 0 ships.
func newReconciler() (*reconcile.Reconciler, error) {
	root, err := resolveRoot()
	if err != nil {
		return nil, err
	}
	if ctlRepo == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		ctlRepo = cwd
	}
	wm, err := worktree.NewManager(ctlRepo)
	if err != nil {
		return nil, err
	}
	return reconcile.New(reconcile.Options{
		Root:      root,
		Worktrees: wm,
		Adapters:  claudeAdapters,
		Actor:     resolveActor(),
		BaseSpec: agent.SessionSpec{
			Model:          ctlModel,
			PermissionMode: ctlPermMode,
		},
	})
}

// claudeAdapters resolves adapter names to factories. Tier 0 knows only Claude
// Code (the reference adapter); Aider/Codex plug in here behind the same seam.
func claudeAdapters(name string) (func() agent.Adapter, error) {
	switch name {
	case "claude-code", "":
		return func() agent.Adapter { return claudecode.New() }, nil
	default:
		return nil, fmt.Errorf("ctl: unknown adapter %q (Tier 0 ships claude-code only)", name)
	}
}

func init() {
	ctlCmd.PersistentFlags().StringVar(&ctlRepo, "repo", "", "path to the repo agents work in (default: cwd)")
	ctlCmd.PersistentFlags().StringVar(&ctlModel, "model", "", "default model for spawned sessions (overridden per attempt)")
	ctlUpCmd.Flags().DurationVar(&ctlInterval, "interval", 5*time.Second, "reconcile tick interval")
	ctlUpCmd.Flags().StringVar(&ctlPermMode, "permission-mode", "", "agent permission mode (routes tool use through the gates)")
	ctlCmd.AddCommand(ctlUpCmd, ctlStatusCmd)
	rootCmd.AddCommand(ctlCmd)
}
