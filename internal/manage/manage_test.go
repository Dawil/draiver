package manage_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/manage"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// --- fake adapter -----------------------------------------------------------

// fakeAdapter is an in-memory agent.Adapter: no process, no tokens. It records
// the id it was resumed with and echoes a system event onto its own fresh
// channel, so a test can prove the cattle loop reloads the right session id and
// re-attaches to a new stream.
type fakeAdapter struct {
	pid      int
	spawnErr error
	resumeErr error

	mu       sync.Mutex
	events   chan agent.Event
	started  bool
	killed   bool
	resumeID string
	prompts  []string
}

func (a *fakeAdapter) Spawn(ctx context.Context, spec agent.SessionSpec) (string, error) {
	if a.spawnErr != nil {
		// A real adapter mints the id before the process is confirmed.
		return "sess-partial", a.spawnErr
	}
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
	if a.resumeErr != nil {
		a.mu.Unlock()
		return a.resumeErr
	}
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

// PID satisfies manage's optional pid-accessor: 0 unless a process is "running".
func (a *fakeAdapter) PID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		return 0
	}
	return a.pid
}

func (a *fakeAdapter) emit(ev agent.Event) { a.events <- ev }

// factory mints one fresh fakeAdapter per Spawn/Resume (an adapter is single-use)
// and keeps the lot so a test can inspect the resumed instance.
type factory struct {
	mu        sync.Mutex
	made      []*fakeAdapter
	nextPID   int
	spawnErr  error
	resumeErr error
}

func (f *factory) new() agent.Adapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPID++
	a := &fakeAdapter{
		pid:       1000 + f.nextPID,
		events:    make(chan agent.Event, 8),
		spawnErr:  f.spawnErr,
		resumeErr: f.resumeErr,
	}
	f.made = append(f.made, a)
	return a
}

func (f *factory) at(i int) *fakeAdapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.made[i]
}

func (f *factory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

// --- fixture ----------------------------------------------------------------

// newRepo makes a throwaway git repo with one commit; skips if git is absent.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Worktree checkouts default to a base under the user cache dir; redirect it to
	// a temp dir so tests never write into the real ~/.cache.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
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

// newFixture sets up a real ticket+attempt, an open session store, and a worktree
// manager over a real git repo.
func newFixture(t *testing.T) (*session.Store, *worktree.Manager, string, string) {
	t.Helper()
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	ticket := "PROJ-1"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Model: "opus-4.8", Repo: "/repo", Actor: "agent:x"})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	sess, err := session.Open(root, ticket, m.ID)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	wm, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatalf("worktree manager: %v", err)
	}
	return sess, wm, ticket, m.ID
}

func newHandle(t *testing.T, sess *session.Store, wm *worktree.Manager, f *factory, ticket, att string) *manage.Handle {
	t.Helper()
	h, err := manage.New(sess, wm, f.new, manage.Config{
		Ticket:         ticket,
		Attempt:        att,
		Adapter:        "claude-code",
		AdapterVersion: "2.1.216",
		Model:          "opus-4.8",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func recv(t *testing.T, ch <-chan agent.Event) agent.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed unexpectedly")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a stream event")
		return agent.Event{}
	}
}

// --- tests ------------------------------------------------------------------

// TestCattleLoop is the headline: spawn a session, kill it keeping the id + log,
// then resume it into a fresh process that reloads the same session id and
// re-attaches a new stream.
func TestCattleLoop(t *testing.T) {
	ctx := context.Background()
	sess, wm, ticket, att := newFixture(t)
	f := &factory{}
	h := newHandle(t, sess, wm, f, ticket, att)

	// --- Spawn ---
	if err := h.Spawn(ctx); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	sys := recv(t, h.Stream())
	if sys.Kind != agent.EventSystem || sys.SessionID == "" {
		t.Fatalf("first event = %+v, want a system event with a session id", sys)
	}
	liveID := sys.SessionID

	id1, err := h.Identity()
	if err != nil {
		t.Fatalf("Identity after spawn: %v", err)
	}
	if id1.SessionID != liveID {
		t.Fatalf("persisted session id = %q, want %q", id1.SessionID, liveID)
	}
	if id1.PID == 0 {
		t.Fatal("persisted pid is 0 while the session is live")
	}
	if id1.Worktree == "" {
		t.Fatal("persisted worktree is empty")
	}
	if fi, err := os.Stat(id1.Worktree); err != nil || !fi.IsDir() {
		t.Fatalf("worktree %q not on disk: %v", id1.Worktree, err)
	}
	if err := h.Prompt(ctx, "cold-start: run `draiver brief`"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// --- Kill: keep the id + log, drop the pid ---
	if err := h.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if h.Live() {
		t.Fatal("Live() true after Kill")
	}
	if !f.at(0).killed {
		t.Fatal("the session process was not reaped")
	}
	id2, err := h.Identity()
	if err != nil {
		t.Fatalf("Identity after kill: %v", err)
	}
	if id2.SessionID != liveID {
		t.Fatalf("session id lost across kill: got %q, want %q", id2.SessionID, liveID)
	}
	if id2.PID != 0 {
		t.Fatalf("pid = %d after kill, want 0 (stale)", id2.PID)
	}

	// --- Resume: fresh process, same session id, new stream ---
	if err := h.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if f.count() != 2 {
		t.Fatalf("adapters made = %d, want 2 (spawn + resume)", f.count())
	}
	resumed := f.at(1)
	if resumed.resumeID != liveID {
		t.Fatalf("resumed with id %q, want the persisted %q", resumed.resumeID, liveID)
	}
	cont := recv(t, h.Stream())
	if cont.SessionID != liveID {
		t.Fatalf("resumed stream session id = %q, want %q (same session, new process)", cont.SessionID, liveID)
	}

	id3, err := h.Identity()
	if err != nil {
		t.Fatalf("Identity after resume: %v", err)
	}
	if id3.SessionID != liveID {
		t.Fatalf("session id changed on resume: %q != %q", id3.SessionID, liveID)
	}
	if id3.PID == 0 {
		t.Fatal("resume left pid 0")
	}
	if id3.PID == id1.PID {
		t.Fatalf("resume reused the old pid %d — expected a fresh process", id1.PID)
	}
	if !id3.Started.Equal(id1.Started) {
		t.Fatalf("resume changed the session birth time: %v != %v", id3.Started, id1.Started)
	}
	if id3.Worktree != id1.Worktree {
		t.Fatalf("resume changed the worktree: %q != %q", id3.Worktree, id1.Worktree)
	}
}

func TestKillIsIdempotentAndSafeWhenIdle(t *testing.T) {
	ctx := context.Background()
	sess, wm, ticket, att := newFixture(t)
	h := newHandle(t, sess, wm, &factory{}, ticket, att)

	// Kill with no live session is a no-op.
	if err := h.Kill(); err != nil {
		t.Fatalf("Kill (idle): %v", err)
	}
	if err := h.Spawn(ctx); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := h.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := h.Kill(); err != nil {
		t.Fatalf("second Kill (idempotent): %v", err)
	}
}

func TestResumeNeedsARecordedSession(t *testing.T) {
	sess, wm, ticket, att := newFixture(t)
	h := newHandle(t, sess, wm, &factory{}, ticket, att)
	if err := h.Resume(context.Background()); err == nil {
		t.Fatal("Resume without a prior spawn should error (no session.json)")
	}
}

func TestResumeRejectsLiveSession(t *testing.T) {
	ctx := context.Background()
	sess, wm, ticket, att := newFixture(t)
	h := newHandle(t, sess, wm, &factory{}, ticket, att)
	if err := h.Spawn(ctx); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer h.Kill()
	if err := h.Resume(ctx); err == nil {
		t.Fatal("Resume with a live session should error; kill first")
	}
}

func TestSpawnRejectsDoubleStart(t *testing.T) {
	ctx := context.Background()
	sess, wm, ticket, att := newFixture(t)
	h := newHandle(t, sess, wm, &factory{}, ticket, att)
	if err := h.Spawn(ctx); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer h.Kill()
	if err := h.Spawn(ctx); err == nil {
		t.Fatal("second Spawn should error")
	}
}

// TestSpawnPersistsHandleOnFailure: even when the process fails to come up, the
// minted session id is persisted so the attempt stays resumable/inspectable.
func TestSpawnPersistsHandleOnFailure(t *testing.T) {
	sess, wm, ticket, att := newFixture(t)
	f := &factory{spawnErr: context.DeadlineExceeded}
	h := newHandle(t, sess, wm, f, ticket, att)

	if err := h.Spawn(context.Background()); err == nil {
		t.Fatal("Spawn should surface the adapter failure")
	}
	if h.Live() {
		t.Fatal("a failed spawn must not leave a live session")
	}
	id, err := h.Identity()
	if err != nil {
		t.Fatalf("Identity after failed spawn: %v", err)
	}
	if id.SessionID != "sess-partial" {
		t.Fatalf("cattle handle not persisted on failure: got %q", id.SessionID)
	}
}

// TestSpawnRecordsAdapterVersion: the configured adapter version is stamped into
// session.json at spawn (drvctl-033 provenance) and, because it is pinned for the
// session's lifetime, survives a kill+resume unchanged.
func TestSpawnRecordsAdapterVersion(t *testing.T) {
	ctx := context.Background()
	sess, wm, ticket, att := newFixture(t)
	f := &factory{}
	h := newHandle(t, sess, wm, f, ticket, att) // newHandle configures version "2.1.216"

	if err := h.Spawn(ctx); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	id, err := h.Identity()
	if err != nil {
		t.Fatalf("Identity after spawn: %v", err)
	}
	if id.AdapterVersion != "2.1.216" {
		t.Fatalf("adapter version = %q, want %q", id.AdapterVersion, "2.1.216")
	}

	// The pin holds across the cattle loop: kill drops the pid but keeps the version.
	if err := h.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := h.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer h.Kill()
	id2, err := h.Identity()
	if err != nil {
		t.Fatalf("Identity after resume: %v", err)
	}
	if id2.AdapterVersion != "2.1.216" {
		t.Fatalf("adapter version after resume = %q, want it pinned at %q", id2.AdapterVersion, "2.1.216")
	}
}

func TestPromptAndInterruptNeedALiveSession(t *testing.T) {
	sess, wm, ticket, att := newFixture(t)
	h := newHandle(t, sess, wm, &factory{}, ticket, att)
	if err := h.Prompt(context.Background(), "hi"); err == nil {
		t.Fatal("Prompt with no live session should error")
	}
	if err := h.Interrupt(context.Background()); err == nil {
		t.Fatal("Interrupt with no live session should error")
	}
	if h.Stream() != nil {
		t.Fatal("Stream() should be nil with no live session")
	}
}

func TestNewValidatesArgs(t *testing.T) {
	sess, wm, ticket, att := newFixture(t)
	f := &factory{}
	cfg := manage.Config{Ticket: ticket, Attempt: att}
	cases := []struct {
		name string
		sess *session.Store
		wm   *worktree.Manager
		fn   func() agent.Adapter
		cfg  manage.Config
	}{
		{"nil session", nil, wm, f.new, cfg},
		{"nil worktree", sess, nil, f.new, cfg},
		{"nil factory", sess, wm, nil, cfg},
		{"no ticket", sess, wm, f.new, manage.Config{Attempt: att}},
		{"no attempt", sess, wm, f.new, manage.Config{Ticket: ticket}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := manage.New(tc.sess, tc.wm, tc.fn, tc.cfg); err == nil {
				t.Fatal("New should reject invalid args")
			}
		})
	}
}
