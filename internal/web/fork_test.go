package web

import (
	"html"
	"net/http/httptest"

	"github.com/Dawil/draiver/internal/store"
	"os/exec"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/session"
)

func TestForkCommand(t *testing.T) {
	t.Setenv("HOME", "/home/op")
	def := "/home/op/.draiver/data"
	cc := func(sid, wt string) session.Identity {
		return session.Identity{Adapter: "claude-code", SessionID: sid, Worktree: wt}
	}
	cases := []struct {
		name    string
		id      session.Identity
		dataDir string
		want    string
	}{
		{"absolute worktree, default root", cc("abc-123", "/home/me/wt/0001"), def,
			`draiver fork 'T-1@0001'`},
		{"worktree with spaces and a quote", cc("s1", "/tmp/it's a dir"), def,
			`draiver fork 'T-1@0001'`},
		{"non-default root carries --data", cc("s1", "/tmp/wt"), "/srv/draiver data",
			`draiver --data '/srv/draiver data' fork 'T-1@0001'`},
		{"root with a quote is quoted", cc("s1", "/tmp/wt"), "/srv/it's",
			`draiver --data '/srv/it'\''s' fork 'T-1@0001'`},
		{"empty session id", cc("", "/tmp/wt"), def, ""},
		{"blank session id", cc("   ", "/tmp/wt"), def, ""},
		{"relative worktree", cc("s1", "wt/0001"), def, ""},
		{"empty worktree", cc("s1", ""), def, ""},
		{"other adapter", session.Identity{Adapter: "aider", SessionID: "s1", Worktree: "/tmp/wt"}, def, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forkCommand(c.id, "T-1", "0001", c.dataDir); got != c.want {
				t.Fatalf("forkCommand = %q\nwant          %q", got, c.want)
			}
		})
	}
}

// TestForkCommandShellRoundTrip proves the quoting survives a real shell: the
// quoted worktree and session id come back byte-for-byte, however hostile.
func TestForkCommandShellRoundTrip(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	for _, v := range []string{"/tmp/plain", "/tmp/a b", "/tmp/it's", `/tmp/$(x) "q" ` + "`y`", "/tmp/''"} {
		out, err := exec.Command(sh, "-c", "printf %s "+shellQuote(v)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != v {
			t.Fatalf("round-trip of %q gave %q", v, out)
		}
	}
}

// TestDetailForkButton pins the render gate: the detail page carries the copy
// button and the HTML-escaped command for a claude-code session, and nothing for
// an attempt with no session id or a different adapter.
func TestDetailForkButton(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	mkAttempt(t, root, "FORK", "Fork", "0001")
	writeSession(t, root, "FORK", "0001", session.Identity{Adapter: "claude-code", SessionID: "sess-42", Worktree: "/tmp/it's here"})
	mkAttempt(t, root, "NOSID", "No sid", "0001")
	writeSession(t, root, "NOSID", "0001", session.Identity{Adapter: "claude-code", Worktree: "/tmp/wt"})
	mkAttempt(t, root, "AIDER", "Aider", "0001")
	writeSession(t, root, "AIDER", "0001", session.Identity{Adapter: "aider", SessionID: "s", Worktree: "/tmp/wt"})
	mkAttempt(t, root, "FRESH", "Fresh", "0001")

	srv, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	get := func(ticket string) string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/ticket/"+ticket+"/0001", nil))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", ticket, rec.Code)
		}
		return rec.Body.String()
	}

	page := get("FORK")
	if !strings.Contains(page, `data-testid="fork-copy"`) {
		t.Fatal("claude-code attempt with a session id: no fork button")
	}
	want := html.EscapeString("draiver --data " + shellQuote(root.Dir) + " fork 'FORK@0001'")
	if !strings.Contains(page, want) {
		t.Fatalf("rendered page missing escaped command %q", want)
	}
	for _, tk := range []string{"NOSID", "AIDER", "FRESH"} {
		if strings.Contains(get(tk), `data-testid="fork-session"`) {
			t.Fatalf("%s: fork button rendered; want hidden", tk)
		}
	}
}
