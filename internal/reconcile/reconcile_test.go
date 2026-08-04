package reconcile_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// --- fake adapter -----------------------------------------------------------

// fakeAdapter is an in-memory agent.Adapter with a driveable stream: a test
// Spawns it (via the reconciler), then pushes tool-call / permission / usage
// events onto its channel to simulate the agent working, and inspects the prompts
// and permission decisions it received. It implements agent.Permissioner so the
// gates can answer it.
type fakeAdapter struct {
	pid int

	mu        sync.Mutex
	events    chan agent.Event
	started   bool
	killed    bool
	resumeID  string
	prompts   []string
	decisions map[string]agent.Decision
}

func (a *fakeAdapter) Spawn(ctx context.Context, spec agent.SessionSpec) (string, error) {
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()
	const id = "sess-live"
	a.emit(agent.Event{Kind: agent.EventSystem, SessionID: id})
	return id, nil
}

func (a *fakeAdapter) Resume(ctx context.Context, sessionID string, spec agent.SessionSpec) error {
	a.mu.Lock()
	a.resumeID = sessionID
	a.started = true
	a.mu.Unlock()
	a.emit(agent.Event{Kind: agent.EventSystem, SessionID: sessionID})
	return nil
}

func (a *fakeAdapter) Prompt(ctx context.Context, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prompts = append(a.prompts, text)
	return nil
}

func (a *fakeAdapter) Interrupt(ctx context.Context) error { return nil }
func (a *fakeAdapter) Stream() <-chan agent.Event          { return a.events }

func (a *fakeAdapter) Kill() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.killed {
		return nil
	}
	a.killed = true
	a.started = false
	close(a.events)
	return nil
}

func (a *fakeAdapter) Decide(ctx context.Context, requestID string, d agent.Decision) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.decisions == nil {
		a.decisions = map[string]agent.Decision{}
	}
	a.decisions[requestID] = d
	return nil
}

func (a *fakeAdapter) PID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		return 0
	}
	return a.pid
}

// emit pushes an event onto the stream, dropping it if the session was killed
// (the channel is closed) so a late-emitting test never panics.
func (a *fakeAdapter) emit(ev agent.Event) {
	a.mu.Lock()
	if a.killed {
		a.mu.Unlock()
		return
	}
	ch := a.events
	a.mu.Unlock()
	ch <- ev
}

func (a *fakeAdapter) prompted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

func (a *fakeAdapter) decision(id string) (agent.Decision, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d, ok := a.decisions[id]
	return d, ok
}

func (a *fakeAdapter) wasKilled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.killed
}

// factory mints one fresh fakeAdapter per Spawn/Resume and records them so a test
// can drive the live one.
type factory struct {
	mu      sync.Mutex
	made    []*fakeAdapter
	nextPID int
}

func (f *factory) new() agent.Adapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPID++
	a := &fakeAdapter{pid: 1000 + f.nextPID, events: make(chan agent.Event, 16)}
	f.made = append(f.made, a)
	return a
}

func (f *factory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

func (f *factory) at(i int) *fakeAdapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.made[i]
}

// adapters is the AdapterFor resolver: every adapter name maps to this factory.
func (f *factory) adapters(name string) (func() agent.Adapter, error) { return f.new, nil }

// --- fake proc --------------------------------------------------------------

// fakeProc drives re-adoption deterministically: the test declares which pids are
// alive, decoupled from the in-memory fake adapters.
type fakeProc struct {
	mu         sync.Mutex
	alive      map[int]bool
	terminated []int
}

func newProc() *fakeProc { return &fakeProc{alive: map[int]bool{}} }

func (p *fakeProc) setAlive(pid int, live bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.alive[pid] = live
}

func (p *fakeProc) Alive(pid int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive[pid]
}

func (p *fakeProc) Terminate(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminated = append(p.terminated, pid)
	p.alive[pid] = false
	return nil
}

// --- fixture ----------------------------------------------------------------

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// world is a data root + a git repo, the two roots the reconciler spans.
type world struct {
	root store.Root
	repo string
}

func newWorld(t *testing.T) world {
	t.Helper()
	return world{root: store.Root{Dir: t.TempDir()}, repo: newRepo(t)}
}

// newTicket creates a ticket with one Running attempt (its genesis "created"
// event derives to Running) and returns the attempt id.
func (w world) newTicket(t *testing.T, ticket string) string {
	t.Helper()
	if err := w.root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(w.root, ticket, attempt.New{Tool: "claude-code", Model: "opus-4.8", Actor: "agent:x"})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	return m.ID
}

// reconciler wires a Reconciler over the world with the given factory and proc.
func (w world) reconciler(t *testing.T, f *factory, p *fakeProc) *reconcile.Reconciler {
	t.Helper()
	wm, err := worktree.NewManager(w.repo)
	if err != nil {
		t.Fatalf("worktree manager: %v", err)
	}
	r, err := reconcile.New(reconcile.Options{
		Root:      w.root,
		Worktrees: wm,
		Adapters:  f.adapters,
		Actor:     "agent:claude-code",
		Proc:      p,
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func logTypes(t *testing.T, root store.Root, ticket, att string) []string {
	t.Helper()
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	return types
}

func hasType(types []string, want string) bool {
	for _, ty := range types {
		if ty == want {
			return true
		}
	}
	return false
}

func bashCall(id, command string) agent.Event {
	input, _ := json.Marshal(struct {
		Command string `json:"command"`
	}{command})
	return agent.Event{
		Kind: agent.EventToolCall,
		Tool: &agent.ToolEvent{ID: id, Name: "Bash", Input: input},
		Raw:  json.RawMessage(`{"t":"` + id + `"}`),
	}
}

func permReq(id, tool string) agent.Event {
	return agent.Event{
		Kind:       agent.EventPermission,
		Permission: &agent.PermissionRequest{ID: id, Tool: tool, Input: json.RawMessage(`{}`)},
		Raw:        json.RawMessage(`{"perm":"` + id + `"}`),
	}
}

// --- tests ------------------------------------------------------------------

// TestTickAdmitsWatchesAndMeters is the headline Tier-0 path: a Running attempt is
// admitted into a worktree with a persisted session, the cold-start brief is
// injected, and the ingest loop promotes the agent's protocol calls into the
// durable log and meters its usage.
func TestTickAdmitsWatchesAndMeters(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// A session came up: session.json has a live id, pid, and an on-disk worktree.
	sess, err := session.Open(w.root, ticket, att)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	id, err := sess.ReadIdentity()
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if id.SessionID == "" || id.PID == 0 {
		t.Fatalf("session not brought up: %+v", id)
	}
	if fi, err := os.Stat(id.Worktree); err != nil || !fi.IsDir() {
		t.Fatalf("worktree %q not on disk: %v", id.Worktree, err)
	}

	// The cold-start brief was injected as the first prompt.
	live := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(live.prompted()) > 0 })
	if p := live.prompted()[0]; !strings.Contains(p, "BRIEF") {
		t.Fatalf("first prompt is not the brief: %q", p)
	}

	// The agent runs `draiver log --type gotcha …`; the watcher promotes it.
	live.emit(bashCall("t1", `draiver log PROJ-1 --type gotcha "worktree isolation confirmed"`))
	waitFor(t, "promoted gotcha", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "gotcha")
	})

	// A usage frame is metered into meter.json (the live context gauge).
	live.emit(agent.Event{Kind: agent.EventUsage, Usage: &agent.Usage{ContextTokens: 4242, CostUSD: 0.12}, Raw: json.RawMessage(`{"u":1}`)})
	waitFor(t, "metered usage", func() bool {
		m, err := sess.ReadMeter()
		return err == nil && m.Usage.ContextTokens == 4242
	})
}

// TestTickIsIdempotent: a second tick with a session already live does not spawn a
// duplicate.
func TestTickIsIdempotent(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.newTicket(t, "PROJ-1")
	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if n := f.count(); n != 1 {
		t.Fatalf("adapters spawned = %d, want 1 (no duplicate admit)", n)
	}
}

// TestRetireOnReview: when an attempt is claimed for review it leaves the desired
// set; the tick reaps the session and cleans the worktree.
func TestRetireOnReview(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	id, _ := sess.ReadIdentity()
	worktreePath := id.Worktree

	// The agent claims review → Review state → no longer desired.
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "done"}); err != nil {
		t.Fatalf("append review: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("retire tick: %v", err)
	}

	if !f.at(0).wasKilled() {
		t.Fatal("session was not reaped on retire")
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree %q should be removed on a terminal retire (err=%v)", worktreePath, err)
	}
}

// TestParkOnNeedsMeKeepsWorktree: an escalation moves the attempt to Needs-me; the
// tick stops the session but keeps the worktree warm for a post-resolution resume.
func TestParkOnNeedsMeKeepsWorktree(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	id, _ := sess.ReadIdentity()
	worktreePath := id.Worktree

	// An open escalation → Needs-me → not desired, but blocked (will resume).
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "escalation", Actor: "agent:x", Body: "which region?"}); err != nil {
		t.Fatalf("append escalation: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("park tick: %v", err)
	}

	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree %q must be kept warm while parked: %v", worktreePath, err)
	}
}

// TestResumeAfterResolution closes the escalate → resolve → resume arc through the
// reconcile diff alone: a resolved escalation flips the attempt back to Running,
// and the next tick re-admits it via Resume on the surviving cattle handle — no
// path-activation needed at Tier 0.
func TestResumeAfterResolution(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	origID, _ := sess.ReadIdentity()

	// Escalate → park.
	esc, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "escalation", Actor: "agent:x", Body: "blocked"})
	if err != nil {
		t.Fatalf("append escalation: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("park tick: %v", err)
	}

	// Human resolves → back to Running.
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "resolution", Actor: "human:dave", Refs: []int{esc.Seq}, Body: "use australiasoutheast"}); err != nil {
		t.Fatalf("append resolution: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("resume tick: %v", err)
	}

	if f.count() != 2 {
		t.Fatalf("adapters made = %d, want 2 (spawn + resume)", f.count())
	}
	resumed := f.at(1)
	if resumed.resumeID != origID.SessionID {
		t.Fatalf("resumed with id %q, want the surviving %q", resumed.resumeID, origID.SessionID)
	}
}

// TestPermissionGateEscalates: a gated tool (Bash under the ReadOnly base) surfaced
// as a permission callback is denied, recorded as an escalation, and halts the
// session — flipping the attempt to Needs-me.
func TestPermissionGateEscalates(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	live := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(live.prompted()) > 0 })

	live.emit(permReq("p1", "Bash"))
	waitFor(t, "escalation recorded", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "escalation")
	})
	waitFor(t, "session halted", live.wasKilled)

	d, ok := live.decision("p1")
	if !ok || d.Allow {
		t.Fatalf("gated tool should be denied, got decision=%+v ok=%v", d, ok)
	}
}

// TestProtocolGateWithholdsThenClears: an Edit callback with no rationale on disk
// is withheld (denied, session stays live); once a justifying decision is logged,
// a re-request clears the protocol gate and falls through to the permission gate.
func TestProtocolGateWithholdsThenClears(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	live := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(live.prompted()) > 0 })

	// Edit with no rationale yet → withheld: denied, but the session stays live and
	// nothing is escalated.
	live.emit(permReq("edit-1", "Edit"))
	waitFor(t, "edit withheld", func() bool {
		d, ok := live.decision("edit-1")
		return ok && !d.Allow
	})
	if live.wasKilled() {
		t.Fatal("a withhold must not halt the session")
	}
	if hasType(logTypes(t, w.root, ticket, att), "escalation") {
		t.Fatal("a withhold must not record an escalation")
	}

	// The agent logs its rationale (promoted from its own `draiver log` call), then
	// retries the edit: the protocol gate clears it, the permission gate takes over
	// and escalates Edit (not in the ReadOnly allow-set), halting the session.
	live.emit(bashCall("log-1", `draiver log PROJ-1 --type decision "rename the helper for clarity"`))
	waitFor(t, "decision promoted", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "decision")
	})
	live.emit(permReq("edit-2", "Edit"))
	waitFor(t, "edit escalated after clearing", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "escalation")
	})
}

// TestReadoptOnRestart is the restart-survival guarantee. A daemon brings a session
// up and detaches (drain, keeping the process alive). A fresh daemon:
//   - re-adopts the still-live process and does NOT spawn a duplicate;
//   - once that process is gone, re-admits the attempt via Resume on the recorded
//     session id, regaining a stream.
func TestReadoptOnRestart(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	proc := newProc()

	// Daemon A: admit, learn the pid, then detach (down) without reaping.
	fa := &factory{}
	a := w.reconciler(t, fa, proc)
	if err := a.Tick(ctx); err != nil {
		t.Fatalf("A admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	id, _ := sess.ReadIdentity()
	origID := id.SessionID
	proc.setAlive(id.PID, true) // the agent survives A's shutdown
	a.Close()                   // detach: ingest stops, process left running

	// Daemon B: the recorded pid is alive → re-adopt, do not duplicate.
	fb := &factory{}
	b := w.reconciler(t, fb, proc)
	if err := b.Adopt(ctx); err != nil {
		t.Fatalf("B adopt: %v", err)
	}
	if err := b.Tick(ctx); err != nil {
		t.Fatalf("B tick: %v", err)
	}
	if n := fb.count(); n != 0 {
		t.Fatalf("B spawned %d adapters; a live session must be re-adopted, not duplicated", n)
	}
	b.Close()

	// Daemon C: the process has since exited → re-admit via Resume, same session id.
	proc.setAlive(id.PID, false)
	fc := &factory{}
	c := w.reconciler(t, fc, proc)
	t.Cleanup(c.Close)
	if err := c.Adopt(ctx); err != nil {
		t.Fatalf("C adopt: %v", err)
	}
	if err := c.Tick(ctx); err != nil {
		t.Fatalf("C tick: %v", err)
	}
	if fc.count() != 1 {
		t.Fatalf("C made %d adapters, want 1 (resume the dead session)", fc.count())
	}
	if got := fc.at(0).resumeID; got != origID {
		t.Fatalf("C resumed id %q, want the recorded %q", got, origID)
	}
}

// TestReadoptRetiresAdoptedWhenUndesired: an adopted (foreign) live session whose
// attempt is no longer desired is terminated by pid — the only handle the daemon
// has on a process it did not spawn.
func TestReadoptRetiresAdoptedWhenUndesired(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	proc := newProc()

	fa := &factory{}
	a := w.reconciler(t, fa, proc)
	if err := a.Tick(ctx); err != nil {
		t.Fatalf("A admit: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	id, _ := sess.ReadIdentity()
	proc.setAlive(id.PID, true)
	a.Close()

	// The attempt is claimed for review while the daemon is down.
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "done"}); err != nil {
		t.Fatalf("append review: %v", err)
	}

	fb := &factory{}
	b := w.reconciler(t, fb, proc)
	t.Cleanup(b.Close)
	if err := b.Adopt(ctx); err != nil {
		t.Fatalf("B adopt: %v", err)
	}
	if err := b.Tick(ctx); err != nil {
		t.Fatalf("B tick: %v", err)
	}

	proc.mu.Lock()
	terminated := append([]int(nil), proc.terminated...)
	proc.mu.Unlock()
	if len(terminated) != 1 || terminated[0] != id.PID {
		t.Fatalf("adopted session pid %d should be terminated on retire, got %v", id.PID, terminated)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	w := newWorld(t)
	wm, err := worktree.NewManager(w.repo)
	if err != nil {
		t.Fatal(err)
	}
	f := &factory{}
	good := reconcile.Options{Root: w.root, Worktrees: wm, Adapters: f.adapters, Actor: "agent:x"}
	if _, err := reconcile.New(good); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	cases := map[string]reconcile.Options{
		"no root":     {Worktrees: wm, Adapters: f.adapters, Actor: "a"},
		"no worktree": {Root: w.root, Adapters: f.adapters, Actor: "a"},
		"no adapters": {Root: w.root, Worktrees: wm, Actor: "a"},
		"no actor":    {Root: w.root, Worktrees: wm, Adapters: f.adapters},
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := reconcile.New(opt); err == nil {
				t.Fatal("New should reject invalid options")
			}
		})
	}
}
