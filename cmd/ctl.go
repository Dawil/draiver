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
	"github.com/Dawil/draiver/internal/project"
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
	ctlStatusAll     bool // include terminal Done attempts in the list view
	ctlPermRules     map[string]string // --permission tool=rule, layered over config

	// restart depth flags — the cumulative degree axis (drvctl-016). The deepest
	// one passed wins; a deeper flag implies every shallower flush.
	ctlRestartNewSession  bool
	ctlRestartNewWorktree bool
	ctlRestartNewAttempt  bool

	// start --new-attempt forks a new attempt off the target and starts the fork,
	// leaving the parent as it is (the parallel-branch counterpart of restart
	// --new-attempt, which parks the parent instead — drvctl-016 / decision #23).
	ctlStartNewAttempt bool
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
		// Claim PID 1 for this data root with a fresh boot nonce, so imperative
		// `ctl start`/`restart` can find this supervisor and stamp their transient
		// desired-markers with a nonce that dies when this daemon does (drvctl-016).
		nonce, err := reconcile.NewNonce()
		if err != nil {
			return err
		}
		ctrl := reconcile.Controller{PID: os.Getpid(), Nonce: nonce, Started: time.Now()}
		fmt.Fprintf(cmd.OutOrStdout(), "draiverctld %s up (pid %d) — %s, tick every %s (Ctrl-C to drain)\n", version, ctrl.PID, repoNote, ctlInterval)
		return r.Run(ctx, ctlInterval, ctrl)
	},
}

var ctlStartCmd = &cobra.Command{
	Use:   "start <ticket[@attempt]>",
	Short: "Hand an attempt to a running `ctl up` to bring up in the background",
	Long: "start hands one attempt off to a running `ctl up` supervisor, which brings " +
		"it up in the background — resuming the recorded session, or spawning a fresh " +
		"one (cold-started from the brief) if there is none — through the same self-heal " +
		"cascade and gate/protocol pipeline the enabled fleet uses.\n\n" +
		"It is imperative and transient: it does not persist like `enable` and is swept " +
		"if the supervisor restarts, so it never leaves an unsupervised orphan. start " +
		"requires a running `ctl up`; it errors if none is up. The session runs " +
		"headless — watch it with `ctl logs -f <ticket[@attempt]>`.\n\n" +
		"--new-attempt forks a NEW attempt off the target (new id, provenance to it) and " +
		"starts the fork, leaving the parent as it is — a parallel branch that runs " +
		"alongside it. Contrast `restart --new-attempt`, which parks the parent.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, ticket, att, err := newCtlTarget(args[0])
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if ctlStartNewAttempt {
			res, err := r.StartNewAttempt(ticket, att)
			if err != nil {
				return err
			}
			fmt.Fprintf(out,
				"draiverctl start %s@%s --new-attempt — forked a new attempt %s/%s (from %s) and handed it to draiverctld (pid %d), leaving the parent running; watch with `ctl logs -f %s@%s`\n",
				ticket, att, ticket, res.Attempt, res.From, res.ControllerPID, ticket, res.Attempt)
			return nil
		}
		res, err := r.Start(ticket, att)
		if err != nil {
			return err
		}
		fmt.Fprintf(out,
			"handed %s/%s to draiverctld (pid %d) — it will come up shortly; watch with `ctl logs -f %s@%s`\n",
			ticket, att, res.ControllerPID, ticket, att)
		return nil
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
	Short: "Reap, flush to a chosen depth, and hand the attempt to `ctl up` to bring back up",
	Long: "restart reaps the current session, flushes volatile state to a chosen depth, then " +
		"hands the attempt off to a running `ctl up` to bring back up in the background — the " +
		"same imperative-transient handoff `start` uses. It requires a running `ctl up` " +
		"(errors otherwise, never orphans) and returns immediately; watch with `ctl logs -f`.\n\n" +
		"With no flag it reaps only the process and the daemon resumes the same session — a " +
		"clean continue, no re-brief. The depth flags flush deeper volatile layers before the " +
		"cascade climbs back; they are cumulative (a deeper flag implies the shallower flushes), " +
		"and if more than one is given the deepest wins:\n\n" +
		"  --new-session   discard the conversation (L0); spawn a fresh session on the same " +
		"worktree, cold-started from the brief.\n" +
		"  --new-worktree  also rebuild the worktree from HEAD (L1) — discards uncommitted " +
		"work in the checkout.\n" +
		"  --new-attempt   FORK a new attempt (L2): a new attempt id with provenance to this " +
		"one, started fresh, and PARK this one (it leaves the supervised fleet). This attempt's " +
		"log is preserved untouched — restart lands on a *different* attempt.\n\n" +
		"A re-brief happens only when the session layer was flushed.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, ticket, att, err := newCtlTarget(args[0])
		if err != nil {
			return err
		}
		level := restartLevel()
		res, err := r.Restart(cmd.Context(), ticket, att, level)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		switch {
		case res.Forked:
			fmt.Fprintf(out, "draiverctl restart %s@%s --new-attempt — forked a new attempt %s/%s (from %s) and parked the parent; handed the fork to draiverctld (pid %d), cold-starting it fresh. Watch with `ctl logs -f %s@%s`\n",
				ticket, att, ticket, res.Attempt, res.From, res.ControllerPID, ticket, res.Attempt)
		case res.Level == reconcile.FlushWorktree:
			fmt.Fprintf(out, "draiverctl restart %s@%s --new-worktree — rebuilding the worktree from HEAD, cold-starting from brief; handed to draiverctld (pid %d). Watch with `ctl logs -f %s@%s`\n",
				ticket, att, res.ControllerPID, ticket, att)
		case res.Level == reconcile.FlushSession:
			fmt.Fprintf(out, "draiverctl restart %s@%s --new-session — fresh session on the same worktree, cold-starting from brief; handed to draiverctld (pid %d). Watch with `ctl logs -f %s@%s`\n",
				ticket, att, res.ControllerPID, ticket, att)
		default:
			fmt.Fprintf(out, "draiverctl restart %s@%s — reaping and resuming the same session; handed to draiverctld (pid %d). Watch with `ctl logs -f %s@%s`\n",
				ticket, att, res.ControllerPID, ticket, att)
		}
		return nil
	},
}

// restartLevel resolves the restart depth flags into a single FlushLevel. The
// flags are cumulative and the deepest one passed wins (--new-attempt implies a
// new worktree implies a new session), so they are checked deepest-first.
func restartLevel() reconcile.FlushLevel {
	switch {
	case ctlRestartNewAttempt:
		return reconcile.FlushAttempt
	case ctlRestartNewWorktree:
		return reconcile.FlushWorktree
	case ctlRestartNewSession:
		return reconcile.FlushSession
	default:
		return reconcile.FlushNone
	}
}

var ctlStatusCmd = &cobra.Command{
	Use:   "status [ticket[@attempt]]",
	Short: "Live view per attempt (Done hidden by default; --all shows them): control state, session pid, model, tokens/$ and context-window %",
	Long: "status prints one row per attempt from the reconciler snapshot — control " +
		"state, session pid, enabled/desired, model, context-window fill and cost.\n\n" +
		"By default it omits terminal Done attempts so the live view stays focused on " +
		"the Running / Needs-me / Review attempts that still want attention; --all/-a " +
		"restores the full list. Naming an attempt explicitly (`status ticket[@attempt]`) " +
		"always prints it regardless of state.",
	Args: cobra.RangeArgs(0, 1),
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
		// The Done filter applies only to the unfiltered list view; an explicit
		// target arg named the attempt by hand, so it prints regardless of state.
		explicit := wantTicket != ""
		var printed, hiddenDone int
		for _, s := range snap {
			if explicit && (s.Key.Ticket != wantTicket || s.Key.Attempt != wantAtt) {
				continue
			}
			if !explicit && !ctlStatusAll && s.State == project.Done {
				hiddenDone++
				continue
			}
			printed++
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
		// A blank list view reads as "nothing running" when the truth may be
		// "everything is Done and hidden"; say so, with the count and the opt-in.
		if printed == 0 && hiddenDone > 0 {
			fmt.Fprintf(out, "no active attempts — %d Done hidden; --all to show\n", hiddenDone)
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
	// Per-session config (permission mode, context-window auto-stop, permission
	// overrides) lives only on `up`. The daemon owns every session it brings up, and
	// since start/restart became transient daemon handoffs that drive no session of
	// their own (drvctl-016 C2), these flags would be dead on them.
	ctlDefaultPermMode := "acceptEdits"
	ctlUpCmd.Flags().StringVar(&ctlPermMode, "permission-mode", ctlDefaultPermMode, "agent permission mode (routes tool use through the gates)")
	ctlUpCmd.Flags().IntVar(&ctlContextLimit, "context-limit", -1, "context-window auto-stop threshold (tokens); 0 disables (default from config, 150000)")
	ctlUpCmd.Flags().StringToStringVar(&ctlPermRules, "permission", nil, "per-tool permission-gate override, e.g. --permission Bash=escalate (allow|escalate); layers over config")
	// restart depth flags — the cumulative degree axis (drvctl-016); the deepest
	// one passed selects how many volatile layers are flushed before the cascade
	// climbs back.
	ctlRestartCmd.Flags().BoolVar(&ctlRestartNewSession, "new-session", false, "flush the session conversation (L0): spawn a fresh session on the same worktree, cold-started from the brief")
	ctlRestartCmd.Flags().BoolVar(&ctlRestartNewWorktree, "new-worktree", false, "flush the worktree too (L1): rebuild it from HEAD, discarding uncommitted work; implies --new-session")
	ctlRestartCmd.Flags().BoolVar(&ctlRestartNewAttempt, "new-attempt", false, "fork a new attempt (L2) with provenance to this one and start the fork fresh, parking this attempt; its log is preserved")
	// start --new-attempt forks a parallel branch (the fork runs alongside the
	// parent) — contrast restart --new-attempt, which parks the parent.
	ctlStartCmd.Flags().BoolVar(&ctlStartNewAttempt, "new-attempt", false, "fork a new attempt (new id, provenance to this one) and start the fork, leaving the parent running — a parallel branch")
	ctlLogsCmd.Flags().BoolVarP(&ctlLogsFollow, "follow", "f", false, "keep printing new stream lines as they are appended")
	ctlLogsCmd.Flags().BoolVar(&ctlLogsJSON, "json", false, "print the raw stream.jsonl lines verbatim (machine form for | jq / replay) instead of the human-readable rendering")
	ctlStatusCmd.Flags().BoolVarP(&ctlStatusAll, "all", "a", false, "include terminal Done attempts (hidden by default in the list view)")
	ctlCmd.AddCommand(ctlUpCmd, ctlStartCmd, ctlStopCmd, ctlRestartCmd, ctlStatusCmd, ctlLogsCmd)
	rootCmd.AddCommand(ctlCmd)
}
