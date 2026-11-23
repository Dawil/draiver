package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/handoff"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// steps_handoff_drvctl061.go realises drvctl-061 (features/session-lifecycle/
// handoff-before-reap.feature): checkpoint-before-reap and the automatic restart.
//
// A REAL reconcile.Reconciler ticks over the board's attempt (its repo made a real
// git repo, so bringUp cuts a real worktree). Its adapter resolver mints d61Session
// fakes: each emits system/init on spawn and, when prompted with the supervisor's
// handoff ask, writes its note by shelling the REAL `draiver log` with exactly the
// env bringUp handed the session — so attribution is the real CLI's, not the test's.

const (
	d61Limit     = 150_000
	d61NoteFirst = "Handoff: parser half done; next wire the CLI flag"
)

func init() {
	for text, fn := range map[string]StepFunc{
		"a drvctl-061 supervised session with a context limit and one auto-restart": d61GivenAutoRestart,
		"a drvctl-061 supervised session with the handoff disabled":                 d61GivenDisabled,
		"a drvctl-061 supervised session owned by a running ctl up":                 d61GivenOwned,

		"the drvctl-061 session's context crosses the limit":                 d61CrossFirst,
		"the restarted drvctl-061 session's context crosses the limit again": d61CrossSecond,
		"a human runs ctl stop on the drvctl-061 attempt":                    d61CtlStop,
		"a human runs ctl stop --now on the drvctl-061 attempt":              d61CtlStopNow,

		"the supervisor asks the session for a handoff note before reaping it":                 d61ThenAsked,
		"the handoff note lands on the log attributed to agent:claude-code":                    d61ThenNoteAttributed,
		"the supervisor resolves the auto-stop and restarts the attempt on a fresh session":    d61ThenRestarted,
		"the fresh session's brief carries the handoff note":                                   d61ThenBriefCarries,
		"the second auto-stop is left open for a human and nothing restarts":                   d61ThenLeftForHuman,
		"the board shows the drvctl-061 attempt waiting on a human":                            d61ThenBoard,
		"the session is reaped at once without a handoff prompt and the escalation stays open": d61ThenImmediate,
		"the stop reports a handoff note written before the reap":                              d61ThenStopHandoff,
		"the stopped drvctl-061 attempt is not brought back up":                                d61ThenStaysStopped,
		"the session is reaped without a handoff prompt":                                       d61ThenNoPrompt,
	} {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

// --- fake session -------------------------------------------------------------

type d61Session struct {
	d   *d61World
	pid int

	mu          sync.Mutex
	events      chan agent.Event
	online      chan struct{}
	onlineOnce  sync.Once
	killed      bool
	resumed     string
	env         []string
	prompts     []string
	interrupted bool
}

func (s *d61Session) Spawn(ctx context.Context, spec agent.SessionSpec) (string, error) {
	s.mu.Lock()
	s.env = append([]string(nil), spec.Env...)
	s.mu.Unlock()
	id := fmt.Sprintf("sess-%d", s.pid)
	s.emit(agent.Event{Kind: agent.EventSystem, SessionID: id})
	return id, nil
}

func (s *d61Session) Resume(ctx context.Context, id string, spec agent.SessionSpec) error {
	s.mu.Lock()
	s.resumed = id
	s.env = append([]string(nil), spec.Env...)
	s.mu.Unlock()
	s.emit(agent.Event{Kind: agent.EventSystem, SessionID: id})
	return nil
}

func (s *d61Session) Online() <-chan struct{}    { return s.online }
func (s *d61Session) Stream() <-chan agent.Event { return s.events }
func (s *d61Session) Decide(context.Context, string, agent.Decision) error {
	return nil
}

func (s *d61Session) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.killed {
		return 0
	}
	return s.pid
}

func (s *d61Session) Interrupt(context.Context) error {
	s.mu.Lock()
	s.interrupted = true
	s.mu.Unlock()
	s.d.note(fmt.Sprintf("session pid %d: interrupted", s.pid))
	return nil
}

// Prompt answers the supervisor's handoff ask the way an agent would: by running
// `draiver log … --tag handoff` with its session env.
func (s *d61Session) Prompt(ctx context.Context, text string) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, text)
	env := append([]string(nil), s.env...)
	s.mu.Unlock()
	if strings.Contains(text, "hand off now") {
		s.d.note(fmt.Sprintf("session pid %d: received the handoff prompt", s.pid))
		go s.d.runHandoffLog(s, env)
	}
	return nil
}

func (s *d61Session) Kill() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.killed {
		s.killed = true
		close(s.events)
		s.d.note(fmt.Sprintf("session pid %d: reaped", s.pid))
	}
	return nil
}

func (s *d61Session) emit(ev agent.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.killed {
		return
	}
	if ev.Kind == agent.EventSystem {
		s.onlineOnce.Do(func() { close(s.online) })
	}
	select {
	case s.events <- ev:
	default:
	}
}

func (s *d61Session) wasKilled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killed
}

func (s *d61Session) handoffPrompted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.prompts, func(p string) bool { return strings.Contains(p, "hand off now") })
}

func (s *d61Session) firstPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.prompts) == 0 {
		return ""
	}
	return s.prompts[0]
}

// d61Proc is the reconciler's process prober over the fake sessions: a terminate
// reaps the session with that pid (the process dying closes its stream).
type d61Proc struct {
	d     *d61World
	mu    sync.Mutex
	alive map[int]bool
}

func (p *d61Proc) Alive(pid int) bool {
	p.mu.Lock()
	if p.alive[pid] {
		p.mu.Unlock()
		return true
	}
	p.mu.Unlock()
	for _, s := range p.d.all() {
		if s.pid == pid {
			return !s.wasKilled()
		}
	}
	return false
}

func (p *d61Proc) Terminate(pid int) error {
	for _, s := range p.d.all() {
		if s.pid == pid {
			return s.Kill()
		}
	}
	return nil
}

// --- world --------------------------------------------------------------------

type d61World struct {
	b      *board
	proc   *d61Proc
	rec    *reconcile.Reconciler
	client *reconcile.Reconciler

	mu       sync.Mutex
	sessions []*d61Session
	trace    []string
	notes    int // handoff notes the fake sessions have written
	logOut   []string

	stopRes reconcile.StopResult
	stopErr error
}

func d61Get(w *World) (*d61World, error) {
	d, _ := w.get("d61").(*d61World)
	if d == nil {
		return nil, errors.New("no drvctl-061 fixture in scope")
	}
	return d, nil
}

func (d *d61World) note(s string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trace = append(d.trace, s)
}

func (d *d61World) traceText() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.trace, "\n") + "\n"
}

func (d *d61World) all() []*d61Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*d61Session(nil), d.sessions...)
}

func (d *d61World) session(i int) (*d61Session, error) {
	if err := d61Wait(fmt.Sprintf("session #%d to be admitted", i+1), func() bool {
		_ = d.rec.Tick(context.Background())
		return len(d.all()) > i
	}); err != nil {
		return nil, err
	}
	return d.all()[i], nil
}

func (d *d61World) mint() agent.Adapter {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := &d61Session{d: d, pid: 61001 + len(d.sessions), events: make(chan agent.Event, 64), online: make(chan struct{})}
	d.sessions = append(d.sessions, s)
	d.trace = append(d.trace, fmt.Sprintf("supervisor: admitted session pid %d", s.pid))
	return s
}

// runHandoffLog shells the real draiver binary with the session's env.
func (d *d61World) runHandoffLog(s *d61Session, sessEnv []string) {
	bin, err := overlayBin()
	if err != nil {
		d.note("draiver log: " + err.Error())
		return
	}
	d.mu.Lock()
	d.notes++
	body := d61NoteFirst
	if d.notes > 1 {
		body = fmt.Sprintf("Handoff: CLI wired (note %d); only the tests are left", d.notes)
	}
	d.mu.Unlock()
	drop := map[string]bool{"DRAIVER_DATA": true, "DRAIVER_CONFIG": true, "DRAIVER_ACTOR": true, "DRAIVER_ATTEMPT": true}
	var env []string
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && (drop[e[:i]] || strings.HasPrefix(e[:i], "DRAIVER_SESSION")) {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "DRAIVER_DATA="+d.b.root.Dir)
	env = append(env, sessEnv...)
	full := body + "\n\n## Done\n- parser\n\n## In progress\n- CLI flag — next: wire `--now`\n\n## Uncommitted files\n- none\n\n## Gotchas\n- the full test climb takes minutes\n\n## Don't redo\n- the parser"
	cmd := exec.Command(bin, "log", accTicket, "--type", "note", "--tag", handoff.Tag, full)
	cmd.Dir = d.b.wd
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	d.mu.Lock()
	d.logOut = append(d.logOut, fmt.Sprintf("$ draiver log %s --type note --tag handoff …  (session pid %d)\n%s(err=%v)", accTicket, s.pid, out.String(), runErr))
	d.trace = append(d.trace, fmt.Sprintf("session pid %d: ran draiver log --tag handoff (err=%v)", s.pid, runErr))
	d.mu.Unlock()
}

func d61Wait(what string, cond func() bool) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

func (d *d61World) events() ([]event.Event, error) {
	return ticketlog.Read(d.b.root, accTicket, accAtt)
}

func (d *d61World) count(typ, tag string) int {
	evs, _ := d.events()
	n := 0
	for _, e := range evs {
		if e.Type == typ && (tag == "" || slices.Contains(e.Tags, tag)) {
			n++
		}
	}
	return n
}

func (d *d61World) logText() string {
	evs, _ := d.events()
	var b strings.Builder
	for _, e := range evs {
		head, _, _ := strings.Cut(e.Body, "\n")
		fmt.Fprintf(&b, "#%d %-10s %-18s tags=%v  %s\n", e.Seq, e.Type, e.Actor, e.Tags, head)
	}
	return b.String()
}

// d61Seed builds the board, makes its worktree a real git repo, enables the
// attempt, and wires the reconciler.
func d61Seed(w *World, grace time.Duration) (*d61World, error) {
	// Build the real draiver binary up front: its one-time build can outlast the
	// handoff grace period if the fake session triggers it mid-checkpoint.
	if _, err := overlayBin(); err != nil {
		return nil, err
	}
	b, err := seedBoard(w, "levels: []\n")
	if err != nil {
		return nil, err
	}
	w.set("board", b)
	if err := gitc(b.wd, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	if err := gitc(b.wd, "add", "-A"); err != nil {
		return nil, err
	}
	if err := gitc(b.wd, "commit", "-q", "-m", "init"); err != nil {
		return nil, err
	}
	cache, err := os.MkdirTemp("", "acc-d61-cache-")
	if err != nil {
		return nil, err
	}
	prev, had := os.LookupEnv("XDG_CACHE_HOME")
	os.Setenv("XDG_CACHE_HOME", cache)
	w.defer_(func() {
		if had {
			os.Setenv("XDG_CACHE_HOME", prev)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
		os.RemoveAll(cache)
	})
	if _, err := ticketlog.Append(b.root, accTicket, accAtt, event.Event{Type: "enable", Actor: "human:dave", Body: "Supervision enabled."}); err != nil {
		return nil, err
	}

	d := &d61World{b: b}
	d.proc = &d61Proc{d: d, alive: map[int]bool{}}
	mk := func() (*reconcile.Reconciler, error) {
		return reconcile.New(reconcile.Options{
			Root:                b.root,
			Actor:               "agent:draiver",
			Adapters:            func(string) (func() agent.Adapter, error) { return d.mint, nil },
			ContextLimit:        d61Limit,
			ContextWindow:       200_000,
			StopGrace:           grace,
			ContextAutoRestarts: 1,
			HandoffPoll:         10 * time.Millisecond,
			Proc:                d.proc,
			Logf:                func(f string, a ...any) { d.note("supervisor log: " + fmt.Sprintf(f, a...)) },
		})
	}
	if d.rec, err = mk(); err != nil {
		return nil, err
	}
	w.defer_(d.rec.Close)
	if d.client, err = mk(); err != nil {
		return nil, err
	}
	w.defer_(d.client.Close)
	w.set("d61", d)

	s, err := d.session(0)
	if err != nil {
		return nil, err
	}
	if err := d61Wait("the cold-start brief", func() bool { return s.firstPrompt() != "" }); err != nil {
		return nil, err
	}
	return d, nil
}

// --- given --------------------------------------------------------------------

func d61GivenAutoRestart(w *World, sr *StepRun) error {
	_, err := d61Seed(w, 20*time.Second)
	return err
}

func d61GivenDisabled(w *World, sr *StepRun) error {
	_, err := d61Seed(w, 0)
	return err
}

func d61GivenOwned(w *World, sr *StepRun) error {
	d, err := d61Seed(w, 20*time.Second)
	if err != nil {
		return err
	}
	ctrl := reconcile.Controller{PID: 61999, Nonce: "d61-boot", Started: time.Now()}
	d.proc.mu.Lock()
	d.proc.alive[ctrl.PID] = true
	d.proc.mu.Unlock()
	return reconcile.WriteController(d.b.root, ctrl)
}

// --- when ---------------------------------------------------------------------

func d61Cross(d *d61World, s *d61Session, tokens int) {
	// A mid-turn tool call, then a usage frame over the limit.
	s.emit(agent.Event{Kind: agent.EventAssistant, Text: "Reading the parser…"})
	s.emit(agent.Event{Kind: agent.EventUsage, Usage: &agent.Usage{ContextTokens: tokens}})
	d.note(fmt.Sprintf("session pid %d: context fill %d crosses context_limit %d", s.pid, tokens, d61Limit))
}

func d61CrossFirst(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	s, err := d.session(0)
	if err != nil {
		return err
	}
	d61Cross(d, s, 151_200)
	return nil
}

func d61CrossSecond(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	s, err := d.session(1)
	if err != nil {
		return err
	}
	if err := d61Wait("the restarted session's brief", func() bool { return s.firstPrompt() != "" }); err != nil {
		return err
	}
	d61Cross(d, s, 152_400)
	return d61Wait("the second session to be reaped", s.wasKilled)
}

func (d *d61World) ctlStop(now bool) {
	type res struct {
		r   reconcile.StopResult
		err error
	}
	done := make(chan res, 1)
	go func() {
		r, err := d.client.StopWithHandoff(accTicket, accAtt, now)
		done <- res{r, err}
	}()
	for {
		select {
		case got := <-done:
			d.stopRes, d.stopErr = got.r, got.err
			return
		case <-time.After(20 * time.Millisecond):
			_ = d.rec.Tick(context.Background()) // the owning daemon keeps ticking
		}
	}
}

func d61CtlStop(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	d.note("human: draiver ctl stop " + accTicket)
	d.ctlStop(false)
	return nil
}

func d61CtlStopNow(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	d.note("human: draiver ctl stop --now " + accTicket)
	d.ctlStop(true)
	return nil
}

// --- then ---------------------------------------------------------------------

func d61ThenAsked(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	s := d.all()[0]
	if err := d61Wait("the handoff prompt", s.handoffPrompted); err != nil {
		return err
	}
	if err := d61Wait("the reap", s.wasKilled); err != nil {
		return err
	}
	attachText(sr, "drvctl-061 auto-stop checkpoint trace", d.traceText()+"\n--- handoff prompt ---\n"+handoff.PromptText(accTicket, "its context window reached the configured limit", 20*time.Second)+"\n")
	tr := d.traceText()
	ask := strings.Index(tr, "received the handoff prompt")
	reap := strings.Index(tr, fmt.Sprintf("session pid %d: reaped", s.pid))
	if ask < 0 || reap < 0 || ask > reap {
		return fmt.Errorf("the handoff prompt must come before the reap:\n%s", tr)
	}
	if !s.interrupted {
		return errors.New("the in-flight turn was not interrupted before the handoff prompt")
	}
	if d.count("escalation", limit.TagContextAutoStop) != 1 {
		return errors.New("the auto-stop escalation is not durable")
	}
	return nil
}

func d61ThenNoteAttributed(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	evs, err := d.events()
	if err != nil {
		return err
	}
	d.mu.Lock()
	out := strings.Join(d.logOut, "\n")
	d.mu.Unlock()
	attachText(sr, "drvctl-061 handoff note on the log", out+"\n\n--- attempt log ---\n"+d.logText())
	escSeq := 0
	for _, e := range evs {
		if e.Type == "escalation" && slices.Contains(e.Tags, limit.TagContextAutoStop) && escSeq == 0 {
			escSeq = e.Seq
		}
		if e.Type == "note" && slices.Contains(e.Tags, handoff.Tag) {
			if e.Actor != "agent:claude-code" {
				return fmt.Errorf("handoff note #%d attributed to %q, want agent:claude-code", e.Seq, e.Actor)
			}
			if e.Seq < escSeq || escSeq == 0 {
				return fmt.Errorf("handoff note #%d landed before the escalation #%d", e.Seq, escSeq)
			}
			return nil
		}
	}
	return errors.New("no handoff note on the log")
}

func d61ThenRestarted(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	if err := d61Wait("the supervisor's auto-restart resolution", func() bool {
		return d.count("resolution", limit.TagContextAutoRestart) == 1
	}); err != nil {
		return err
	}
	s2, err := d.session(1)
	if err != nil {
		return err
	}
	attachText(sr, "drvctl-061 restart", d.traceText()+"\n--- attempt log ---\n"+d.logText())
	if s2.resumed != "" {
		return fmt.Errorf("the restart resumed the old conversation %q instead of a fresh session", s2.resumed)
	}
	a, err := project.LoadAttempt(d.b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	if a.State != project.Running {
		return fmt.Errorf("attempt state = %s, want Running after the auto-restart", a.State)
	}
	return nil
}

func d61ThenBriefCarries(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	s2, err := d.session(1)
	if err != nil {
		return err
	}
	if err := d61Wait("the fresh brief", func() bool { return s2.firstPrompt() != "" }); err != nil {
		return err
	}
	brief := s2.firstPrompt()
	attachText(sr, "drvctl-061 fresh session brief", brief)
	if !strings.Contains(brief, "parser half done") {
		return errors.New("the fresh session's brief does not carry the handoff note")
	}
	return nil
}

func d61ThenLeftForHuman(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	for i := 0; i < 5; i++ {
		_ = d.rec.Tick(context.Background())
		time.Sleep(20 * time.Millisecond)
	}
	attachText(sr, "drvctl-061 second trip", d.traceText()+"\n--- attempt log ---\n"+d.logText())
	a, err := project.LoadAttempt(d.b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	if a.State != project.NeedsMe {
		return fmt.Errorf("attempt state = %s, want NeedsMe after the second trip", a.State)
	}
	if n := d.count("resolution", limit.TagContextAutoRestart); n != 1 {
		return fmt.Errorf("auto-restart resolutions = %d, want 1 (budget spent)", n)
	}
	if n := len(d.all()); n != 2 {
		return fmt.Errorf("sessions admitted = %d, want 2 (no third)", n)
	}
	if d.count("note", handoff.Tag) != 2 {
		return errors.New("the second session should still have handed off before the reap")
	}
	return nil
}

func d61ThenBoard(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/")
	if err != nil {
		return err
	}
	if !strings.Contains(html, accTicket) {
		return errors.New("the board does not list the attempt")
	}
	return shoot(w, sr, "/", "drvctl-061-second-trip-needs-me", true, "", "")
}

func d61ThenImmediate(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	s := d.all()[0]
	if err := d61Wait("the immediate reap", s.wasKilled); err != nil {
		return err
	}
	attachText(sr, "drvctl-061 stop_grace_seconds 0", d.traceText()+"\n--- attempt log ---\n"+d.logText())
	if s.handoffPrompted() {
		return errors.New("stop_grace_seconds 0 must not prompt for a handoff")
	}
	evs, _ := d.events()
	if st, open := project.Derive(evs); st != project.NeedsMe || len(open) != 1 {
		return fmt.Errorf("state = %s with %d open, want NeedsMe with the escalation open", st, len(open))
	}
	for _, e := range evs {
		if e.Type == "escalation" && strings.Contains(e.Body, "handoff") {
			return errors.New("the escalation body should be today's, with no handoff paragraph")
		}
	}
	return nil
}

func d61ThenStopHandoff(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	attachText(sr, "drvctl-061 ctl stop via ctl up", fmt.Sprintf("StopResult: %+v (err=%v)\n\n%s\n--- attempt log ---\n%s",
		d.stopRes, d.stopErr, d.traceText(), d.logText()))
	if d.stopErr != nil {
		return d.stopErr
	}
	r := d.stopRes
	if !r.Handoff || r.Direct || r.Outcome != string(handoff.Written) || r.NoteSeq == 0 {
		return fmt.Errorf("stop result = %+v, want a written handoff through ctl up", r)
	}
	if !d.all()[0].wasKilled() {
		return errors.New("the session was not reaped")
	}
	return nil
}

func d61ThenStaysStopped(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	for i := 0; i < 5; i++ {
		_ = d.rec.Tick(context.Background())
		time.Sleep(20 * time.Millisecond)
	}
	a, err := project.LoadAttempt(d.b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	attachText(sr, "drvctl-061 stopped attempt", fmt.Sprintf("after 5 ticks: sessions admitted=%d, interrupted=%v, state=%s\n",
		len(d.all()), a.Interrupted(), a.State))
	if n := len(d.all()); n != 1 {
		return fmt.Errorf("sessions admitted = %d; a stopped attempt must stay stopped", n)
	}
	return nil
}

func d61ThenNoPrompt(w *World, sr *StepRun) error {
	d, err := d61Get(w)
	if err != nil {
		return err
	}
	s := d.all()[0]
	attachText(sr, "drvctl-061 ctl stop --now", fmt.Sprintf("StopResult: %+v (err=%v)\n\n%s", d.stopRes, d.stopErr, d.traceText()))
	if d.stopErr != nil {
		return d.stopErr
	}
	if err := d61Wait("the reap", s.wasKilled); err != nil {
		return err
	}
	if s.handoffPrompted() || d.stopRes.Handoff {
		return errors.New("--now must skip the handoff")
	}
	return nil
}
