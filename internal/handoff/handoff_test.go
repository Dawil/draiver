package handoff_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/handoff"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func fixture(t *testing.T) (store.Root, string, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	ticket := "PROJ-1"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Model: "m", Repo: "/repo", Actor: "human:x"})
	if err != nil {
		t.Fatal(err)
	}
	return root, ticket, m.ID
}

// fakeSession records the interrupt/prompt calls and runs onPrompt (the "agent")
// when prompted.
type fakeSession struct {
	mu          sync.Mutex
	interrupted bool
	prompts     []string
	promptErr   error
	onPrompt    func()
}

func (s *fakeSession) Interrupt(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupted = true
	return nil
}

func (s *fakeSession) Prompt(_ context.Context, text string) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, text)
	err, fn := s.promptErr, s.onPrompt
	s.mu.Unlock()
	if err == nil && fn != nil {
		go fn()
	}
	return err
}

func logNote(t *testing.T, root store.Root, ticket, att string, tags ...string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "note", Actor: "agent:claude-code", Body: "Handoff: x", Tags: tags}); err != nil {
		t.Error(err)
	}
}

func TestCheckpointWrittenWhenSessionLogsHandoffNote(t *testing.T) {
	root, ticket, att := fixture(t)
	// A pre-existing handoff note (from an earlier session) must not count.
	logNote(t, root, ticket, att, handoff.Tag)
	sess := &fakeSession{}
	sess.onPrompt = func() {
		logNote(t, root, ticket, att, "other") // a plain note is not the handoff
		logNote(t, root, ticket, att, handoff.Tag)
	}
	ck := handoff.New(root, ticket, att, sess, 2*time.Second)
	ck.Poll = 5 * time.Millisecond

	res := ck.Run(context.Background(), true, "test reason")
	if res.Outcome != handoff.Written {
		t.Fatalf("outcome = %v, want written (%s)", res.Outcome, res.Describe())
	}
	evs, _ := ticketlog.Read(root, ticket, att)
	if last := evs[len(evs)-1]; res.NoteSeq != last.Seq {
		t.Errorf("NoteSeq = %d, want the new note #%d", res.NoteSeq, last.Seq)
	}
	if !sess.interrupted {
		t.Error("an in-flight turn must be interrupted before the prompt")
	}
	if len(sess.prompts) != 1 || !strings.Contains(sess.prompts[0], "draiver log PROJ-1 --type note --tag handoff") ||
		!strings.Contains(sess.prompts[0], "test reason") {
		t.Errorf("prompt = %q", sess.prompts)
	}
	for _, section := range []string{"## Done", "## In progress", "## Uncommitted files", "## Gotchas", "## Don't redo"} {
		if !strings.Contains(sess.prompts[0], section) {
			t.Errorf("prompt is missing section %q", section)
		}
	}
}

func TestCheckpointIdleSessionIsNotInterrupted(t *testing.T) {
	root, ticket, att := fixture(t)
	sess := &fakeSession{onPrompt: func() { logNote(t, root, ticket, att, handoff.Tag) }}
	ck := handoff.New(root, ticket, att, sess, 2*time.Second)
	ck.Poll = 5 * time.Millisecond
	if res := ck.Run(context.Background(), false, "r"); res.Outcome != handoff.Written {
		t.Fatalf("outcome = %v", res.Outcome)
	}
	if sess.interrupted {
		t.Error("no turn in flight: nothing to interrupt")
	}
}

func TestCheckpointTimesOutAfterGrace(t *testing.T) {
	root, ticket, att := fixture(t)
	ck := handoff.New(root, ticket, att, &fakeSession{}, 50*time.Millisecond)
	ck.Poll = 5 * time.Millisecond
	res := ck.Run(context.Background(), false, "r")
	if res.Outcome != handoff.TimedOut || res.TurnEnded {
		t.Fatalf("result = %+v, want a grace timeout", res)
	}
	if !strings.Contains(res.Describe(), "grace period") {
		t.Errorf("Describe = %q", res.Describe())
	}
}

// A handoff turn that ends without a note is a timeout — with a turn in flight
// the first turn end is the interrupted turn, the second the handoff turn.
func TestCheckpointTurnEndWithoutNoteIsTimeout(t *testing.T) {
	root, ticket, att := fixture(t)
	var ck *handoff.Checkpoint
	end := agent.Event{Kind: agent.EventTurnEnd, Turn: "success"}
	sess := &fakeSession{}
	sess.onPrompt = func() {
		ck.Observe(agent.Event{Kind: agent.EventTurnEnd, Origin: "task-1"}) // background turn: ignored
		ck.Observe(end)                                                     // the interrupted turn
	}
	ck = handoff.New(root, ticket, att, sess, 5*time.Second)
	ck.Poll = 5 * time.Millisecond

	done := make(chan handoff.Result, 1)
	go func() { done <- ck.Run(context.Background(), true, "r") }()
	select {
	case res := <-done:
		t.Fatalf("finished after only the interrupted turn ended: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}
	ck.Observe(end) // the handoff turn ends with no note
	select {
	case res := <-done:
		if res.Outcome != handoff.TimedOut || !res.TurnEnded {
			t.Fatalf("result = %+v, want a turn-ended timeout", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint did not end on the handoff turn's end")
	}
}

func TestCheckpointPromptFailure(t *testing.T) {
	root, ticket, att := fixture(t)
	ck := handoff.New(root, ticket, att, &fakeSession{promptErr: errors.New("stdin closed")}, time.Second)
	res := ck.Run(context.Background(), false, "r")
	if res.Outcome != handoff.Failed || !strings.Contains(res.Describe(), "stdin closed") {
		t.Fatalf("result = %+v", res)
	}
}

func TestCheckpointCancelled(t *testing.T) {
	root, ticket, att := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	ck := handoff.New(root, ticket, att, &fakeSession{onPrompt: cancel}, 5*time.Second)
	ck.Poll = 5 * time.Millisecond
	if res := ck.Run(ctx, false, "r"); res.Outcome != handoff.Cancelled {
		t.Fatalf("outcome = %v, want cancelled", res.Outcome)
	}
}

func TestHeadroomWarning(t *testing.T) {
	cases := []struct {
		limit, window int
		warn          bool
	}{
		{150_000, 200_000, false}, // 190K fits
		{160_000, 200_000, false}, // exactly fits
		{170_000, 200_000, true},
		{0, 200_000, false}, // limit disabled
		{190_000, 0, false}, // window unknown
	}
	for _, c := range cases {
		got := handoff.HeadroomWarning(c.limit, c.window)
		if (got != "") != c.warn {
			t.Errorf("HeadroomWarning(%d, %d) = %q, want warn=%v", c.limit, c.window, got, c.warn)
		}
	}
	if w := handoff.HeadroomWarning(170_000, 200_000); !strings.Contains(w, "160000") {
		t.Errorf("warning should suggest a limit of 160000: %q", w)
	}
}
