package brief

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// seedForLedger writes a ticket's spec + one attempt's meta (carrying repo) + a
// created event — enough for Build/LoadAttempt to resolve its repo and type.
func seedForLedger(t *testing.T, root store.Root, id, title, ticketType, repo string) {
	t.Helper()
	const att = "0001"
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	typeLine := ""
	if ticketType != "" {
		typeLine = "type: " + ticketType + "\n"
	}
	spec := fmt.Sprintf("---\nid: %s\ntitle: %s\n%s---\n\n# %s\n", id, title, typeLine, title)
	if err := os.WriteFile(root.SpecPath(id), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf("---\nid: %s\nticket: %s\nrepo: %s\nbase: main\n---\n", att, id, repo)
	if err := os.WriteFile(root.AttemptMetaPath(id, att), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "a"}); err != nil {
		t.Fatal(err)
	}
}

// TestBuildSurfacesCodebaseLedger (drv-038 AC1): a code ticket's brief surfaces
// the codebase ledger bound to its repo as a pointer + recall instruction, and
// only that ledger.
func TestBuildSurfacesCodebaseLedger(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedForLedger(t, root, "PROJ-1", "Wire auth", "", "/repo/app")
	seedForLedger(t, root, "APP-CB", "App Codebase", "codebase", "/repo/app")
	seedForLedger(t, root, "OTHER-CB", "Other Codebase", "codebase", "/repo/elsewhere")

	out, err := Build(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Codebase ledger for this repo",
		"`APP-CB` — App Codebase",
		"draiver recall <ledger>",
		"Externalise codebase-level findings", // points at the handbook write rule
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief missing %q\n---\n%s", want, out)
		}
	}
	// Only the repo-matched ledger — not a ledger bound to a different repo.
	if strings.Contains(out, "OTHER-CB") {
		t.Errorf("brief surfaced a ledger bound to a different repo:\n%s", out)
	}
}

// TestBuildNoLedgerSection (drv-038 AC3/AC4): a repo with no codebase ledger bound
// gets no ledger section at all — cold-start is unchanged.
func TestBuildNoLedgerSection(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedForLedger(t, root, "PROJ-1", "Wire auth", "", "/repo/app")

	out, err := Build(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Codebase ledger") {
		t.Errorf("brief rendered a ledger section for a repo with no ledger:\n%s", out)
	}
}
