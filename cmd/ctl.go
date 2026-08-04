package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/agent/claudecode"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

var (
	ctlRepo          string
	ctlInterval      time.Duration
	ctlPermMode      string
	ctlModel         string
	ctlContextWindow int
	ctlLogsFollow    bool
)

// ctlCmd is the draiverctld client surface — the reconciling supervisor half of
// draiver (the "systemctl" to the daemon's "PID 1"). `up`/`down` drive the
// reconcile loop over the whole fleet; the client verbs — start / stop / restart
// / status / logs — act directly on one attempt's session, the hand-driven handle
// for early dev.
var ctlCmd = &cobra.Command{
	Use:   "ctl",
	Short: "draiverctld: the reconciling supervisor for AI coding sessions",
	Long: "ctl is draiver's active half — the supervisor daemon that brings up a " +
		"session for every enabled ticket in an isolated worktree, watches and gates " +
		"it, and survives its own restart by re-adopting what is still running.\n\n" +
		"`ctl up` runs the reconcile loop over the fleet. The client verbs — start, " +
		"stop, restart, status, logs — drive a single attempt's session by hand, the " +
		"systemctl to the daemon's PID 1.",
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

var ctlStartCmd = &cobra.Command{
	Use:   "start <ticket[@attempt]>",
	Short: "Bring up (or resume) a session for one attempt and stream it in the foreground",
	Long: "start brings up a session for one attempt — spawning a fresh one, or " +
		"resuming the recorded cattle handle if the attempt already ran — injects the " +
		"cold-start brief, and streams it in the foreground with the full gate/protocol " +
		"pipeline live. Ctrl-C reaps the session but keeps its id, so a later start (or " +
		"restart) resumes it.\n\n" +
		"Tier 0 has no background daemon channel, so start runs in the foreground; use " +
		"`ctl up` to supervise the fleet in the background.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, ticket, att, err := newCtlTarget(args[0])
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "draiverctl start %s/%s — Ctrl-C to stop (session stays resumable)\n", ticket, att)
		return r.Start(ctx, ticket, att, streamPrinter(out))
	},
}

var ctlStopCmd = &cobra.Command{
	Use:   "stop <ticket[@attempt]>",
	Short: "Reap an attempt's session, keeping its id + log for a later resume",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, ticket, att, err := newCtlTarget(args[0])
		if err != nil {
			return err
		}
		res, err := r.Stop(ticket, att)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if res.Signaled {
			fmt.Fprintf(out, "stopped %s/%s (pid %d) — session id kept for resume\n", ticket, att, res.PID)
		} else {
			fmt.Fprintf(out, "%s/%s already stopped (no live process)\n", ticket, att)
		}
		return nil
	},
}

var ctlRestartCmd = &cobra.Command{
	Use:   "restart <ticket[@attempt]>",
	Short: "Reap and respawn a session fresh from a new brief (context refresh)",
	Long: "restart reaps the current session and brings it back up fresh from a new " +
		"cold-start brief — the context refresh. The session id survives, so this is a " +
		"resume, not a new attempt. Like start it streams in the foreground.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, ticket, att, err := newCtlTarget(args[0])
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "draiverctl restart %s/%s — reaping and respawning fresh from brief (Ctrl-C to stop)\n", ticket, att)
		return r.Restart(ctx, ticket, att, streamPrinter(out))
	},
}

var ctlStatusCmd = &cobra.Command{
	Use:   "status [ticket[@attempt]]",
	Short: "Live view per attempt: control state, session pid, model, tokens/$ and context-window %",
	Args:  cobra.RangeArgs(0, 1),
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

		var wantTicket, wantAtt string
		if len(args) == 1 {
			root, err := resolveRoot()
			if err != nil {
				return err
			}
			if wantTicket, wantAtt, err = resolveCtlTarget(root, args[0]); err != nil {
				return err
			}
		}

		out := cmd.OutOrStdout()
		for _, s := range snap {
			if wantTicket != "" && (s.Key.Ticket != wantTicket || s.Key.Attempt != wantAtt) {
				continue
			}
			sessionCol := "-"
			switch {
			case s.Adopted:
				sessionCol = fmt.Sprintf("adopted pid=%d", s.Identity.PID)
			case s.Running:
				sessionCol = fmt.Sprintf("running pid=%d", s.Identity.PID)
			}
			model := s.Identity.Model
			if model == "" {
				model = "-"
			}
			fmt.Fprintf(out, "%-14s %-9s desired=%-5t %-18s model=%-10s %s  $%.4f\n",
				s.Key.Ticket+"/"+s.Key.Attempt, s.State, s.Desired, sessionCol, model,
				contextGauge(s.Meter.Usage.ContextTokens, ctlContextWindow), s.Meter.Usage.CostUSD)
		}
		return nil
	},
}

var ctlLogsCmd = &cobra.Command{
	Use:   "logs <ticket[@attempt]>",
	Short: "Tail an attempt's raw session stream (stream.jsonl); -f follows",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		ticket, att, err := resolveCtlTarget(root, args[0])
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return tailStream(ctx, cmd.OutOrStdout(), root.SessionStreamPath(ticket, att), ctlLogsFollow)
	},
}

// newCtlTarget resolves an attempt target and a Reconciler in one step — the
// shared preamble of start/stop/restart. It returns the reconciler bound to the
// repo plus the resolved ticket and attempt id.
func newCtlTarget(arg string) (*reconcile.Reconciler, string, string, error) {
	root, err := resolveRoot()
	if err != nil {
		return nil, "", "", err
	}
	ticket, att, err := resolveCtlTarget(root, arg)
	if err != nil {
		return nil, "", "", err
	}
	r, err := newReconciler()
	if err != nil {
		return nil, "", "", err
	}
	return r, ticket, att, nil
}

// resolveCtlTarget parses a `ticket[@attempt]` argument into a ticket and a
// resolved attempt id. An explicit @attempt suffix wins; otherwise the attempt
// falls back to --attempt / $DRAIVER_ATTEMPT / the ticket's latest. Both the
// ticket and the attempt must exist.
func resolveCtlTarget(root store.Root, arg string) (string, string, error) {
	ticket, att := arg, ""
	if i := strings.IndexByte(arg, '@'); i >= 0 {
		ticket, att = arg[:i], arg[i+1:]
	}
	if !root.Exists(ticket) {
		return "", "", fmt.Errorf("ticket %q not found under %s", ticket, root.Dir)
	}
	if att == "" {
		var err error
		if att, err = resolveAttempt(root, ticket); err != nil {
			return "", "", err
		}
	}
	if !root.AttemptExists(ticket, att) {
		return "", "", fmt.Errorf("attempt %s/%s not found", ticket, att)
	}
	return ticket, att, nil
}

// streamPrinter renders a live session's normalized events as concise one-liners
// for a foreground start/restart. It is presentation only; the durable log and
// meter are written by the dispatch pipeline underneath.
func streamPrinter(out io.Writer) func(agent.Event) {
	return func(ev agent.Event) {
		switch ev.Kind {
		case agent.EventSystem:
			fmt.Fprintf(out, "  -- session %s online\n", ev.SessionID)
		case agent.EventAssistant:
			if ev.Thinking {
				return
			}
			if s := strings.TrimSpace(ev.Text); s != "" {
				fmt.Fprintf(out, "  %s\n", s)
			}
		case agent.EventToolCall:
			if ev.Tool != nil {
				fmt.Fprintf(out, "  > %s\n", ev.Tool.Name)
			}
		case agent.EventToolResult:
			if ev.Tool != nil && ev.Tool.IsError {
				fmt.Fprintf(out, "  ! %s failed\n", ev.Tool.Name)
			}
		case agent.EventPermission:
			if ev.Permission != nil {
				fmt.Fprintf(out, "  ? permission: %s\n", ev.Permission.Tool)
			}
		case agent.EventUsage:
			if ev.Usage != nil {
				fmt.Fprintf(out, "  -- %s, $%.4f\n", contextGauge(ev.Usage.ContextTokens, ctlContextWindow), ev.Usage.CostUSD)
			}
		case agent.EventTurnEnd:
			fmt.Fprintf(out, "  -- turn end (%s)\n", ev.Turn)
		case agent.EventError:
			fmt.Fprintf(out, "  ! %s\n", ev.Err)
		}
	}
}

// contextGauge renders the live context-window fill: an absolute token count and,
// when a capacity is configured (--context-window), the percentage the doc calls
// for. The stream does not report the window size, so the capacity is configured
// rather than measured.
func contextGauge(ctxTokens, capacity int) string {
	if ctxTokens <= 0 {
		return "ctx=-"
	}
	if capacity <= 0 {
		return "ctx=" + commas(ctxTokens) + " tok"
	}
	return fmt.Sprintf("ctx=%d%% (%s/%s)", ctxTokens*100/capacity, commas(ctxTokens), commas(capacity))
}

// commas groups an integer into thousands for a readable gauge (12345 -> 12,345).
func commas(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// tailStream prints a session's raw stream-json tee (stream.jsonl). Without
// follow it prints what is on disk and returns; with follow it keeps printing
// appended lines until ctx is cancelled (Ctrl-C), waiting for the file to appear
// if the session has not been started yet.
func tailStream(ctx context.Context, out io.Writer, path string, follow bool) error {
	f, err := openStream(ctx, path, follow)
	if err != nil {
		return err
	}
	if f == nil {
		// Absent stream and not following (or the wait was cancelled): a session
		// that simply has not produced a stream yet, not an error.
		if !follow {
			fmt.Fprintln(out, "(no session stream yet)")
		}
		return nil
	}
	defer f.Close()

	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			_, _ = io.WriteString(out, line)
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return err
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// openStream opens the stream file, waiting for it to appear when following. It
// returns (nil, nil) when the file is absent and we are not following — a session
// that simply has not produced a stream yet, which is not an error.
func openStream(ctx context.Context, path string, follow bool) (*os.File, error) {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if !follow {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(300 * time.Millisecond):
		}
	}
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
	// The foreground drivers route tool use through the gates just like the daemon.
	ctlStartCmd.Flags().StringVar(&ctlPermMode, "permission-mode", "", "agent permission mode (routes tool use through the gates)")
	ctlRestartCmd.Flags().StringVar(&ctlPermMode, "permission-mode", "", "agent permission mode (routes tool use through the gates)")
	ctlStatusCmd.Flags().IntVar(&ctlContextWindow, "context-window", 200_000, "context-window capacity (tokens) the context-% gauge is measured against")
	ctlLogsCmd.Flags().BoolVarP(&ctlLogsFollow, "follow", "f", false, "keep printing new stream lines as they are appended")
	ctlCmd.AddCommand(ctlUpCmd, ctlStartCmd, ctlStopCmd, ctlRestartCmd, ctlStatusCmd, ctlLogsCmd)
	rootCmd.AddCommand(ctlCmd)
}
