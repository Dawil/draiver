package cmd

import (
	"context"
	"fmt"
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
	"github.com/Dawil/draiver/internal/handbook"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/streamlog"
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
	ctlLogsTail      int               // -f backlog window: last N stream records before following
	ctlStatusAll     bool              // include terminal Done attempts in the list view
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
		"It also interleaves draiverctld's own operational health (session/ctl.jsonl): " +
		"error-start/error-end transitions for a wedged attempt — a worktree it cannot " +
		"cut, a model it cannot reach — so the logs show what the supervisor is doing, not " +
		"only what the agent said (drvctl-027).\n\n" +
		"--json emits the raw stream.jsonl lines verbatim, byte-for-byte: the machine " +
		"form for `| jq` and replay (health is human-render only). -f/--follow works in " +
		"both modes.",
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
		return streamlog.TailStream(ctx, cmd.OutOrStdout(),
			root.SessionStreamPath(ticket, att), root.SessionCtlLogPath(ticket, att),
			ctlLogsFollow, !ctlLogsJSON, ctlLogsTail)
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
	// Resolve the prompt-cache surface (drvctl-032). Both keys default off, so an
	// unset config leaves BaseSpec byte-identical to before.
	excludeDynamic, appendPrompt, err := resolvePromptCache(cfg, func() (bool, error) {
		return claudecode.SupportsExcludeDynamicSystemPrompt(context.Background(), "")
	})
	if err != nil {
		return nil, err
	}
	// Resolve the adapter version ONCE, here at daemon start, and pin it into every
	// session's provenance (drvctl-033). With auto-update gated off (DISABLE_AUTOUPDATER
	// below) the binary cannot move under the daemon, so one resolution holds fleet-wide.
	// Unlike the exclude-flag probe above, a failed version probe is NOT fatal: version
	// is provenance, not a correctness gate, so log it and record an empty version (the
	// canary degrades to "unattributable") rather than refusing to boot.
	adapterVersion, err := claudecode.Version(context.Background(), "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "draiverctld: adapter version probe failed, provenance will omit it: %v\n", err)
	}
	// Resolve the fleet's default coordinator supervision mode (drvctl-042). A bad
	// config value fails daemon start loudly — same posture as the permission policy
	// above — rather than silently degrading the dial to a wrong default.
	defaultSupervision, err := project.ParseSupervision(cfg.DefaultSupervision)
	if err != nil {
		return nil, fmt.Errorf("config default_supervision: %w", err)
	}
	return reconcile.New(reconcile.Options{
		Root:               root,
		DefaultRepo:        ctlRepo,
		Adapters:           claudeAdapters,
		AdapterVersion:     adapterVersion,
		Actor:              resolveActor(),
		ContextLimit:       contextLimit,
		DefaultSupervision: defaultSupervision,
		PermPolicy:         permPolicy,
		BaseSpec: agent.SessionSpec{
			Model:          ctlModel,
			PermissionMode: ctlPermMode,
			// Prompt-cache surface (drvctl-032), resolved from config above. Both are
			// off/empty by default, so an unconfigured daemon launches exactly as
			// before. Turned on, they strip the per-machine system-prompt sections and
			// carry draiver's protocol above the wall as a shared, cacheable prefix.
			ExcludeDynamicSystemPromptSections: excludeDynamic,
			SystemPromptAppend:                 appendPrompt,
			// Pin the 1-hour prompt-cache TTL on every session. The shared per-repo
			// prefix goes cold across gaps longer than the TTL — the interval between
			// tickets on a repo, or a session parked on Needs-me awaiting a human. The
			// adapter authenticates via a Claude.ai subscription (OAuth), where the 1h
			// TTL is automatic but silently drops to 5m in usage-credit overage (and is
			// 5m by default on an API key). This drop-in holds 1h regardless. See
			// docs/prompt-caching.md.
			//
			// DISABLE_AUTOUPDATER=1 gates Claude Code's background auto-update off for
			// every supervised session (drvctl-033). A mid-session upgrade would shift
			// the system prompt / tool definitions and bust the shared cache prefix
			// fleet-wide; pinning the version for a session's life is exactly the
			// invariant the control plane is placed to hold. Upgrades are operator-driven
			// and happen between sessions; the pinned version is recorded in provenance.
			Env: []string{"ENABLE_PROMPT_CACHING_1H=1", "DISABLE_AUTOUPDATER=1"},
		},
		// Route the daemon's operational log to its own stderr. Left unset it
		// defaults to a no-op — the gap drvctl-027 named: an admit that failed
		// (and every gate error) went nowhere, not even to the terminal. The
		// per-attempt *health* transitions land in each attempt's ctl.jsonl on
		// their own; this is the daemon's own operator stream beside them.
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "draiverctld: "+format+"\n", args...)
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

// resolvePromptCache resolves the drvctl-032 prompt-cache surface into the two
// BaseSpec fields the adapter consumes. The append text defaults to draiver's
// shipped invariant protocol (handbook.Content(), drvctl-034) so every session
// carries it above the wall as a shared, cacheable prefix; append_system_prompt_file
// overrides that with an operator's own file, and an override file that is empty
// disables the append entirely. Whatever the source, the text is resolved ONCE
// here so it is byte-identical across every attempt this daemon brings up (the
// prefix-sharing invariant); a named-but-unreadable override file is a hard error
// rather than a silently-empty prefix. When the exclude toggle is on it verifies
// the pinned agent accepts the flag via supports and fails loud rather than
// launching sessions without it (spec #5) — supports is a parameter so the check
// is testable without a real claude on PATH. The exclude toggle still defaults off,
// so an unconfigured daemon appends the protocol without stripping the wall (the
// cross-ticket cache win is the opt-in exclude lever, docs/prompt-caching.md).
func resolvePromptCache(cfg config.Config, supports func() (bool, error)) (exclude bool, appendPrompt string, err error) {
	appendPrompt = handbook.Content()
	if cfg.AppendSystemPromptFile != "" {
		data, rerr := os.ReadFile(cfg.AppendSystemPromptFile)
		if rerr != nil {
			return false, "", fmt.Errorf("ctl: read append_system_prompt_file %q: %w", cfg.AppendSystemPromptFile, rerr)
		}
		appendPrompt = string(data)
	}
	if cfg.ExcludeDynamicSystemPromptSections {
		ok, serr := supports()
		if serr != nil {
			return false, "", fmt.Errorf("ctl: verify --exclude-dynamic-system-prompt-sections support: %w", serr)
		}
		if !ok {
			return false, "", fmt.Errorf("ctl: exclude_dynamic_system_prompt_sections is set but the pinned `claude` does not accept " +
				"--exclude-dynamic-system-prompt-sections; upgrade the agent or pin a supported version (drvctl-033), or unset the toggle")
		}
	}
	return cfg.ExcludeDynamicSystemPromptSections, appendPrompt, nil
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
	// -f follows from the last N stream records (tail semantics) rather than
	// replaying the whole history — a follow is a "what's happening now" request,
	// and the webui's Agent Logs panel consumes exactly `logs -f`, where a
	// full-history flood froze the page (drvctl-030). 0 = no backlog; -1 = full
	// history (the old behaviour). Meaningful only with -f; ignored without it.
	ctlLogsCmd.Flags().IntVar(&ctlLogsTail, "tail", 50, "with -f, start from the last N stream records then follow (0 = none, -1 = full history); ignored without -f")
	ctlStatusCmd.Flags().BoolVarP(&ctlStatusAll, "all", "a", false, "include terminal Done attempts (hidden by default in the list view)")
	ctlCmd.AddCommand(ctlUpCmd, ctlStartCmd, ctlStopCmd, ctlRestartCmd, ctlStatusCmd, ctlLogsCmd)
	rootCmd.AddCommand(ctlCmd)
}
