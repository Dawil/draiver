package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/completion"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// steps_lifecycle_drvctl060.go realises drvctl-060 (features/session-lifecycle/):
//
//   - identity: the REAL draiver binary is shelled with exactly the env the daemon's
//     bringUp composes (agentmsg.SessionEnv + agentmsg.IdentityEnv), with every
//     DRAIVER_ACTOR/DRAIVER_ATTEMPT the test process inherited stripped first;
//   - background work: a REAL completion.Gate is fed the normalized frames of an
//     async-subagent launch and a bare turn end;
//   - interrupt: a REAL reconcile.Reconciler ticks over an enabled attempt held by an
//     `interrupt` event. Its adapter resolver is the admission probe — bringUp calls
//     it first thing, so a resolver call means "the supervisor tried to bring the
//     session back up".

const d60Adapter = "claude-code"

func init() {
	for text, fn := range map[string]StepFunc{
		"a drvctl-060 attempt with a supervised session environment from the daemon": d60GivenSession,
		"the agent runs draiver log with no exports of its own":                      d60LogNoExports,
		"the logged note is attributed to agent:claude-code":                         d60ThenAgentActor,
		"the agent runs draiver log with DRAIVER_ACTOR overridden to a human":        d60LogAsHuman,
		"the write is refused with an attribution error and nothing is appended":     d60ThenRefused,

		"a drvctl-060 completion gate on a session that launched an async subagent": d60GivenGate,
		"the session ends its turn without a hand-off":                              d60TurnEnds,
		"the gate waits — no nudge, no escalation and no halt":                      d60ThenWaiting,
		"the subagent's completion notice clears the outstanding task":              d60ThenNotified,

		"a drvctl-060 enabled attempt whose session the human interrupted": d60GivenInterrupted,
		"the supervisor ticks several times":                               d60Tick,
		"the interrupted attempt is not brought back up":                   d60ThenNotResumed,
		"a message from the human to the agent releases the hold":          d60ThenMessageLifts,
		"the board shows the attempt Pending as interrupted by human":      d60ThenBoardLabel,
	} {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

// d60World is one scenario's fixture.
type d60World struct {
	b *board

	// identity
	envUsed        []string
	stdout, stderr string
	code           int
	before         int

	// completion gate
	gate    *completion.Gate
	nudges  int
	halts   int
	trace   []string
	outcome completion.Outcome

	// reconciler
	rec      *reconcile.Reconciler
	mu       sync.Mutex
	resolves int
}

func d60Get(w *World) (*d60World, error) {
	d, _ := w.get("d60").(*d60World)
	if d == nil {
		return nil, errors.New("no drvctl-060 fixture in scope")
	}
	return d, nil
}

func d60Seed(w *World) (*d60World, error) {
	b, err := seedBoard(w, "levels: []\n")
	if err != nil {
		return nil, err
	}
	w.set("board", b)
	d := &d60World{b: b}
	w.set("d60", d)
	return d, nil
}

func (d *d60World) logLen() (int, error) {
	evs, err := ticketlog.Read(d.b.root, accTicket, accAtt)
	return len(evs), err
}

// --- identity -----------------------------------------------------------------

func d60GivenSession(w *World, sr *StepRun) error {
	d, err := d60Seed(w)
	if err != nil {
		return err
	}
	n, err := d.logLen()
	if err != nil {
		return err
	}
	// Exactly what bringUp hands the spawned agent, layered over a parent env with
	// any inherited identity stripped (the test runner may itself run in a session).
	drop := map[string]bool{
		"DRAIVER_DATA": true, "DRAIVER_CONFIG": true,
		agentmsg.EnvActor: true, agentmsg.EnvAgentAttempt: true,
		agentmsg.EnvTicket: true, agentmsg.EnvAttempt: true, agentmsg.EnvSeq: true,
	}
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && drop[e[:i]] {
			continue
		}
		d.envUsed = append(d.envUsed, e)
	}
	d.envUsed = append(d.envUsed, "DRAIVER_DATA="+d.b.root.Dir)
	d.envUsed = append(d.envUsed, agentmsg.SessionEnv(accTicket, accAtt, n)...)
	d.envUsed = append(d.envUsed, agentmsg.IdentityEnv(d60Adapter, accAtt)...)
	return nil
}

func (d *d60World) runLog(extraEnv []string, body string) error {
	bin, err := overlayBin()
	if err != nil {
		return err
	}
	n, err := d.logLen()
	if err != nil {
		return err
	}
	d.before = n
	// No --attempt and no --actor: both must come from the daemon-provided env.
	cmd := exec.Command(bin, "log", accTicket, "--type", "note", body)
	cmd.Dir = d.b.wd
	cmd.Env = append(append([]string(nil), d.envUsed...), extraEnv...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	d.stdout, d.stderr, d.code = out.String(), errb.String(), 0
	var ee *exec.ExitError
	switch {
	case errors.As(runErr, &ee):
		d.code = ee.ExitCode()
	case runErr != nil:
		return runErr
	}
	return nil
}

func d60LogNoExports(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	return d.runLog(nil, "Found the flaky test: it races the watcher.")
}

func d60LogAsHuman(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	return d.runLog([]string{agentmsg.EnvActor + "=human:dave"}, "This would be misattributed.")
}

func d60ThenAgentActor(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	if d.code != 0 {
		return fmt.Errorf("draiver log exited %d: %s", d.code, d.stderr)
	}
	evs, err := ticketlog.Read(d.b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	last := evs[len(evs)-1]
	attachText(sr, "drvctl-060 supervised draiver log", fmt.Sprintf(
		"session env (from bringUp):\n  %s\n\n$ draiver log %s --type note …   # no export, no --attempt, no --actor\n%s\nlast event: #%d %s actor=%s\n%s\n",
		strings.Join(d60SessionVars(d.envUsed), "\n  "), accTicket, d.stdout, last.Seq, last.Type, last.Actor, last.Body))
	if last.Type != "note" || last.Actor != agentmsg.AgentActor(d60Adapter) {
		return fmt.Errorf("last event is %s by %q, want a note by %q", last.Type, last.Actor, agentmsg.AgentActor(d60Adapter))
	}
	return nil
}

func d60ThenRefused(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	n, err := d.logLen()
	if err != nil {
		return err
	}
	attachText(sr, "drvctl-060 refused human write", fmt.Sprintf(
		"$ DRAIVER_ACTOR=human:dave draiver log %s --type note …   # inside a supervised session\nexit %d\nstderr:\n%s\nlog length before=%d after=%d\n",
		accTicket, d.code, d.stderr, d.before, n))
	if d.code == 0 {
		return errors.New("a human-attributed write inside a session must be refused")
	}
	if !strings.Contains(d.stderr, "refusing to write as \"human:dave\"") || !strings.Contains(d.stderr, "--actor") {
		return fmt.Errorf("refusal should name the human actor and the --actor escape hatch:\n%s", d.stderr)
	}
	if n != d.before {
		return fmt.Errorf("a refused write appended %d event(s)", n-d.before)
	}
	return nil
}

func d60SessionVars(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "DRAIVER_SESSION_") || strings.HasPrefix(e, agentmsg.EnvActor+"=") || strings.HasPrefix(e, agentmsg.EnvAgentAttempt+"=") {
			out = append(out, e)
		}
	}
	return out
}

// --- background work ----------------------------------------------------------

func d60GivenGate(w *World, sr *StepRun) error {
	d, err := d60Seed(w)
	if err != nil {
		return err
	}
	g, err := completion.New(d.b.root, accTicket, accAtt, "agent:acceptance",
		func(context.Context, string) error { d.nudges++; return nil },
		func() error { d.halts++; return nil })
	if err != nil {
		return err
	}
	d.gate = g
	// drv-051's shape: an Agent call with no run_in_background flag, which the
	// harness launched async.
	input, _ := json.Marshal(map[string]string{"description": "draft section 3", "prompt": "…"})
	for _, ev := range []agent.Event{
		{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{ID: "toolu_d60", Name: "Agent", Input: input}},
		{Kind: agent.EventToolResult, Tool: &agent.ToolEvent{ID: "toolu_d60", Result: "Async agent launched successfully. agentId: a1"}},
	} {
		if _, err := d.feed(ev); err != nil {
			return err
		}
	}
	return nil
}

func (d *d60World) feed(ev agent.Event) (completion.Outcome, error) {
	out, err := d.gate.Consider(context.Background(), ev)
	label := string(ev.Kind)
	if ev.Tool != nil {
		label += " " + ev.Tool.Name + " " + ev.Tool.ID
	}
	if ev.Origin != "" {
		label += " origin=" + ev.Origin
	}
	d.trace = append(d.trace, fmt.Sprintf("%-48s → outcome=%d outstanding=%d nudges=%d halts=%d", label, out, d.gate.Outstanding(), d.nudges, d.halts))
	return out, err
}

func d60TurnEnds(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	d.outcome, err = d.feed(agent.Event{Kind: agent.EventTurnEnd, Turn: "success", Result: "Waiting on the drafting agent."})
	return err
}

func d60ThenWaiting(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	evs, err := ticketlog.Read(d.b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	attachText(sr, "drvctl-060 completion gate trace", strings.Join(d.trace, "\n")+"\n")
	if d.outcome != completion.Waiting {
		return fmt.Errorf("outcome = %d, want Waiting (%d)", d.outcome, completion.Waiting)
	}
	if d.nudges != 0 || d.halts != 0 {
		return fmt.Errorf("gate nudged %d / halted %d times while a background task was running", d.nudges, d.halts)
	}
	for _, e := range evs {
		if e.Type == "escalation" {
			return fmt.Errorf("gate escalated while a background task was running: %s", e.Body)
		}
	}
	return nil
}

func d60ThenNotified(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	if _, err := d.feed(agent.Event{Kind: agent.EventTurnEnd, Turn: "success", Origin: "task-notification"}); err != nil {
		return err
	}
	attachText(sr, "drvctl-060 completion gate trace after notice", strings.Join(d.trace, "\n")+"\n")
	if n := d.gate.Outstanding(); n != 0 {
		return fmt.Errorf("outstanding = %d after the completion notice, want 0", n)
	}
	return nil
}

// --- interrupt hold -----------------------------------------------------------

func d60GivenInterrupted(w *World, sr *StepRun) error {
	d, err := d60Seed(w)
	if err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "enable", Actor: "human:dave", Body: "Supervision enabled."},
		{Type: project.InterruptType, Actor: "agent:draiver", Body: "Interrupted by human: the human interrupted the session's in-flight turn."},
	} {
		if _, err := ticketlog.Append(d.b.root, accTicket, accAtt, e); err != nil {
			return err
		}
	}
	d.rec, err = reconcile.New(reconcile.Options{
		Root:  d.b.root,
		Actor: "agent:draiver",
		Adapters: func(string) (func() agent.Adapter, error) {
			d.mu.Lock()
			d.resolves++
			d.mu.Unlock()
			return nil, errors.New("acceptance: admission probe — no real agent")
		},
	})
	if err != nil {
		return err
	}
	w.defer_(d.rec.Close)
	return nil
}

func (d *d60World) admissions() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.resolves
}

func d60Tick(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	for i := 0; i < 5; i++ {
		if err := d.rec.Tick(context.Background()); err != nil {
			return err
		}
	}
	return nil
}

func d60ThenNotResumed(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	a, err := project.LoadAttempt(d.b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	attachText(sr, "drvctl-060 held attempt", fmt.Sprintf(
		"attempt %s/%s: enabled=%v state=%s interrupted=%v\nsupervisor ticks: 5, bring-up attempts: %d\n",
		accTicket, accAtt, a.Enabled, a.State, a.Interrupted(), d.admissions()))
	if !a.Interrupted() {
		return errors.New("fixture attempt is not held by an interrupt")
	}
	if n := d.admissions(); n != 0 {
		return fmt.Errorf("the supervisor tried to bring the interrupted attempt back up %d time(s)", n)
	}
	return nil
}

func d60ThenMessageLifts(w *World, sr *StepRun) error {
	d, err := d60Get(w)
	if err != nil {
		return err
	}
	if _, err := ticketlog.Append(d.b.root, accTicket, accAtt, event.Event{
		Type: "note", Actor: "human:dave", To: agentmsg.ToAgent, Body: "Carry on, but skip the refactor.",
	}); err != nil {
		return err
	}
	if err := d.rec.Tick(context.Background()); err != nil {
		return err
	}
	attachText(sr, "drvctl-060 hold released", fmt.Sprintf("after the human's message: bring-up attempts = %d\n", d.admissions()))
	if d.admissions() == 0 {
		return errors.New("a message to the agent should release the hold and let the supervisor bring it up")
	}
	return nil
}

func d60ThenBoardLabel(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/")
	if err != nil {
		return err
	}
	if !strings.Contains(html, project.InterruptedReason) {
		return fmt.Errorf("board does not show %q for the held attempt", project.InterruptedReason)
	}
	return shoot(w, sr, "/", "drvctl-060-interrupted-by-human", true, "", "")
}
