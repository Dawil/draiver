package cmd

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/gate"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
)

var (
	ctlRepo          string
	ctlInterval      time.Duration
	ctlPermMode      string
	ctlModel         string
	ctlConfigPath    string
	ctlContextWindow int // -1 sentinel: resolve from config
	ctlContextLimit  int // -1 sentinel: resolve from config
	ctlLogsFollow    bool
	ctlLogsJSON      bool
	ctlPermRules     map[string]string // --permission tool=rule, layered over config
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

		repoNote := "repo per ticket"
		if ctlRepo != "" {
			repoNote = "fallback repo " + ctlRepo
		}
		fmt.Fprintf(cmd.OutOrStdout(), "draiverctld up — %s, tick every %s (Ctrl-C to drain)\n", repoNote, ctlInterval)
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
			fmt.Fprintf(out, "%-14s %-9s enabled=%-5t desired=%-5t %-18s model=%-10s %s  $%.4f\n",
				s.Key.Ticket+"/"+s.Key.Attempt, s.State, s.Enabled, s.Desired, sessionCol, model,
				contextGauge(s.Meter.Usage.ContextTokens, ctlContextWindow), s.Meter.Usage.CostUSD)
		}
		return nil
	},
}

var ctlLogsCmd = &cobra.Command{
	Use:   "logs <ticket[@attempt]>",
	Short: "Read an attempt's session stream — human-readable by default, raw stream-json with --json; -f follows",
	Long: "logs renders an attempt's recorded session stream for a human by default: " +
		"assistant prose, tool calls, tool errors, permission prompts, usage/cost and " +
		"turn boundaries — the same one-liners the live start/restart view prints — with " +
		"the stream-json envelope (event uuids, session id) dropped.\n\n" +
		"--json emits the raw stream.jsonl lines verbatim, byte-for-byte: the machine " +
		"form for `| jq` and replay. -f/--follow works in both modes.",
	Args: cobra.ExactArgs(1),
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
		return tailStream(ctx, cmd.OutOrStdout(), root.SessionStreamPath(ticket, att), ctlLogsFollow, !ctlLogsJSON)
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

// streamPrinter renders a live session's normalized events for a foreground
// start/restart. It is a thin adapter over renderEvent — the one shared renderer
// `ctl logs` also reads the recorded stream back through — so live and replayed
// output speak the same vocabulary. It is presentation only; the durable log and
// meter are written by the dispatch pipeline underneath.
func streamPrinter(out io.Writer) func(agent.Event) {
	return func(ev agent.Event) { renderEvent(out, ev) }
}

// renderEvent writes one normalized event as a concise, human-readable one-liner:
// assistant prose, `> tool` calls (with a short argument snippet), tool errors,
// permission prompts, usage/cost + context-window fill, and turn boundaries. It
// deliberately drops transport fields — event uuids, the session id, envelope
// wrappers — that a machine needs but a human reading the session does not. This
// is the single renderer shared by the live start/restart view (streamPrinter)
// and `ctl logs` reading the recorded stream.jsonl back off disk.
func renderEvent(out io.Writer, ev agent.Event) {
	switch ev.Kind {
	case agent.EventSystem:
		// The session id is a transport handle, not something a human reading the
		// stream needs; note only that the session came online.
		fmt.Fprintln(out, "  -- session online")
	case agent.EventAssistant:
		if ev.Thinking {
			return
		}
		if s := strings.TrimSpace(ev.Text); s != "" {
			fmt.Fprintf(out, "  %s\n", s)
		}
	case agent.EventToolCall:
		if ev.Tool != nil {
			if s := toolSummary(ev.Tool.Input); s != "" {
				fmt.Fprintf(out, "  > %s: %s\n", ev.Tool.Name, s)
			} else {
				fmt.Fprintf(out, "  > %s\n", ev.Tool.Name)
			}
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

// toolSummary pulls a short, human-meaningful snippet out of a tool call's raw
// input — the primary field most tools key on (a command, a path, a pattern) —
// so a rendered `> tool` line shows what the call is doing, not just its name. It
// stays a single short line; input it cannot read as one of those fields yields
// "" and the caller prints the bare tool name.
func toolSummary(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return truncateOneLine(v, 72)
		}
	}
	return ""
}

// truncateOneLine collapses a value to a single short line for a one-liner
// render: it keeps only the first line and caps the length (rune-safe), marking
// with an ellipsis whenever it dropped anything.
func truncateOneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	truncated := false
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
		truncated = true
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
		truncated = true
	}
	if truncated {
		s += "…"
	}
	return s
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

// tailStream prints a session's recorded stream (stream.jsonl). In render mode —
// the human-readable default — each raw stream-json line is normalized back into
// events and printed through renderEvent, the same vocabulary the live view uses,
// with transport noise dropped; in raw mode (--json) the lines are emitted
// verbatim, byte-for-byte, so `| jq` pipelines keep working. Without follow it
// prints what is on disk and returns; with follow it keeps emitting appended
// lines until ctx is cancelled (Ctrl-C), waiting for the file to appear if the
// session has not been started yet.
func tailStream(ctx context.Context, out io.Writer, path string, follow, render bool) error {
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
	var pending string // render mode only: bytes read past the last newline
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			switch {
			case !render:
				_, _ = io.WriteString(out, line)
			case strings.HasSuffix(line, "\n"):
				renderStreamLine(out, pending+line)
				pending = ""
			default:
				// A partial trailing line (EOF before a newline): hold it until its
				// newline arrives so we never try to parse half a JSON object.
				pending += line
			}
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return err
		}
		if !follow {
			if render && pending != "" {
				renderStreamLine(out, pending)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// renderStreamLine normalizes one recorded stream-json line and renders the
// events it yields; lines that carry only transport noise normalize to nothing
// and print nothing.
func renderStreamLine(out io.Writer, line string) {
	for _, ev := range claudecode.Normalize([]byte(line)) {
		renderEvent(out, ev)
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

// newReconciler builds a Reconciler from the resolved data root and the Claude
// Code adapter — the one adapter Tier 0 ships. Repo binding is per-ticket now
// (drvctl-015): the reconciler derives a worktree Manager per attempt's own repo
// path, so no single repo is resolved here. --repo, when given, is only a
// fallback for attempts that record none.
func newReconciler() (*reconcile.Reconciler, error) {
	root, err := resolveRoot()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(ctlConfigPath)
	if err != nil {
		return nil, err
	}
	// A flag value overrides the config file; the -1 sentinel means "the flag was
	// not given, use the config" (which itself carries the built-in default). The
	// resolved window is written back to the global the gauges read.
	if ctlContextWindow < 0 {
		ctlContextWindow = cfg.ContextWindow
	}
	contextLimit := cfg.ContextLimit
	if ctlContextLimit >= 0 {
		contextLimit = ctlContextLimit
	}
	permPolicy, err := resolvePermPolicy(cfg, ctlPermRules)
	if err != nil {
		return nil, err
	}
	return reconcile.New(reconcile.Options{
		Root:         root,
		DefaultRepo:  ctlRepo,
		Adapters:     claudeAdapters,
		Actor:        resolveActor(),
		ContextLimit: contextLimit,
		PermPolicy:   permPolicy,
		BaseSpec: agent.SessionSpec{
			Model:          ctlModel,
			PermissionMode: ctlPermMode,
		},
	})
}

// resolvePermPolicy layers the permission gate's policy lowest→highest: the
// allow-all base (auto mode) under the config file's overrides under the
// --permission flag's one-offs. An empty config and no flag leave AllowAll —
// Claude in auto mode, the local default — while either layer can escalate
// specific tools or reset the default. An invalid rule string in either source is
// surfaced as an error rather than silently defaulted.
func resolvePermPolicy(cfg config.Config, flagRules map[string]string) (gate.Policy, error) {
	cfgPolicy, err := gate.PolicyFromMap(cfg.PermissionsDefault, cfg.Permissions)
	if err != nil {
		return gate.Policy{}, fmt.Errorf("ctl: config permissions: %w", err)
	}
	flagPolicy, err := gate.PolicyFromMap("", flagRules)
	if err != nil {
		return gate.Policy{}, fmt.Errorf("ctl: --permission: %w", err)
	}
	return gate.Layer(gate.AllowAll(), cfgPolicy, flagPolicy), nil
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
	ctlCmd.PersistentFlags().StringVar(&ctlRepo, "repo", "", "fallback local repo path for tickets that record none (optional; repo is normally per-ticket)")
	ctlCmd.PersistentFlags().StringVar(&ctlModel, "model", "", "default model for spawned sessions (overridden per attempt)")
	ctlCmd.PersistentFlags().StringVar(&ctlConfigPath, "config", "", "supervisor config file (default $DRAIVER_CONFIG or ~/.draiver/config.json)")
	// context-window (gauge capacity) and context-limit (auto-stop threshold) both
	// default to -1, the "use the config file" sentinel newReconciler resolves.
	ctlCmd.PersistentFlags().IntVar(&ctlContextWindow, "context-window", -1, "context-window capacity (tokens) the context-% gauge is measured against (default from config, 200000)")
	ctlUpCmd.Flags().DurationVar(&ctlInterval, "interval", 5*time.Second, "reconcile tick interval")
	// Default to acceptEdits: claude's path-aware classifier auto-approves edits
	// inside the worktree (so an attempt makes progress without a human waving
	// through every write) while still *asking* — i.e. routing to the permission
	// gate — for writes outside the checkout and other risky tools. Empty (the
	// bare default mode) would instead route every in-worktree edit to the gate,
	// which escalates them, stalling the attempt (drvctl-013).
	ctlDefaultPermMode := "acceptEdits"
	ctlUpCmd.Flags().StringVar(&ctlPermMode, "permission-mode", ctlDefaultPermMode, "agent permission mode (routes tool use through the gates)")
	// The foreground drivers route tool use through the gates just like the daemon.
	ctlStartCmd.Flags().StringVar(&ctlPermMode, "permission-mode", ctlDefaultPermMode, "agent permission mode (routes tool use through the gates)")
	ctlRestartCmd.Flags().StringVar(&ctlPermMode, "permission-mode", ctlDefaultPermMode, "agent permission mode (routes tool use through the gates)")
	// The auto-stop is only meaningful where a session is actually driven: up,
	// start, restart. 0 disables it; -1 defers to the config (default 150000).
	for _, c := range []*cobra.Command{ctlUpCmd, ctlStartCmd, ctlRestartCmd} {
		c.Flags().IntVar(&ctlContextLimit, "context-limit", -1, "context-window auto-stop threshold (tokens); 0 disables (default from config, 150000)")
		// --permission tool=rule (allow|escalate) is the one-off layer over the
		// config file's permissions, itself over the allow-all auto-mode base.
		c.Flags().StringToStringVar(&ctlPermRules, "permission", nil, "per-tool permission-gate override, e.g. --permission Bash=escalate (allow|escalate); layers over config")
	}
	ctlLogsCmd.Flags().BoolVarP(&ctlLogsFollow, "follow", "f", false, "keep printing new stream lines as they are appended")
	ctlLogsCmd.Flags().BoolVar(&ctlLogsJSON, "json", false, "print the raw stream.jsonl lines verbatim (machine form for | jq / replay) instead of the human-readable rendering")
	ctlCmd.AddCommand(ctlUpCmd, ctlStartCmd, ctlStopCmd, ctlRestartCmd, ctlStatusCmd, ctlLogsCmd)
	rootCmd.AddCommand(ctlCmd)
}
