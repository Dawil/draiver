package canary

import (
	"testing"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
)

// TestAdapterVersionReadsSessionJSON locks the drvctl-033 loader seam: the
// pinned adapter version recorded in session.json is what feeds the canary's
// attribution. A never-run attempt (no session.json) degrades to "" —
// "unattributable" — rather than erroring.
func TestAdapterVersionReadsSessionJSON(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	ticket := "PROJ-1"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Repo: "/repo", Actor: "agent:x"})
	if err != nil {
		t.Fatal(err)
	}

	// No session.json yet → unattributable, never an error.
	if v := adapterVersion(root, ticket, m.ID); v != "" {
		t.Fatalf("no session.json: adapterVersion=%q, want empty", v)
	}

	s, err := session.Open(root, ticket, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.WriteIdentity(session.Identity{
		Adapter:        "claude-code",
		AdapterVersion: "2.1.216",
		SessionID:      "sess-1",
	}); err != nil {
		t.Fatal(err)
	}

	if v := adapterVersion(root, ticket, m.ID); v != "2.1.216" {
		t.Fatalf("adapterVersion=%q, want %q", v, "2.1.216")
	}
}
