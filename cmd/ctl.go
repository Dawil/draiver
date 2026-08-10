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

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
	"golang.org/x/term"

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
	ctlLogsTail      int // -f backlog window: last N stream records before following
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
		return tailStream(ctx, cmd.OutOrStdout(),
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

// The role vocabulary event kinds map onto — a small, stable set that replaces
// the old glyph markers (`>`, `!`, `--`, `?`). Each renders in a distinct colour
// on a TTY so a reader can tell who is speaking without decoding a marker.
const (
	roleAssistant = "assistant"
	roleTool      = "tool"
	roleSystem    = "system"
	rolePerm      = "perm"
	roleError     = "error"
)

// Prefix column widths. The datetime is a fixed 19-column stamp
// (`2006-01-02 15:04:05`); the tokens column is right-aligned so it stays put as
// the number grows; the role column is left-aligned so the content start-column
// is stable. prefixWidth is the total visible width of the `[<prefix>]: ` string,
// used to leave the markdown renderer room to wrap inside the terminal.
const (
	tsLayout   = "2006-01-02 15:04:05"
	tokenWidth = 9
	roleWidth  = 9
	// "[" + 19 (datetime) + "  " + tokenWidth + "  " + roleWidth + "]: "
	prefixWidth = 1 + 19 + 2 + tokenWidth + 2 + roleWidth + 3
)

// ANSI attributes used on a TTY only. roleColor colours the role token in the
// prefix (not the datetime, tokens or brackets — drvctl-025 #17); ansiItalic
// marks tool output so it reads as visibly distinct from prose (drvctl-025 #8).
const (
	ansiReset  = "\033[0m"
	ansiItalic = "\033[3m"
)

func roleColor(role string) string {
	switch role {
	case roleAssistant:
		return "\033[36m" // cyan
	case roleTool:
		return "\033[33m" // yellow
	case roleSystem:
		return "\033[90m" // grey
	case rolePerm:
		return "\033[35m" // magenta
	case roleError:
		return "\033[31m" // red
	}
	return ""
}

// streamRenderer turns normalized events into the human-readable session render:
// a fixed-width, greppable `[<time>  <tokens>  <role>]: ` prefix on every line,
// followed by the event body — with assistant prose rendered from Markdown to
// styled ANSI (glamour over the goldmark parser this repo already carries).
//
// It is stateful, so one value is built per stream and reused for every line: it
// carries the last-known cumulative context-token count forward across the many
// events that report no usage, so the tokens column is populated on every line
// rather than only on usage frames. It is the single renderer shared by the live
// `ctl logs -f` follow and disk replay alike; transport fields (event uuids, the
// session id, envelope wrappers) are dropped.
//
// The timestamp is render-time wallclock captured as each line is emitted, NOT a
// recorded event time — the normalized event and the stream-json envelope carry
// none, and the on-disk stream.jsonl format and `--json` passthrough must stay
// byte-for-byte unchanged (drvctl-025 #3). It is truthful for the live follow and
// honest-but-approximate for historical replay.
type streamRenderer struct {
	out       io.Writer
	styled    bool // out is a TTY: colours + italics + ANSI markdown on
	md        *glamour.TermRenderer
	ctxTokens int // last-known cumulative context tokens; -1 until first usage
	now       func() time.Time
}

// newStreamRenderer builds a renderer bound to out, detecting whether out is a
// terminal. On a TTY it styles: role-coloured prefixes, italic tool output, and
// glamour's dark ANSI markdown wrapped to the terminal width minus the prefix.
// When out is not a terminal (a pipe, a file, the test buffer) it renders plain —
// glamour's notty style, which shows emphasis as literal `**bold**` markers
// rather than leaking ANSI escapes into a `| jq`/file consumer (drvctl-025 #5).
func newStreamRenderer(out io.Writer) *streamRenderer {
	r := &streamRenderer{out: out, ctxTokens: -1, now: time.Now}
	width := 100
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		r.styled = true
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > prefixWidth+20 {
			width = w
		}
	}
	wrap := width - prefixWidth
	if wrap < 20 {
		wrap = 20
	}
	base := styles.NoTTYStyleConfig
	profile := termenv.Ascii
	if r.styled {
		base = styles.DarkStyleConfig
		profile = termenv.ANSI256
	}
	md, err := glamour.NewTermRenderer(
		glamour.WithStyles(compactStyle(base)),
		glamour.WithColorProfile(profile),
		glamour.WithWordWrap(wrap),
	)
	if err == nil {
		r.md = md
	}
	return r
}

// compactStyle strips glamour's document framing — the 2-space margin and the
// leading/trailing blank lines it wraps every block in — so the rendered body
// sits flush against our prefix column with no gutter (drvctl-025 #5). It copies
// the base config by value; only the Document fields are replaced.
func compactStyle(base ansi.StyleConfig) ansi.StyleConfig {
	s := base
	zero := uint(0)
	s.Document.Margin = &zero
	s.Document.BlockPrefix = ""
	s.Document.BlockSuffix = ""
	return s
}

// renderEvent writes one normalized event. Assistant prose is rendered as styled
// Markdown; tool calls, tool errors, permission asks, turn boundaries and errors
// map to their role. Usage-only frames print nothing (a bare ctx/tok/$ line is
// noise, per drvctl-025 #8) but still advance the token tally the prefix carries.
func (r *streamRenderer) renderEvent(ev agent.Event) {
	// Fold any usage this event carries into the running tally first, so the
	// tokens column reflects it even on the line that reported it. Only a
	// positive count updates the tally: a result frame reports cost-only usage
	// with ContextTokens==0 (see claudecode.Normalize), which must not clobber
	// the last real context-window snapshot the column carries forward.
	if ev.Usage != nil && ev.Usage.ContextTokens > 0 {
		r.ctxTokens = ev.Usage.ContextTokens
	}
	switch ev.Kind {
	case agent.EventSystem:
		// The session id is a transport handle, not something a human reading the
		// stream needs; note only that the session came online.
		r.emit(roleSystem, "session online")
	case agent.EventAssistant:
		if ev.Thinking {
			return
		}
		if s := strings.TrimSpace(ev.Text); s != "" {
			r.emit(roleAssistant, s)
		}
	case agent.EventToolCall:
		if ev.Tool != nil {
			if s := toolSummary(ev.Tool.Input); s != "" {
				r.emit(roleTool, ev.Tool.Name+": "+s)
			} else {
				r.emit(roleTool, ev.Tool.Name)
			}
		}
	case agent.EventToolResult:
		if ev.Tool != nil && ev.Tool.IsError {
			r.emit(roleTool, ev.Tool.Name+" failed")
		}
	case agent.EventPermission:
		if ev.Permission != nil {
			r.emit(rolePerm, "permission: "+ev.Permission.Tool)
		}
	case agent.EventUsage:
		// The ctx/tok/$ figures ride along on the next real line's prefix; a
		// dedicated line for them is dropped (drvctl-025 #8).
		return
	case agent.EventTurnEnd:
		r.emit(roleSystem, "turn end ("+ev.Turn+")")
	case agent.EventError:
		r.emit(roleError, ev.Err)
	}
}

// emit writes body under role at render-time wallclock, one physical line at a
// time, each carrying the full `[<prefix>]: ` so every line stays independently
// greppable (drvctl-025 #8).
func (r *streamRenderer) emit(role, body string) {
	r.emitAt(role, r.now().Format(tsLayout), body)
}

// emitAt is emit with an explicit timestamp column, for a source that carries its
// own recorded time (the ctl.jsonl health log) rather than the stream's render-time
// approximation.
func (r *streamRenderer) emitAt(role, ts, body string) {
	prefix := r.prefixAt(role, ts)
	for _, line := range r.bodyLines(role, body) {
		fmt.Fprintln(r.out, prefix+line)
	}
}

// prefix renders the `[<datetime>  <tokens>  <role>]: ` column for one line. The
// bracket-and-colon are wrapped in `[` … `]: ` so a reader (or grep) can split
// prefix from content on a fixed delimiter; on a TTY only the role token is
// tinted by its colour — the datetime, tokens, brackets and delimiter stay
// uncoloured (drvctl-025 #17).
func (r *streamRenderer) prefix(role string) string {
	return r.prefixAt(role, r.now().Format(tsLayout))
}

// prefixAt renders the prefix column with an explicit datetime stamp, so a source
// carrying its own recorded time (ctl.jsonl health) sits in the same fixed-width
// column as the render-time-stamped agent stream.
func (r *streamRenderer) prefixAt(role, ts string) string {
	tok := "-"
	if r.ctxTokens >= 0 {
		tok = commas(r.ctxTokens)
	}
	// Pad the role to a fixed column by hand: on a TTY the colour escapes wrap
	// only the role word, so %-*s (which counts the escape bytes) would misalign
	// the closing bracket. Left-align the visible word, then trailing spaces.
	roleField := role
	if r.styled {
		roleField = roleColor(role) + role + ansiReset
	}
	if pad := roleWidth - len(role); pad > 0 {
		roleField += strings.Repeat(" ", pad)
	}
	return fmt.Sprintf("[%s  %*s  %s]: ", ts, tokenWidth, tok, roleField)
}

// bodyLines turns an event body into the physical lines to print under the
// prefix. Assistant prose is rendered from Markdown (styled ANSI on a TTY, plain
// otherwise), split into lines with glamour's padding and blank framing stripped;
// tool output is italicised on a TTY so it reads as visibly distinct; everything
// else is a single plain line.
func (r *streamRenderer) bodyLines(role, body string) []string {
	if role == roleAssistant && r.md != nil {
		if rendered, err := r.md.Render(body); err == nil {
			var lines []string
			for _, ln := range strings.Split(rendered, "\n") {
				if ln = trimTrailingBlank(ln); ln != "" {
					lines = append(lines, ln)
				}
			}
			if len(lines) > 0 {
				return lines
			}
		}
	}
	if role == roleTool && r.styled {
		return []string{ansiItalic + body + ansiReset}
	}
	return []string{body}
}

// trimTrailingBlank strips trailing whitespace from a rendered line, seeing
// through the per-cell colour escapes glamour uses to right-pad every line to the
// wrap width — a plain strings.TrimRight can't, because such a line ends in an
// ANSI reset, not a space (drvctl-025 #5). It skips CSI escape sequences while
// tracking the last visible non-space byte, drops everything past it, and (if any
// colour survived) re-appends a reset so no styling bleeds past the content. A
// line that is blank once its padding and escapes are removed returns "".
func trimTrailingBlank(s string) string {
	lastVisible := -1
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // ESC: skip a CSI sequence (ESC [ … final-byte 0x40–0x7e)
			j := i + 1
			if j < len(s) && s[j] == '[' {
				for j++; j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e); j++ {
				}
				if j < len(s) {
					j++ // include the final byte
				}
			}
			i = j
			continue
		}
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\r' {
			lastVisible = i
		}
		i++
	}
	if lastVisible < 0 {
		return ""
	}
	trimmed := s[:lastVisible+1]
	if strings.Contains(trimmed, "\x1b[") {
		trimmed += ansiReset
	}
	return trimmed
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

// tailStream prints an attempt's recorded logs. In raw mode (--json) it emits the
// stream.jsonl lines verbatim, byte-for-byte, so `| jq` pipelines keep working —
// the machine form is unchanged, and ctl.jsonl health is *not* folded in (it is a
// human-render concern only). In render mode — the human-readable default — it
// interleaves two sources through one stateful renderer: the agent stream
// (stream.jsonl, normalized back into the live view's event vocabulary with
// transport noise dropped) and draiverctld's own operational health transitions
// (ctl.jsonl, drvctl-027). Without follow it prints what is on disk and returns;
// with follow it keeps emitting appended lines from both files until ctx is
// cancelled (Ctrl-C), waiting for either to appear if the session has not started.
//
// tail is the follow-mode backlog window over stream.jsonl: a follow is a "show me
// what's happening now" request, so rather than replay the whole recorded history
// on start it emits only the last `tail` stream records and then follows live.
// tail == 0 shows no backlog (only lines appended after the follow starts); tail
// < 0 replays the full history (today's behaviour, the escape hatch). The window
// is meaningful only under follow — without it the full history always prints (cat
// semantics) — and it applies to the stream.jsonl backlog only: the interleaved
// ctl.jsonl health lines are sparse and kept in full (drvctl-030).
func tailStream(ctx context.Context, out io.Writer, streamPath, ctlPath string, follow, render bool, tail int) error {
	// The backlog window is a follow-mode concern; a plain `logs` (cat) always
	// prints the whole file, and health is never windowed.
	streamTail := -1
	if follow {
		streamTail = tail
	}
	if !render {
		return tailRaw(ctx, out, streamPath, follow, streamTail)
	}

	// One renderer for both sources: it carries the running token tally forward, and
	// a shared prefix column keeps the two visually aligned as they interleave.
	sr := newStreamRenderer(out)
	stream := &lineReader{path: streamPath, tail: streamTail}
	ctl := &lineReader{path: ctlPath, tail: -1}
	defer stream.close()
	defer ctl.close()

	printed := false
	for {
		n := stream.drain(func(line string) { sr.renderLine(line); printed = true })
		n += ctl.drain(func(line string) { sr.renderHealthLine(line); printed = true })
		if !follow {
			// Flush any partial trailing line held for a newline that will not come.
			if stream.flush(func(line string) { sr.renderLine(line); printed = true }) {
				printed = true
			}
			if ctl.flush(func(line string) { sr.renderHealthLine(line); printed = true }) {
				printed = true
			}
			if !printed {
				fmt.Fprintln(out, "(no session stream yet)")
			}
			return nil
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
}

// tailRaw is the --json path: the stream.jsonl bytes, verbatim. It preserves the
// exact pre-drvctl-027 behaviour — a `| jq` / replay consumer sees the on-disk
// journal untouched, and health never enters this stream. In follow mode it honours
// the same backlog window as the render path (tail records, seeked to a record
// boundary so the bytes stay byte-for-byte); non-follow always emits the full file
// (tail < 0), which is where replay-from-start is served.
func tailRaw(ctx context.Context, out io.Writer, path string, follow bool, tail int) error {
	f, err := openStream(ctx, path, follow)
	if err != nil {
		return err
	}
	if f == nil {
		if !follow {
			fmt.Fprintln(out, "(no session stream yet)")
		}
		return nil
	}
	defer f.Close()
	if off, err := tailOffset(f, tail); err == nil {
		_, _ = f.Seek(off, io.SeekStart)
	}
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

// tailOffset returns the byte offset in f from which reading yields the last n
// complete (newline-terminated) lines — the seek target for a follow's backlog
// window. n < 0 means the whole file (offset 0); n == 0 means only content
// appended after the current end (offset == size). It scans backward from EOF
// counting record-terminating newlines, ignoring a single trailing newline at the
// very end (which terminates the last line rather than starting a new one), and
// stops at the start of the nth-from-last line; a file with fewer than n lines
// yields offset 0 (the whole file). A trailing partial line (no newline yet) is
// kept within the window so its bytes reach the reader's pending buffer.
func tailOffset(f *os.File, n int) (int64, error) {
	if n < 0 {
		return 0, nil
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if n == 0 || size == 0 {
		return size, nil
	}
	const chunk = 32 * 1024
	buf := make([]byte, chunk)
	count := 0
	pos := size
	skipTrailing := true // the final '\n' terminates the last line, not a new one
	for pos > 0 {
		readSize := int64(chunk)
		if pos < readSize {
			readSize = pos
		}
		start := pos - readSize
		if _, err := f.ReadAt(buf[:readSize], start); err != nil && err != io.EOF {
			return 0, err
		}
		for i := int(readSize) - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			abs := start + int64(i)
			if skipTrailing && abs == size-1 {
				skipTrailing = false
				continue
			}
			skipTrailing = false
			count++
			if count == n {
				return abs + 1, nil
			}
		}
		pos = start
	}
	return 0, nil
}

// lineReader tails one append-only file a line at a time, opening it lazily (so a
// file that does not exist yet is simply skipped until it appears) and holding a
// partial trailing line until its newline arrives — so a half-written JSON object
// is never handed to a parser. drain reads every complete line available right now;
// flush releases a held partial line at end-of-input in non-follow mode. It is the
// tailing primitive tailStream interleaves the agent stream and the health log
// through.
type lineReader struct {
	path string
	// tail is the initial backlog window, applied once when the file is first
	// opened: keep the last `tail` complete lines (tail < 0 = the whole file, the
	// default; tail == 0 = none, i.e. seek to the current end and only report lines
	// appended afterwards). Later lines appended past the initial seek are always
	// reported — the window bounds the backlog, not the follow.
	tail    int
	f       *os.File
	r       *bufio.Reader
	pending string
}

// drain reads all currently-available complete lines, calling emit for each, and
// returns how many it emitted. It lazily opens the file on first use (and each
// call, until it exists). A bufio.Reader does not cache EOF, so re-draining the
// same reader after the file has grown yields the new lines — the tail property.
func (lr *lineReader) drain(emit func(string)) int {
	if lr.f == nil {
		f, err := os.Open(lr.path)
		if err != nil {
			return 0 // not created yet (or unreadable) — try again next drain
		}
		lr.f = f
		// Apply the backlog window once, at open: seek past everything but the last
		// `tail` records so a follow starts near the tip instead of replaying the
		// whole history. tailOffset lands on a record boundary, so the reader still
		// only ever sees complete lines.
		if off, err := tailOffset(f, lr.tail); err == nil {
			_, _ = f.Seek(off, io.SeekStart)
		}
		lr.r = bufio.NewReader(f)
	}
	n := 0
	for {
		line, err := lr.r.ReadString('\n')
		if len(line) > 0 {
			if strings.HasSuffix(line, "\n") {
				emit(lr.pending + line)
				lr.pending = ""
				n++
			} else {
				lr.pending += line
			}
		}
		if err != nil {
			return n // EOF (or read error): stop this pass, keep position for the next
		}
	}
}

// flush emits any held partial trailing line (a final record written without a
// newline), used only at end-of-input in non-follow mode. It reports whether it
// emitted anything.
func (lr *lineReader) flush(emit func(string)) bool {
	if lr.pending == "" {
		return false
	}
	emit(lr.pending)
	lr.pending = ""
	return true
}

func (lr *lineReader) close() {
	if lr.f != nil {
		lr.f.Close()
	}
}

// renderLine normalizes one recorded stream-json line and renders the events it
// yields through the shared renderer; lines that carry only transport noise
// normalize to nothing and print nothing.
func (r *streamRenderer) renderLine(line string) {
	for _, ev := range claudecode.Normalize([]byte(line)) {
		r.renderEvent(ev)
	}
}

// renderHealthLine renders one session/ctl.jsonl record — a daemon health
// transition — through the shared prefix vocabulary so it interleaves with the
// agent stream. An error-start is an error-role line (the trouble beginning, with
// its message); an error-end is a system-role line (it cleared). Unlike the agent
// stream, a health record carries its own recorded timestamp, so the prefix uses
// that rather than render-time wallclock. A malformed or unknown record prints
// nothing.
func (r *streamRenderer) renderHealthLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev reconcile.HealthEvent
	if json.Unmarshal([]byte(line), &ev) != nil {
		return
	}
	ts := r.now().Format(tsLayout)
	if t, err := time.Parse(time.RFC3339, ev.TS); err == nil {
		ts = t.Local().Format(tsLayout)
	}
	switch ev.Event {
	case "start":
		body := "ctl: " + ev.Class + " started"
		if ev.Message != "" {
			body += ": " + ev.Message
		}
		r.emitAt(roleError, ts, body)
	case "end":
		r.emitAt(roleSystem, ts, "ctl: "+ev.Class+" cleared")
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
	// Resolve the prompt-cache surface (drvctl-032). Both keys default off, so an
	// unset config leaves BaseSpec byte-identical to before.
	excludeDynamic, appendPrompt, err := resolvePromptCache(cfg, func() (bool, error) {
		return claudecode.SupportsExcludeDynamicSystemPrompt(context.Background(), "")
	})
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
			Env: []string{"ENABLE_PROMPT_CACHING_1H=1"},
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
// BaseSpec fields the adapter consumes. The append-prompt file is read ONCE here
// so its text is byte-identical across every attempt this daemon brings up (the
// prefix-sharing invariant); a named-but-unreadable file is a hard error rather
// than a silently-empty prefix. When the exclude toggle is on it verifies the
// pinned agent accepts the flag via supports and fails loud rather than launching
// sessions without it (spec #5) — supports is a parameter so the check is testable
// without a real claude on PATH. With both keys off (the default) it returns the
// zero surface and never probes, keeping the launch byte-identical to today.
func resolvePromptCache(cfg config.Config, supports func() (bool, error)) (exclude bool, appendPrompt string, err error) {
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
