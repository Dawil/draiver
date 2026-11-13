package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// sessionEnvOptIn lists tests that set DRAIVER_SESSION_* themselves; runE clears
// the session env for every other test so a suite run inside a live supervised
// session never inherits its notice (drvctl-059).
var (
	sessionEnvMu    sync.Mutex
	sessionEnvOptIn = map[string]bool{}
)

func setSessionEnv(t *testing.T, ticket, att string, seq int) {
	t.Helper()
	sessionEnvMu.Lock()
	sessionEnvOptIn[t.Name()] = true
	sessionEnvMu.Unlock()
	t.Cleanup(func() {
		sessionEnvMu.Lock()
		delete(sessionEnvOptIn, t.Name())
		sessionEnvMu.Unlock()
	})
	t.Setenv(agentmsg.EnvTicket, ticket)
	t.Setenv(agentmsg.EnvAttempt, att)
	t.Setenv(agentmsg.EnvSeq, strconv.Itoa(seq))
}

// runSplit runs like runE but captures stdout and stderr separately.
func runSplit(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	resetForRun(t)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			code = ee.code
			errb.WriteString("draiver: " + ee.msg + "\n")
		} else {
			code = 1
			errb.WriteString("draiver: " + err.Error() + "\n")
		}
	}
	return out.String(), errb.String(), code
}

// msgWorld seeds a ticket whose session started at the current tail.
func msgWorld(t *testing.T) (dir string, sessionSeq int) {
	t.Helper()
	dir = newTicket(t)
	// A message posted *before* the session — the brief covers it.
	if _, code := run(t, "--data", dir, "--actor", "human:dave", "log", "PROJ-1", "--type", "note", "--to", "agent", "earlier message"); code != 0 {
		t.Fatal("seed log failed")
	}
	evs, err := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	sessionSeq = evs[len(evs)-1].Seq
	setSessionEnv(t, "PROJ-1", "0001", sessionSeq)
	return dir, sessionSeq
}

func postMessage(t *testing.T, dir, body string) {
	t.Helper()
	if _, code := run(t, "--data", dir, "--actor", "human:dave", "log", "PROJ-1", "--type", "decision", "--to", "agent", body); code != 0 {
		t.Fatal("post message failed")
	}
}

func TestNoticeOnlyForMessagesSinceSessionStart(t *testing.T) {
	dir, _ := msgWorld(t)
	stdout, stderr, code := runSplit(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "--type", "note", "working")
	if code != 0 || strings.Contains(stderr, "📬") || !strings.Contains(stdout, "logged note") {
		t.Fatalf("pre-session message must not notify: code=%d out=%q err=%q", code, stdout, stderr)
	}
	// Spine notes stamped human:* never count.
	run(t, "--data", dir, "--actor", "human:david", "log", "PROJ-1", "--type", "note", "Daemon reclaimed the worktree")
	run(t, "--data", dir, "--actor", "human:webui", "log", "PROJ-1", "--type", "note", "Restart flush")
	_, stderr, _ = runSplit(t, "--data", dir, "status", "PROJ-1")
	if strings.Contains(stderr, "📬") {
		t.Fatalf("unmarked spine notes must not notify: %q", stderr)
	}

	postMessage(t, dir, "SECRET-CONTENT push to main please")
	stdout, stderr, code = runSplit(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "--type", "note", "more work")
	if code != 0 || !strings.Contains(stdout, "logged note") {
		t.Fatalf("command behaviour changed: code=%d out=%q", code, stdout)
	}
	if strings.Contains(stdout, "📬") {
		t.Fatalf("notice must go to stderr only, stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "📬 1 unread message") || !strings.Contains(stderr, "draiver messages") {
		t.Fatalf("missing notice on stderr: %q", stderr)
	}
	if strings.Contains(stderr, "SECRET-CONTENT") {
		t.Fatalf("notice must be count-only: %q", stderr)
	}
}

func TestMessagesClearsNoticeAndNewMessageReappears(t *testing.T) {
	dir, _ := msgWorld(t)
	postMessage(t, dir, "first message body")
	stdout, _, code := runSplit(t, "--data", dir, "messages")
	if code != 0 || !strings.Contains(stdout, "first message body") || strings.Contains(stdout, "earlier message") {
		t.Fatalf("messages output wrong: code=%d %q", code, stdout)
	}
	_, stderr, _ := runSplit(t, "--data", dir, "recall", "PROJ-1")
	if strings.Contains(stderr, "📬") {
		t.Fatalf("notice must clear after `draiver messages`: %q", stderr)
	}
	postMessage(t, dir, "second")
	postMessage(t, dir, "third")
	_, stderr, _ = runSplit(t, "--data", dir, "brief", "PROJ-1")
	if !strings.Contains(stderr, "📬 2 unread messages") {
		t.Fatalf("new messages must re-raise the notice with the new count: %q", stderr)
	}
}

func TestReviewSoftStoppedByUnread(t *testing.T) {
	dir, _ := msgWorld(t)
	postMessage(t, dir, "push to main")
	before, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	_, stderr, code := runSplit(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done")
	if code != ExitUnreadMessages {
		t.Fatalf("review exit = %d, want %d; %q", code, ExitUnreadMessages, stderr)
	}
	if !strings.Contains(stderr, "📬 1 unread message") || !strings.Contains(stderr, "--ignore-unread") || !strings.Contains(stderr, "review not recorded") {
		t.Fatalf("review refusal text: %q", stderr)
	}
	after, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if len(after) != len(before) {
		t.Fatalf("a refused review must append nothing: %d -> %d", len(before), len(after))
	}

	stdout, _, code := runSplit(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done", "--ignore-unread")
	if code != 0 || !strings.Contains(stdout, "review claimed") {
		t.Fatalf("--ignore-unread must claim: code=%d %q", code, stdout)
	}
}

func TestReviewAfterReadingClaims(t *testing.T) {
	dir, _ := msgWorld(t)
	postMessage(t, dir, "push to main")
	runSplit(t, "--data", dir, "messages")
	stdout, stderr, code := runSplit(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done")
	if code != 0 || !strings.Contains(stdout, "review claimed") || strings.Contains(stderr, "📬") {
		t.Fatalf("review after reading must claim cleanly: code=%d out=%q err=%q", code, stdout, stderr)
	}
}

func TestNoNoticeOutsideSession(t *testing.T) {
	dir := newTicket(t)
	postMessage(t, dir, "hello agent")
	_, stderr, code := runSplit(t, "--data", dir, "log", "PROJ-1", "--type", "note", "x")
	if code != 0 || strings.Contains(stderr, "📬") {
		t.Fatalf("no session env ⇒ no notice: code=%d %q", code, stderr)
	}
	stdout, _, code := runSplit(t, "--data", dir, "review", "PROJ-1", "done")
	if code != 0 || !strings.Contains(stdout, "review claimed") {
		t.Fatalf("outside a session review is never soft-stopped: code=%d %q", code, stdout)
	}
}

func TestNoticeFailsSilentOnUnreadableCursor(t *testing.T) {
	dir, _ := msgWorld(t)
	postMessage(t, dir, "push to main")
	cur := filepath.Join(store.Root{Dir: dir}.SessionDir("PROJ-1", "0001"), "messages-read.json")
	if err := os.MkdirAll(filepath.Dir(cur), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSplit(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "--type", "note", "x")
	if code != 0 || !strings.Contains(stdout, "logged note") || strings.Contains(stderr, "📬") {
		t.Fatalf("a broken cursor must leave the command untouched: code=%d out=%q err=%q", code, stdout, stderr)
	}
	// Review fails open too: the claim records as it would have before.
	stdout, _, code = runSplit(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done")
	if code != 0 || !strings.Contains(stdout, "review claimed") {
		t.Fatalf("review must fail open on a broken cursor: code=%d %q", code, stdout)
	}
}

func TestInboxUnchangedByMessages(t *testing.T) {
	dir, _ := msgWorld(t)
	postMessage(t, dir, "hello agent")
	stdout, stderr, code := runSplit(t, "--data", dir, "inbox")
	if code != 0 || strings.Contains(stdout+stderr, "📬") || strings.Contains(stdout, "hello agent") {
		t.Fatalf("inbox (human escalation inbox) must be unchanged: code=%d out=%q err=%q", code, stdout, stderr)
	}
}

func TestLogToRejectsUnknownRecipient(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "log", "PROJ-1", "--type", "note", "--to", "bob", "x"); code == 0 {
		t.Fatal("--to other than agent must be rejected")
	}
}
