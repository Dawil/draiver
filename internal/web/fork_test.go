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
	cases := []struct {
		name string
		id   session.Identity
		want string
	}{
		{
			name: "absolute path",
			id:   session.Identity{Adapter: "claude-code", SessionID: "abc-123", Worktree: "/home/me/wt/0001"},
			want: `cd '/home/me/wt/0001' && claude --resume 'abc-123' --fork-session --permission-mode plan`,
		},
		{
			name: "path with spaces and a single quote",
			id:   session.Identity{Adapter: "claude-code", SessionID: "s1", Worktree: "/tmp/it's a dir"},
			want: `cd '/tmp/it'\''s a dir' && claude --resume 's1' --fork-session --permission-mode plan`,
		},
		{
			name: "trailing slash is cleaned",
			id:   session.Identity{Adapter: "claude-code", SessionID: "s1", Worktree: "/tmp/wt/"},
			want: `cd '/tmp/wt' && claude --resume 's1' --fork-session --permission-mode plan`,
		},
		{
			name: "empty session id",
			id:   session.Identity{Adapter: "claude-code", SessionID: "", Worktree: "/tmp/wt"},
			want: "",
		},
		{
			name: "blank session id",
			id:   session.Identity{Adapter: "claude-code", SessionID: "   ", Worktree: "/tmp/wt"},
			want: "",
		},
		{
			name: "relative worktree",
			id:   session.Identity{Adapter: "claude-code", SessionID: "s1", Worktree: "wt/0001"},
			want: "",
		},
		{
			name: "empty worktree",
			id:   session.Identity{Adapter: "claude-code", SessionID: "s1"},
			want: "",
		},
		{
			name: "other adapter",
			id:   session.Identity{Adapter: "aider", SessionID: "s1", Worktree: "/tmp/wt"},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forkCommand(c.id); got != c.want {
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
	cmd := forkCommand(session.Identity{Adapter: "claude-code", SessionID: "x", Worktree: "/tmp/it's"})
	if !strings.HasPrefix(cmd, `cd '/tmp/it'\''s' && `) {
		t.Fatalf("unexpected prefix: %q", cmd)
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
	want := html.EscapeString(`cd '/tmp/it'\''s here' && claude --resume 'sess-42' --fork-session --permission-mode plan`)
	if !strings.Contains(page, want) {
		t.Fatalf("rendered page missing escaped command %q", want)
	}
	for _, tk := range []string{"NOSID", "AIDER", "FRESH"} {
		if strings.Contains(get(tk), `data-testid="fork-session"`) {
			t.Fatalf("%s: fork button rendered; want hidden", tk)
		}
	}
}
