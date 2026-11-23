package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// steps_pushevents_drvctl062.go realises drvctl-062
// (features/agent-messages/push-human-events.feature):
//
//   - push: a REAL reconcile.Reconciler admits an enabled attempt on a real git
//     repo, driving a scripted in-memory adapter (d62Adapter) that records every
//     user turn it receives and every tool call it makes, in order. The human's
//     events are appended to the real log; the supervisor's tick does the rest.
//   - review/ack and the resume note reuse the drvctl-059 fixture (amWorld): the
//     REAL draiver binary shelled with the session env the daemon composes, and the
//     real board compose handler.

const d62Ticket = "PUSH-1"

func init() {
	for text, fn := range map[string]StepFunc{
		"a drvctl-062 session is live under a real supervisor":                   d62GivenLive,
		"the human logged a decision that was pushed to the session":             d62GivenPushedDecision,
		"the human supersedes that decision mid-session":                         d62Supersede,
		"the drvctl-062 supervisor ticks":                                        d62Tick,
		"the session received the supersede as a turn before its next tool call": d62ThenSupersedeTurn,
		"the agent logs a decision and the spine logs a note mid-session":        d62AgentAndSpine,
		"the session received no pushed turn":                                    d62ThenNoPush,
		"the human logs a decision on the board without messaging the agent":     d62BoardDecision,
		"the agent's write names the unread decision by seq and kind":            d62ThenWriteNames,
		"the agent claims review with --ack":                                     d62ClaimAck,
		"the acknowledged decision is printed in full":                           d62ThenAckPrinted,
	} {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

// d62Adapter is a scripted agent.Adapter: Spawn comes online at once, Prompt
// records the turn, and toolCall records the session's next tool call — one
// ordered trace, so "the turn arrived before the next tool call" is checkable.
type d62Adapter struct {
	mu      sync.Mutex
	events  chan agent.Event
	online  chan struct{}
	killed  bool
	prompts []string
	trace   []string
}

func (a *d62Adapter) Spawn(ctx context.Context, spec agent.SessionSpec) (string, error) {
	close(a.online)
	a.events <- agent.Event{Kind: agent.EventSystem, SessionID: "d62-session"}
	return "d62-session", nil
}

func (a *d62Adapter) Resume(ctx context.Context, id string, spec agent.SessionSpec) error {
	return errors.New("d62: resume not scripted")
}

func (a *d62Adapter) Prompt(ctx context.Context, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prompts = append(a.prompts, text)
	first := strings.SplitN(text, "\n", 2)[0]
	a.trace = append(a.trace, fmt.Sprintf("turn %d ← %s", len(a.prompts), first))
	return nil
}

func (a *d62Adapter) Interrupt(ctx context.Context) error { return nil }
func (a *d62Adapter) Stream() <-chan agent.Event          { return a.events }
func (a *d62Adapter) Online() <-chan struct{}             { return a.online }

func (a *d62Adapter) Kill() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.killed {
		a.killed = true
		close(a.events)
	}
	return nil
}

// toolCall records the session's next tool call in the trace.
func (a *d62Adapter) toolCall(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trace = append(a.trace, "tool call → "+name)
}

func (a *d62Adapter) snapshot() (prompts, trace []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...), append([]string(nil), a.trace...)
}

type d62World struct {
	root    store.Root
	att     string
	rec     *reconcile.Reconciler
	adapter *d62Adapter
	base    int // prompts the session had before the step under test
	dec     int // the human decision's seq
	sup     int // the superseding event's seq
}

func d62Get(w *World) (*d62World, error) {
	d, _ := w.get("d62").(*d62World)
	if d == nil {
		return nil, errors.New("no drvctl-062 supervisor fixture in scope")
	}
	return d, nil
}

func d62GivenLive(w *World, sr *StepRun) error {
	base, err := os.MkdirTemp("", "acc-drvctl062-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(base) })
	// Worktrees are cut under XDG_CACHE_HOME; keep them inside the scenario's temp dir.
	prevCache, hadCache := os.LookupEnv("XDG_CACHE_HOME")
	os.Setenv("XDG_CACHE_HOME", base+"/cache")
	w.defer_(func() {
		if hadCache {
			os.Setenv("XDG_CACHE_HOME", prevCache)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
	})
	repo := base + "/repo"
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	if err := gitc(repo, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if err := gitc(repo, "commit", "-q", "--allow-empty", "-m", "init"); err != nil {
		return err
	}
	d := &d62World{root: store.Root{Dir: base + "/data"}}
	if err := d.root.EnsureTicketDir(d62Ticket); err != nil {
		return err
	}
	m, err := attempt.Create(d.root, d62Ticket, attempt.New{Tool: "claude-code", Repo: repo, Actor: "human:dave"})
	if err != nil {
		return err
	}
	d.att = m.ID
	if _, err := ticketlog.Append(d.root, d62Ticket, d.att, event.Event{Type: "enable", Actor: "human:dave", Body: "Supervision enabled."}); err != nil {
		return err
	}
	d.adapter = &d62Adapter{events: make(chan agent.Event, 16), online: make(chan struct{})}
	d.rec, err = reconcile.New(reconcile.Options{
		Root:     d.root,
		Actor:    "human:dave",
		Adapters: func(string) (func() agent.Adapter, error) { return func() agent.Adapter { return d.adapter }, nil },
	})
	if err != nil {
		return err
	}
	w.defer_(d.rec.Close)
	w.set("d62", d)
	if err := d.rec.Tick(context.Background()); err != nil {
		return err
	}
	// The cold-start brief is the session's first turn.
	if err := d62Wait(func() bool { p, _ := d.adapter.snapshot(); return len(p) == 1 }); err != nil {
		return fmt.Errorf("session never received its cold-start brief: %w", err)
	}
	d.adapter.toolCall("Read README.md")
	d.base = 1
	return nil
}

func d62Wait(cond func() bool) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("timed out")
}

func (d *d62World) append(e event.Event) (int, error) {
	got, err := ticketlog.Append(d.root, d62Ticket, d.att, e)
	return got.Seq, err
}

func d62GivenPushedDecision(w *World, sr *StepRun) error {
	d, err := d62Get(w)
	if err != nil {
		return err
	}
	if d.dec, err = d.append(event.Event{Type: "decision", Actor: "human:dave", Body: "Add a CSV export button to the report page."}); err != nil {
		return err
	}
	if err := d.rec.Tick(context.Background()); err != nil {
		return err
	}
	p, _ := d.adapter.snapshot()
	if len(p) != d.base+1 || !strings.Contains(p[len(p)-1], "CSV export") {
		return fmt.Errorf("the human decision was not pushed as a turn: %d turns", len(p))
	}
	// The session acts on it.
	d.adapter.toolCall("Edit report.go")
	d.base = len(p)
	return nil
}

func d62Supersede(w *World, sr *StepRun) error {
	d, err := d62Get(w)
	if err != nil {
		return err
	}
	d.sup, err = d.append(event.Event{
		Type: "note", Actor: "human:dave", Supersedes: []int{d.dec},
		Body: "Ignore #" + strconv.Itoa(d.dec) + " — that feedback was meant for another ticket.",
	})
	return err
}

func d62Tick(w *World, sr *StepRun) error {
	d, err := d62Get(w)
	if err != nil {
		return err
	}
	if err := d.rec.Tick(context.Background()); err != nil {
		return err
	}
	// The session's next step: another tool call, after whatever the tick delivered.
	d.adapter.toolCall("Edit report.go")
	return nil
}

func d62ThenSupersedeTurn(w *World, sr *StepRun) error {
	d, err := d62Get(w)
	if err != nil {
		return err
	}
	prompts, trace := d.adapter.snapshot()
	var b strings.Builder
	b.WriteString("session trace (turns received, tool calls made), in order:\n")
	for _, t := range trace {
		b.WriteString("  " + t + "\n")
	}
	if len(prompts) > d.base {
		b.WriteString("\n--- the pushed turn ---\n" + prompts[len(prompts)-1] + "\n")
	}
	attachText(sr, "drvctl-062 pushed supersede", b.String())

	if len(prompts) != d.base+1 {
		return fmt.Errorf("want exactly one new turn after the supersede, got %d", len(prompts)-d.base)
	}
	turn := prompts[len(prompts)-1]
	for _, want := range []string{
		fmt.Sprintf("#%d supersede (retires #%d)", d.sup, d.dec),
		"meant for another ticket",
		"draiver messages",
	} {
		if !strings.Contains(turn, want) {
			return fmt.Errorf("pushed turn missing %q:\n%s", want, turn)
		}
	}
	if strings.Contains(turn, "CSV export") {
		return fmt.Errorf("the already-pushed decision must not be pushed again:\n%s", turn)
	}
	// Ordering: the supersede turn precedes the session's next tool call.
	n := len(trace)
	if n < 2 || !strings.HasPrefix(trace[n-2], "turn ") || !strings.HasPrefix(trace[n-1], "tool call") {
		return fmt.Errorf("the supersede turn must arrive before the next tool call; trace: %v", trace)
	}
	return nil
}

func d62AgentAndSpine(w *World, sr *StepRun) error {
	d, err := d62Get(w)
	if err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "decision", Actor: "agent:claude-code", Body: "Chose server-side pagination."},
		{Type: "gotcha", Actor: "agent:claude-code", Body: "Fixture keys only work in test mode."},
		{Type: "note", Actor: "human:dave", Body: "Branched a new attempt from this one (start --new-attempt)."},
	} {
		if _, err := d.append(e); err != nil {
			return err
		}
	}
	return nil
}

func d62ThenNoPush(w *World, sr *StepRun) error {
	d, err := d62Get(w)
	if err != nil {
		return err
	}
	prompts, trace := d.adapter.snapshot()
	attachText(sr, "drvctl-062 no echo", "session trace:\n  "+strings.Join(trace, "\n  ")+"\n")
	if len(prompts) != d.base {
		return fmt.Errorf("agent-authored events and spine notes must not be pushed; got %d extra turn(s):\n%s", len(prompts)-d.base, strings.Join(prompts[d.base:], "\n---\n"))
	}
	return nil
}

// --- review / ack (drvctl-059 fixture) ----------------------------------------

const d62DecisionBody = "Use SQLite for the cache, not Redis — " + amSecret

func d62BoardDecision(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	return a.postBoard("decision", d62DecisionBody, false)
}

func d62ThenWriteNames(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	attachText(sr, "drvctl-062 write warning", "--- stdout\n"+a.stdout+"--- stderr\n"+a.stderr)
	want := fmt.Sprintf("1 new human event since your last read: #%d decision", a.posted[len(a.posted)-1])
	if !strings.Contains(a.stderr, want) {
		return fmt.Errorf("stderr missing %q:\n%s", want, a.stderr)
	}
	if strings.Contains(a.stderr, amSecret) {
		return fmt.Errorf("the write warning must not leak content:\n%s", a.stderr)
	}
	if a.code != 0 || !strings.HasPrefix(a.stdout, "logged note #") {
		return fmt.Errorf("draiver log behaviour changed: exit %d\n%s", a.code, a.stdout)
	}
	return nil
}

func d62ClaimAck(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if a.before, err = a.logLen(); err != nil {
		return err
	}
	args := []string{"review", amTicket, "Claiming review.", "--ack"}
	if err := a.agent(args...); err != nil {
		return err
	}
	attachText(sr, "review --ack", fmt.Sprintf("$ draiver %s\n[exit %d]\n--- stdout\n%s--- stderr\n%s", strings.Join(args, " "), a.code, a.stdout, a.stderr))
	return nil
}

func d62ThenAckPrinted(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if !strings.Contains(a.stdout, d62DecisionBody) || !strings.Contains(a.stdout, "Acknowledged 1 human event") {
		return fmt.Errorf("review --ack must print the decision in full and acknowledge it:\n%s", a.stdout)
	}
	attachText(sr, "acknowledged", a.stdout)
	return nil
}
