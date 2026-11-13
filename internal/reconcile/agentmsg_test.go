package reconcile_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func (a *fakeAdapter) spawnEnv() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.env...)
}

// TestSessionSpawnCarriesMessageWatermark (drvctl-059): a brought-up session is
// handed its own ticket/attempt and the log tail at bring-up as DRAIVER_SESSION_*,
// and the watermark is persisted off-chain for the next resume note.
func TestSessionSpawnCarriesMessageWatermark(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "MSG"
	att := w.newTicket(t, ticket)
	evs, err := ticketlog.Read(w.root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	tail := evs[len(evs)-1].Seq

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "spawned", func() bool { return f.count() == 1 && len(f.at(0).prompted()) > 0 })

	env := f.at(0).spawnEnv()
	for _, want := range agentmsg.SessionEnv(ticket, att, tail) {
		if !slices.Contains(env, want) {
			t.Fatalf("spawn env missing %q: %v", want, env)
		}
	}
	got, err := agentmsg.ReadWatermark(w.root, ticket, att)
	if err != nil || got != tail {
		t.Fatalf("persisted watermark = %d, %v; want %d", got, err, tail)
	}
}

// resumeAfterIgnoredMessage seeds the exap-023 shape: a session started at
// watermark W, the human posted a message mid-session, and the agent claimed review
// (an agent-authored event *above* the message). read controls whether the agent
// read it with `draiver messages` first. A later human reopen resumes the session.
func resumeAfterIgnoredMessage(t *testing.T, read bool) (prompt string, msgSeq int) {
	t.Helper()
	ctx := context.Background()
	w := newWorld(t)
	ticket := "EXAP"
	att := w.newTicket(t, ticket)

	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	if err := sess.WriteIdentity(session.Identity{Adapter: "claude-code", SessionID: "live-id"}); err != nil {
		t.Fatal(err)
	}
	evs, _ := ticketlog.Read(w.root, ticket, att)
	if err := agentmsg.WriteWatermark(w.root, ticket, att, evs[len(evs)-1].Seq); err != nil {
		t.Fatal(err)
	}
	msg, err := ticketlog.Append(w.root, ticket, att, event.Event{
		Type: "decision", Actor: "human:david", To: agentmsg.ToAgent,
		Body: "If possible, push directly to main on forgejo remote once done.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if read {
		if err := agentmsg.AdvanceCursor(w.root, ticket, att, msg.Seq); err != nil {
			t.Fatal(err)
		}
	}
	appendEvt(t, w.root, ticket, att, event.Event{Type: "test-result", Actor: "agent:x", Rung: "unit", Body: "green"})
	appendEvt(t, w.root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "Claimed review."})
	// The reopen: an unmarked board decision (so only the message rule can surface #msg).
	appendEvt(t, w.root, ticket, att, event.Event{Type: "decision", Actor: "human:webui", Body: "Reopened to Running."})

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "resumed", func() bool { return f.count() == 1 && len(f.at(0).prompted()) > 0 })
	if f.at(0).resumedWith() != "live-id" {
		t.Fatalf("expected a resume, got %q", f.at(0).resumedWith())
	}
	return f.at(0).prompted()[0], msg.Seq
}

// TestResumeNoteListsUnreadMessageBelowReview is the exap-023 #12 regression: the
// message sits below the review (the agent-authored watermark) yet was never read,
// so the resume note must still list it.
func TestResumeNoteListsUnreadMessageBelowReview(t *testing.T) {
	p, seq := resumeAfterIgnoredMessage(t, false)
	if !strings.Contains(p, fmt.Sprintf("#%d", seq)) || !strings.Contains(p, "push directly to main") {
		t.Fatalf("resume note must list the never-read message #%d:\n%s", seq, p)
	}
	if !strings.Contains(p, "message to you") {
		t.Fatalf("resume note should flag it as a message to the agent:\n%s", p)
	}
}

// TestResumeNoteOmitsReadMessage: a message the agent read via `draiver messages`
// (cursor at/above it) is not re-listed.
func TestResumeNoteOmitsReadMessage(t *testing.T) {
	p, seq := resumeAfterIgnoredMessage(t, true)
	if strings.Contains(p, fmt.Sprintf("#%d ", seq)) || strings.Contains(p, "push directly to main") {
		t.Fatalf("a read message must not be re-listed:\n%s", p)
	}
}
