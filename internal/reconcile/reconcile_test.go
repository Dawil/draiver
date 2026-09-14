package reconcile_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
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

	// staleResume simulates a session id that can no longer be resumed: Resume
	// starts (returns nil) but the process never emits a system/init frame, so it
	// never comes online — the case the confirm-on-resume cascade must self-heal.
	staleResume bool

	mu         sync.Mutex
	events     chan agent.Event
	online     chan struct{}
	onlineOnce sync.Once
	started    bool
	killed     bool
	resumeID   string
	prompts    []string
	decisions  map[string]agent.Decision
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
	stale := a.staleResume
	a.mu.Unlock()
	// A stale id starts a process that dies on arrival: it never emits the
	// system/init frame, so it never comes online and confirmOnline times out.
	if !stale {
		a.emit(agent.Event{Kind: agent.EventSystem, SessionID: sessionID})
	}
	return nil
}

// Online satisfies agent.Onliner: the channel closes when the fake emits its
// first system/init frame (via emit), mirroring the real adapter's scan loop.
func (a *fakeAdapter) Online() <-chan struct{} { return a.online }

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
// (the channel is closed) so a late-emitting test never panics. A system/init
// frame also signals online, mirroring the real adapter's scan loop.
func (a *fakeAdapter) emit(ev agent.Event) {
	a.mu.Lock()
	if a.killed {
		a.mu.Unlock()
		return
	}
	ch := a.events
	a.mu.Unlock()
	if ev.Kind == agent.EventSystem {
		a.onlineOnce.Do(func() { close(a.online) })
	}
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

// resumedWith returns the session id this adapter was Resumed on (empty if it was
// Spawned fresh). It is lock-guarded so a test can read it while a background
// driver goroutine (e.g. Restart) is still writing it.
func (a *fakeAdapter) resumedWith() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resumeID
}

// factory mints one fresh fakeAdapter per Spawn/Resume and records them so a test
// can drive the live one.
type factory struct {
	// staleResume stamps every minted adapter so a Resume never comes online —
	// the fixture for the confirm-on-resume fall-through (a Spawn still comes up
	// normally; only Resume is affected).
	staleResume bool

	mu      sync.Mutex
	made    []*fakeAdapter
	nextPID int
}

func (f *factory) new() agent.Adapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPID++
	a := &fakeAdapter{
		pid:         1000 + f.nextPID,
		events:      make(chan agent.Event, 16),
		online:      make(chan struct{}),
		staleResume: f.staleResume,
	}
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
	// Worktree checkouts default to a base under the user cache dir; redirect it to
	// a temp dir so tests never write into the real ~/.cache.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return gitRepo(t)
}

// gitRepo inits a fresh git repo in a temp dir with one empty commit and returns
// its path. Unlike newRepo it does not touch XDG_CACHE_HOME, so a test can stand
// up a second repo sharing the already-redirected worktree cache — the multi-repo
// fixture (drvctl-015).
func gitRepo(t *testing.T) string {
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

// newTicket creates a ticket with one Running, enabled attempt (its genesis
// "created" event derives to Running; an enable event opts it into supervision so
// the reconcile loop admits it) and returns the attempt id. Tests exercising the
// enable gate itself create a disabled attempt with newDisabledTicket.
func (w world) newTicket(t *testing.T, ticket string) string {
	t.Helper()
	return w.newTicketOnRepo(t, ticket, w.repo)
}

// newTicketOnRepo creates an enabled Running ticket whose attempt records the
// given repo path — the multi-repo fixture (drvctl-015). A repo path that is not
// a git working tree (or is empty) is recorded verbatim so an admit-failure path
// can be exercised.
func (w world) newTicketOnRepo(t *testing.T, ticket, repo string) string {
	t.Helper()
	att := w.newDisabledTicketOnRepo(t, ticket, repo)
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "enable", Actor: "agent:x", Body: "enabled"}); err != nil {
		t.Fatalf("enable attempt: %v", err)
	}
	return att
}

// newDisabledTicket creates a ticket with one Running attempt that has NOT been
// enabled — the default. The reconcile loop should leave it alone.
func (w world) newDisabledTicket(t *testing.T, ticket string) string {
	t.Helper()
	return w.newDisabledTicketOnRepo(t, ticket, w.repo)
}

func (w world) newDisabledTicketOnRepo(t *testing.T, ticket, repo string) string {
	t.Helper()
	if err := w.root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(w.root, ticket, attempt.New{Tool: "claude-code", Model: "opus-4.8", Repo: repo, Actor: "agent:x"})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	return m.ID
}

// reconciler wires a Reconciler over the world with the given factory and proc.
func (w world) reconciler(t *testing.T, f *factory, p *fakeProc) *reconcile.Reconciler {
	return w.reconcilerLimit(t, f, p, 0)
}

// reconcilerLimit is reconciler with a context-window auto-stop threshold set (0
// disables it, matching the default reconciler).
func (w world) reconcilerLimit(t *testing.T, f *factory, p *fakeProc, contextLimit int) *reconcile.Reconciler {
	t.Helper()
	// Repo binding is per-attempt now (drvctl-015): the reconciler derives a
	// worktree Manager from each attempt's recorded repo, so none is injected here.
	r, err := reconcile.New(reconcile.Options{
		Root:         w.root,
		Adapters:     f.adapters,
		Actor:        "agent:claude-code",
		ContextLimit: contextLimit,
		Proc:         p,
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	return r
}

// reconcilerConfirm is reconciler with a short resume-confirm window, so the
// confirm-on-resume fall-through can be exercised without a real 10s wait.
func (w world) reconcilerConfirm(t *testing.T, f *factory, p *fakeProc, confirm time.Duration) *reconcile.Reconciler {
	t.Helper()
	r, err := reconcile.New(reconcile.Options{
		Root:          w.root,
		Adapters:      f.adapters,
		Actor:         "agent:claude-code",
		Proc:          p,
		ResumeConfirm: confirm,
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	return r
}

// reconcilerLog is reconciler with an operational log sink attached, so a test
// can assert on the per-attempt errors a tick swallows (a bad repo path skips
// just that attempt, drvctl-015).
func (w world) reconcilerLog(t *testing.T, f *factory, p *fakeProc, logf func(string, ...any)) *reconcile.Reconciler {
	t.Helper()
	r, err := reconcile.New(reconcile.Options{
		Root:     w.root,
		Adapters: f.adapters,
		Actor:    "agent:claude-code",
		Proc:     p,
		Logf:     logf,
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	return r
}

// logCapture is a concurrency-safe Logf sink: admit failures are logged from the
// tick goroutine while ingest goroutines may log underneath it.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ln := range l.lines {
		if strings.Contains(ln, sub) {
			return true
		}
	}
	return false
}

// usageEvt is a per-request usage frame carrying a live context reading.
func usageEvt(ctxTokens int) agent.Event {
	return agent.Event{
		Kind:  agent.EventUsage,
		Usage: &agent.Usage{ContextTokens: ctxTokens, CostUSD: 0.1},
		Raw:   json.RawMessage(`{"ctx":` + strconv.Itoa(ctxTokens) + `}`),
	}
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

// turnEnd is a turn-end frame with the given terminal status ("success", "error",
// ...) — the signal the completion gate keys on.
func turnEnd(status string) agent.Event {
	return agent.Event{
		Kind:   agent.EventTurnEnd,
		Turn:   status,
		Result: "final assistant text",
		Raw:    json.RawMessage(`{"turn":"` + status + `"}`),
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

// TestTickIgnoresDisabledRunning: a Running attempt that has NOT been enabled is
// not admitted — being in Running is not consent to spawn an agent. The default is
// disabled, so an idle repo full of Running tickets stays quiet.
func TestTickIgnoresDisabledRunning(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newDisabledTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// No adapter was ever spawned, and no session id was recorded.
	if n := f.count(); n != 0 {
		t.Fatalf("disabled Running attempt was admitted: %d adapters spawned", n)
	}
	sess, err := session.Open(w.root, ticket, att)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	if id, err := sess.ReadIdentity(); err == nil && id.SessionID != "" {
		t.Fatalf("disabled attempt brought a session up: %+v", id)
	}
}

// TestTickAdmitsAfterEnable: a disabled Running attempt is parked; once `enable`
// is logged the very next tick admits it — the enable gate is the only thing that
// was holding it back.
func TestTickAdmitsAfterEnable(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newDisabledTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Parked while disabled.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("pre-enable tick: %v", err)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("admitted before enable: %d adapters spawned", n)
	}

	// Enable → next tick brings a session up.
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "enable", Actor: "human:dave", Body: "ship it"}); err != nil {
		t.Fatalf("append enable: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("post-enable tick: %v", err)
	}
	sess, err := session.Open(w.root, ticket, att)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	waitFor(t, "session brought up after enable", func() bool {
		id, err := sess.ReadIdentity()
		return err == nil && id.SessionID != "" && id.PID != 0
	})
}

// TestRetireOnDisable: an enabled, supervised attempt that is later disabled
// leaves the desired set; the tick reaps its live session (the attempt itself
// stays Running — a human can still work it by hand or re-enable it).
func TestRetireOnDisable(t *testing.T) {
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
	waitFor(t, "session admitted", func() bool { return f.count() == 1 })

	// Disable → the attempt leaves the desired set though it is still Running.
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "disable", Actor: "human:dave", Body: "park it"}); err != nil {
		t.Fatalf("append disable: %v", err)
	}
	if a, err := project.LoadAttempt(w.root, ticket, att); err != nil || a.State != project.Running || a.Enabled {
		t.Fatalf("expected Running+disabled after disable, got state=%v enabled=%v err=%v", a.State, a.Enabled, err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("retire tick: %v", err)
	}
	if !f.at(0).wasKilled() {
		t.Fatal("session was not reaped on disable")
	}
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
// set; the tick reaps the session and — because the checkout is clean — reclaims
// the worktree. The dirty-checkout counterpart is TestRetireOnReviewKeepsDirtyWorktree.
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

// On retire the meter's final Totals are folded into attempt.md metrics
// (drvctl-031): a completed attempt carries its own token/caching record.
func TestRetireFoldsMeterIntoAttemptMetrics(t *testing.T) {
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

	// The session metered some cached work.
	if _, err := sess.UpdateMeter(func(m *session.Meter) {
		m.Totals = agent.Totals{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, CacheCreationTokens: 30}
	}); err != nil {
		t.Fatalf("seed meter: %v", err)
	}

	// Claim review → Review state → no longer desired → retire.
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "done"}); err != nil {
		t.Fatalf("append review: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("retire tick: %v", err)
	}

	meta, err := attempt.LoadMeta(w.root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Metrics == nil {
		t.Fatal("retire did not fold the meter into attempt.md metrics")
	}
	if meta.Metrics.CacheReadTokens != 50 || meta.Metrics.CacheCreationTokens != 30 {
		t.Fatalf("raw cache fields not folded: %+v", *meta.Metrics)
	}
	if !meta.Metrics.CachingActive {
		t.Fatal("caching_active should be true after cache reads/creations were metered")
	}
	if meta.Metrics.NormalizedWork != 200 {
		t.Fatalf("normalized_work = %d, want 200", meta.Metrics.NormalizedWork)
	}
}

// TestRetireOnReviewKeepsDirtyWorktree is the drvctl-014 regression: an attempt
// that reaches Review with an *uncommitted* change must not have that change
// force-removed on retire. The daemon keeps the dirty checkout warm, records a
// note that it did, and a subsequent reopen (Review → Running via a decision,
// drv-002) resumes into the same worktree with the change intact — no silent loss.
func TestRetireOnReviewKeepsDirtyWorktree(t *testing.T) {
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
	waitFor(t, "session admitted", func() bool { return f.count() == 1 })
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	id, _ := sess.ReadIdentity()
	worktreePath := id.Worktree

	// The session implements the feature but never commits it — exactly the
	// drv-002 scenario. An untracked file in the checkout is the uncommitted work.
	uncommitted := filepath.Join(worktreePath, "feature.go")
	const want = "package feature // implemented, not committed\n"
	if err := os.WriteFile(uncommitted, []byte(want), 0o644); err != nil {
		t.Fatalf("write uncommitted change: %v", err)
	}

	// The agent claims review → Review state → no longer desired → retire.
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "done"}); err != nil {
		t.Fatalf("append review: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("retire tick: %v", err)
	}

	// The dirty checkout — and the uncommitted change — must survive the retire.
	if got, err := os.ReadFile(uncommitted); err != nil {
		t.Fatalf("uncommitted change was lost on retire: %v", err)
	} else if string(got) != want {
		t.Fatalf("uncommitted change corrupted: got %q, want %q", got, want)
	}
	// The preservation is recorded to the durable log, visible on the board.
	if !hasType(logTypes(t, w.root, ticket, att), "note") {
		t.Fatal("retire that kept a dirty worktree should record a note")
	}

	// Reopen: a decision against the Review attempt returns it to Running (drv-002).
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{Type: "decision", Actor: "human:dave", Body: "reopen: finish and commit it"}); err != nil {
		t.Fatalf("append reopen decision: %v", err)
	}
	if a, err := project.LoadAttempt(w.root, ticket, att); err != nil || a.State != project.Running {
		t.Fatalf("expected Running after reopen decision, got state=%v err=%v", a.State, err)
	}
	// The next tick re-admits it via Resume into the *same* worktree, still holding
	// the uncommitted change — the reopen resumes where the session stopped.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("reopen tick: %v", err)
	}
	waitFor(t, "session resumed", func() bool { return f.count() == 2 })
	if got, err := os.ReadFile(uncommitted); err != nil || string(got) != want {
		t.Fatalf("reopen must resume with the uncommitted change intact (got %q err %v)", got, err)
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

	// The resumed session must actually be *driven*: a headless stream-json process
	// produces nothing until it gets a user turn, so without this it would idle and
	// never continue the work (drvctl-022). Because the recorded id came back online
	// with its context intact, the drive is the short resume nudge — not the full
	// cold-start brief re-dumped.
	waitFor(t, "resume nudge prompt", func() bool { return len(resumed.prompted()) > 0 })
	if p := resumed.prompted()[0]; strings.Contains(p, "BRIEF") {
		t.Fatalf("resume must be nudged, not re-briefed: got %q", p)
	} else if !strings.Contains(p, "resuming") || !strings.Contains(p, "draiver brief PROJ-1") {
		t.Fatalf("resume nudge missing its guidance: %q", p)
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

// TestContextLimitAutoStops is the drvctl-012 headline: a session whose live
// context fill crosses the configured threshold is halted automatically, the stop
// is recorded as a durable escalation, and the attempt parks at Needs-me — while
// a session under the limit runs on untouched.
func TestContextLimitAutoStops(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconcilerLimit(t, f, newProc(), 150_000)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	live := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(live.prompted()) > 0 })

	// A frame under the limit meters but does not stop the session.
	live.emit(usageEvt(135_520))
	waitFor(t, "under-limit metered", func() bool {
		sess, err := session.Open(w.root, ticket, att)
		if err != nil {
			return false
		}
		defer sess.Close()
		m, err := sess.ReadMeter()
		return err == nil && m.Usage.ContextTokens == 135_520
	})
	if live.wasKilled() {
		t.Fatal("a session under the limit must not be auto-stopped")
	}
	if hasType(logTypes(t, w.root, ticket, att), "escalation") {
		t.Fatal("no escalation should exist under the limit")
	}

	// A frame over the limit halts the session and records the auto-stop.
	live.emit(usageEvt(151_000))
	waitFor(t, "auto-stop escalation recorded", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "escalation")
	})
	waitFor(t, "session halted", live.wasKilled)

	// The open escalation parks the attempt at Needs-me, so the next tick stops
	// desiring it rather than re-admitting it into the same runaway.
	a, err := project.LoadAttempt(w.root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != project.NeedsMe {
		t.Fatalf("state = %v, want NeedsMe after auto-stop", a.State)
	}
}

// TestCompletionGuardNudgesThenEscalates is the drvctl-022 turn-end guard: a
// session that ends a success turn without having filed a review or escalation is
// nudged once to hand off, and — if a later success turn still has nothing filed —
// escalated to a human and halted, so it can never silently stop at "done" and
// strand the attempt in Running + enabled.
func TestCompletionGuardNudgesThenEscalates(t *testing.T) {
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

	// First success turn with nothing filed → a one-shot nudge (a second prompt),
	// not an escalation, and the session stays live.
	live.emit(turnEnd("success"))
	waitFor(t, "completion nudge", func() bool {
		ps := live.prompted()
		return len(ps) >= 2 && strings.Contains(ps[1], "review") && strings.Contains(ps[1], "escalate")
	})
	if hasType(logTypes(t, w.root, ticket, att), "escalation") {
		t.Fatal("a first unmet success turn must nudge, not escalate")
	}
	if live.wasKilled() {
		t.Fatal("a nudge must not halt the session")
	}

	// The nudge went unheeded: a second unmet success turn escalates to a human and
	// halts the session, parking the attempt at Needs-me.
	live.emit(turnEnd("success"))
	waitFor(t, "stall escalation recorded", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "escalation")
	})
	waitFor(t, "session halted", live.wasKilled)

	a, err := project.LoadAttempt(w.root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != project.NeedsMe {
		t.Fatalf("state = %v, want NeedsMe after completion auto-stop", a.State)
	}
}

// TestCompletionGuardSatisfiedByReview: a session that DID claim review before its
// success turn ended has met its hand-off obligation, so the guard stays quiet — no
// nudge, no escalation. A non-success (error) turn is likewise never treated as a
// false "done".
func TestCompletionGuardSatisfiedByReview(t *testing.T) {
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

	// The agent runs `draiver review …`; the watcher promotes it into the durable
	// log before the turn ends.
	live.emit(bashCall("t1", `draiver review PROJ-1 "done, ready for review"`))
	waitFor(t, "promoted review", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "review")
	})

	// An error turn is not a "done"; a success turn now finds the obligation met.
	live.emit(turnEnd("error"))
	live.emit(turnEnd("success"))

	// Drive a second usage frame through and confirm the guard never acted: only the
	// brief prompt was ever sent, and no escalation was recorded.
	live.emit(usageEvt(4242))
	waitFor(t, "metered usage", func() bool {
		sess, err := session.Open(w.root, ticket, att)
		if err != nil {
			return false
		}
		defer sess.Close()
		m, err := sess.ReadMeter()
		return err == nil && m.Usage.ContextTokens == 4242
	})
	if got := live.prompted(); len(got) != 1 {
		t.Fatalf("guard must not nudge once review is filed; prompts = %v", got)
	}
	if hasType(logTypes(t, w.root, ticket, att), "escalation") {
		t.Fatal("guard must not escalate once review is filed")
	}
	if live.wasKilled() {
		t.Fatal("guard must not halt a session that handed off")
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

// TestMultiRepoAdmitsEachIntoItsOwnWorktree is the drvctl-015 headline: one
// reconciler over one data root drives two enabled attempts bound to *different*
// repos, and admits each into a worktree of its own repo — the per-ticket repo
// binding, with no shared controller repo and no collision.
func TestMultiRepoAdmitsEachIntoItsOwnWorktree(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)    // data root + repo A
	repoB := gitRepo(t) // a second, independent repo sharing the redirected cache
	attA := w.newTicketOnRepo(t, "PROJ-A", w.repo)
	attB := w.newTicketOnRepo(t, "PROJ-B", repoB)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitFor(t, "both attempts admitted", func() bool { return f.count() == 2 })

	// Each repo derives its own managed base (from its git-common-dir); the two
	// must differ, and each attempt's checkout must live under its own repo's base.
	baseA, baseB := mustBase(t, w.repo), mustBase(t, repoB)
	if baseA == baseB {
		t.Fatal("two distinct repos must derive distinct worktree bases")
	}
	wtA := attemptWorktree(t, w.root, "PROJ-A", attA)
	wtB := attemptWorktree(t, w.root, "PROJ-B", attB)
	if !strings.HasPrefix(wtA, baseA+string(os.PathSeparator)) {
		t.Fatalf("PROJ-A worktree %q is not under repo A base %q", wtA, baseA)
	}
	if !strings.HasPrefix(wtB, baseB+string(os.PathSeparator)) {
		t.Fatalf("PROJ-B worktree %q is not under repo B base %q", wtB, baseB)
	}
	// Both checkouts exist on disk — each admitted into a real, separate worktree.
	for _, wt := range []string{wtA, wtB} {
		if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
			t.Fatalf("worktree %q not on disk: %v", wt, err)
		}
	}
}

// TestBadRepoSkipsAttemptButSiblingRuns is the drvctl-015 isolation guarantee: an
// attempt whose repo path is not a git working tree fails only *its own* admit —
// logged with the ticket and the offending path — while a sibling on a good repo
// is still admitted, and the tick itself never errors.
func TestBadRepoSkipsAttemptButSiblingRuns(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	badRepo := t.TempDir() // a plain dir, not a git repository
	goodAtt := w.newTicketOnRepo(t, "PROJ-GOOD", w.repo)
	badAtt := w.newTicketOnRepo(t, "PROJ-BAD", badRepo)

	f := &factory{}
	sink := &logCapture{}
	r := w.reconcilerLog(t, f, newProc(), sink.logf)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("a per-attempt admit failure must not fail the tick: %v", err)
	}

	// The good sibling was admitted: a session came up with an id.
	waitFor(t, "good sibling admitted", func() bool {
		sess, err := session.Open(w.root, "PROJ-GOOD", goodAtt)
		if err != nil {
			return false
		}
		defer sess.Close()
		id, err := sess.ReadIdentity()
		return err == nil && id.SessionID != ""
	})

	// The bad attempt was not admitted: no session id recorded.
	badSess, err := session.Open(w.root, "PROJ-BAD", badAtt)
	if err != nil {
		t.Fatalf("open bad session: %v", err)
	}
	t.Cleanup(func() { badSess.Close() })
	if id, err := badSess.ReadIdentity(); err == nil && id.SessionID != "" {
		t.Fatalf("bad-repo attempt should not have been admitted: %+v", id)
	}

	// The failure is logged, actionable: it names the ticket and the offending path.
	if !sink.contains("PROJ-BAD") || !sink.contains(badRepo) {
		t.Fatalf("bad-repo admit error must name the ticket and path; got %v", sink.lines)
	}
}

// stripRepo blanks an attempt's recorded repo path in attempt.md, simulating a
// legacy or hand-edited attempt (drvctl-017): attempt.Create refuses to mint one
// with no repo, so the only way an attempt reaches admit repo-less is a file that
// predates the mandatory-repo rule or was edited by hand. The log (created +
// enable) is untouched, so the attempt still derives Running + enabled.
func stripRepo(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	path := root.AttemptMetaPath(ticket, att)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read attempt.md: %v", err)
	}
	var kept []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "repo:") {
			continue
		}
		kept = append(kept, ln)
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("rewrite attempt.md: %v", err)
	}
	if a, err := project.LoadAttempt(root, ticket, att); err != nil || a.Repo != "" {
		t.Fatalf("repo not stripped: repo=%q err=%v", a.Repo, err)
	}
}

// TestNoRepoAttemptEscalatesAndParks is the drvctl-017 safety net: an enabled
// Running attempt that records no repo (and has no --repo fallback) is not
// silently stalled. Its failed admit appends a durable escalation that flips it
// to Needs-me, so it lands on the board with an actionable ask — and the tick
// neither errors nor spawns a session. A second tick does not pile on a duplicate
// escalation.
func TestNoRepoAttemptEscalatesAndParks(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	att := w.newTicketOnRepo(t, "PROJ-NOREPO", w.repo)
	stripRepo(t, w.root, "PROJ-NOREPO", att)

	f := &factory{}
	sink := &logCapture{}
	// No DefaultRepo: nothing resolves the missing repo, so admit must escalate.
	r := w.reconcilerLog(t, f, newProc(), sink.logf)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("a repo-less attempt must not fail the tick: %v", err)
	}

	// No session was brought up.
	if n := f.count(); n != 0 {
		t.Fatalf("a repo-less attempt must not be admitted: %d adapters spawned", n)
	}

	// An escalation was recorded and it carries the actionable ask.
	events, err := ticketlog.Read(w.root, "PROJ-NOREPO", att)
	if err != nil {
		t.Fatal(err)
	}
	esc := escalationsIn(events)
	if len(esc) != 1 {
		t.Fatalf("want exactly 1 escalation, got %d: %v", len(esc), logTypes(t, w.root, "PROJ-NOREPO", att))
	}
	if !strings.Contains(esc[0].Body, "attempt.md") || !strings.Contains(esc[0].Body, "ctl up --repo") {
		t.Errorf("escalation body is not actionable: %q", esc[0].Body)
	}

	// The open escalation parks the attempt at Needs-me.
	a, err := project.LoadAttempt(w.root, "PROJ-NOREPO", att)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != project.NeedsMe {
		t.Fatalf("state = %v, want NeedsMe", a.State)
	}

	// A second tick must not pile on a duplicate escalation (Needs-me is not
	// desired, so admit stops re-running it — the de-dup requirement, drvctl-017).
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if esc := escalationsIn(mustRead(t, w.root, "PROJ-NOREPO", att)); len(esc) != 1 {
		t.Fatalf("a second tick duplicated the escalation: now %d", len(esc))
	}
}

// TestAdoptStrayCheckoutEscalatesAndDoesNotAbort is the drvctl-046 bug-2 headline:
// a foreign/detached checkout squatting inside the managed base (someone ran `git
// checkout` in it) yields the zero Key. It used to abort the whole crash-recovery
// reconcile — one stray hand-checkout blocked startup for *every* attempt — because
// Reconcile passed the invalid Key into Remove. Now Adopt completes: the stray is
// swept by path, the healthy sibling is untouched, and the affected attempt raises
// exactly one durable escalation (idempotent across restarts) that flips it to
// Needs-me — the promotion drvctl-027 deferred.
func TestAdoptStrayCheckoutEscalatesAndDoesNotAbort(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	okAtt := w.newTicketOnRepo(t, "PROJ-OK", w.repo)
	strayAtt := w.newTicketOnRepo(t, "PROJ-STRAY", w.repo)

	// Daemon A: admit both so each gets a real checkout + session.json under the base.
	fa := &factory{}
	a := w.reconciler(t, fa, newProc())
	if err := a.Tick(ctx); err != nil {
		t.Fatalf("A admit tick: %v", err)
	}
	waitFor(t, "both admitted", func() bool { return fa.count() == 2 })
	okWt := attemptWorktree(t, w.root, "PROJ-OK", okAtt)
	strayWt := attemptWorktree(t, w.root, "PROJ-STRAY", strayAtt)
	a.Close()

	// PROJ-STRAY's checkout drifts onto a foreign branch: git now reports it zero-key
	// — exactly the entry that used to abort the whole reconcile.
	gitCheckoutIn(t, strayWt, "-b", "hand-checkout")

	// Daemon B: Adopt must complete (not abort over the stray) and escalate PROJ-STRAY.
	fb := &factory{}
	b := w.reconciler(t, fb, newProc())
	t.Cleanup(b.Close)
	if err := b.Adopt(ctx); err != nil {
		t.Fatalf("a stray checkout must not abort Adopt: %v", err)
	}

	// Exactly one escalation, actionable (it names the stray path), flipping the
	// attempt to Needs-me.
	esc := escalationsIn(mustRead(t, w.root, "PROJ-STRAY", strayAtt))
	if len(esc) != 1 {
		t.Fatalf("want exactly 1 escalation on the stray attempt, got %d: %v", len(esc), logTypes(t, w.root, "PROJ-STRAY", strayAtt))
	}
	if !strings.Contains(esc[0].Body, strayWt) {
		t.Errorf("escalation must name the stray checkout path %q; got %q", strayWt, esc[0].Body)
	}
	if a, err := project.LoadAttempt(w.root, "PROJ-STRAY", strayAtt); err != nil {
		t.Fatal(err)
	} else if a.State != project.NeedsMe {
		t.Fatalf("stray attempt state = %v, want NeedsMe", a.State)
	}

	// The healthy sibling was untouched: no escalation, checkout intact on disk.
	if esc := escalationsIn(mustRead(t, w.root, "PROJ-OK", okAtt)); len(esc) != 0 {
		t.Fatalf("healthy sibling should not be escalated, got %d", len(esc))
	}
	if fi, err := os.Stat(okWt); err != nil || !fi.IsDir() {
		t.Errorf("healthy sibling checkout disturbed: %v", err)
	}

	// Idempotent across restarts: re-introduce the intrusion and Adopt again — the
	// still-open escalation must not be duplicated.
	if out, err := exec.Command("git", "-C", w.repo, "worktree", "add", strayWt, "-b", "hand-checkout-2").CombinedOutput(); err != nil {
		t.Fatalf("re-introduce stray: %v\n%s", err, out)
	}
	if err := b.Adopt(ctx); err != nil {
		t.Fatalf("second Adopt: %v", err)
	}
	if esc := escalationsIn(mustRead(t, w.root, "PROJ-STRAY", strayAtt)); len(esc) != 1 {
		t.Fatalf("a second Adopt duplicated the still-open escalation: now %d", len(esc))
	}
}

// gitCheckoutIn runs `git checkout` inside a checkout dir, drifting a managed
// worktree off its attempt branch (the drvctl-046 intrusion).
func gitCheckoutIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir, "checkout"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git checkout %v in %s: %v\n%s", args, dir, err, out)
	}
}

// escalationsIn returns just the escalation events, for counting.
func escalationsIn(events []event.Event) []event.Event {
	var out []event.Event
	for _, e := range events {
		if e.Type == "escalation" {
			out = append(out, e)
		}
	}
	return out
}

// mustRead reads an attempt's log or fails the test.
func mustRead(t *testing.T, root store.Root, ticket, att string) []event.Event {
	t.Helper()
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return events
}

// mustBase returns the managed worktree base a repo derives (via a throwaway
// Manager), for asserting where an attempt's checkout landed.
func mustBase(t *testing.T, repo string) string {
	t.Helper()
	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatalf("worktree manager for %s: %v", repo, err)
	}
	return m.Base()
}

// attemptWorktree reads the on-disk checkout path recorded for an attempt.
func attemptWorktree(t *testing.T, root store.Root, ticket, att string) string {
	t.Helper()
	sess, err := session.Open(root, ticket, att)
	if err != nil {
		t.Fatalf("open session %s/%s: %v", ticket, att, err)
	}
	defer sess.Close()
	id, err := sess.ReadIdentity()
	if err != nil {
		t.Fatalf("read identity %s/%s: %v", ticket, att, err)
	}
	return id.Worktree
}

func TestNewValidatesOptions(t *testing.T) {
	w := newWorld(t)
	f := &factory{}
	good := reconcile.Options{Root: w.root, Adapters: f.adapters, Actor: "agent:x"}
	if _, err := reconcile.New(good); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	cases := map[string]reconcile.Options{
		"no root":     {Adapters: f.adapters, Actor: "a"},
		"no adapters": {Root: w.root, Actor: "a"},
		"no actor":    {Root: w.root, Adapters: f.adapters},
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := reconcile.New(opt); err == nil {
				t.Fatal("New should reject invalid options")
			}
		})
	}
}
