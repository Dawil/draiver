package project

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// seedTicket writes a ticket's spec + one attempt's meta (with repo) + a created
// event — the minimal on-disk shape LoadAttempt and CodebaseLedgersFor read.
func seedTicket(t *testing.T, root store.Root, id, title, ticketType, att, repo string) {
	t.Helper()
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

// TestCodebaseLedgersForRepoDerived is the core binding (drv-038, resolution #4):
// a code ticket's repo resolves to the type: codebase ticket(s) sharing that repo,
// path-normalised, excluding self, with ordinary tickets on the same repo ignored.
func TestCodebaseLedgersForRepoDerived(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedTicket(t, root, "CODE-1", "A feature", "", "0001", "/repo/app")     // ordinary ticket on /repo/app
	seedTicket(t, root, "LEDGER", "App Codebase", "codebase", "0001", "/repo/app")
	seedTicket(t, root, "OTHER-LEDGER", "Other", "codebase", "0001", "/repo/elsewhere") // different repo
	seedTicket(t, root, "PLAIN", "Plain", "ticket", "0001", "/repo/app")                // explicit ordinary type

	got, err := CodebaseLedgersFor(root, "/repo/app/", "CODE-1") // trailing slash → cleaned
	if err != nil {
		t.Fatalf("CodebaseLedgersFor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d ledgers, want 1: %+v", len(got), got)
	}
	if got[0].Ticket != "LEDGER" || got[0].Title != "App Codebase" {
		t.Errorf("ledger = %+v, want {LEDGER, App Codebase}", got[0])
	}
}

// TestCodebaseLedgersForExcludesSelf: a codebase ticket briefed in its own right
// must not list itself as its own ledger.
func TestCodebaseLedgersForExcludesSelf(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedTicket(t, root, "LEDGER", "The Codebase", "codebase", "0001", "/repo/app")

	got, err := CodebaseLedgersFor(root, "/repo/app", "LEDGER")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a ledger listed itself: %+v", got)
	}
}

// TestCodebaseLedgersForMultiple: more than one match is NOT an error (the library
// framing) — every bound ledger is returned, in ListTickets (sorted) order.
func TestCodebaseLedgersForMultiple(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedTicket(t, root, "LEDGER-B", "Belief B", "codebase", "0001", "/repo/app")
	seedTicket(t, root, "LEDGER-A", "Belief A", "codebase", "0001", "/repo/app")

	got, err := CodebaseLedgersFor(root, "/repo/app", "CODE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d ledgers, want 2: %+v", len(got), got)
	}
	if got[0].Ticket != "LEDGER-A" || got[1].Ticket != "LEDGER-B" {
		t.Errorf("order = [%s, %s], want sorted [LEDGER-A, LEDGER-B]", got[0].Ticket, got[1].Ticket)
	}
}

// TestCodebaseLedgersForNoMatchAndEmptyRepo: an empty repo, or a repo nothing is
// bound to, yields nil — the brief then renders no ledger section (AC4).
func TestCodebaseLedgersForNoMatchAndEmptyRepo(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedTicket(t, root, "LEDGER", "The Codebase", "codebase", "0001", "/repo/app")

	if got, err := CodebaseLedgersFor(root, "", "CODE-1"); err != nil || got != nil {
		t.Errorf("empty repo: got (%+v, %v), want (nil, nil)", got, err)
	}
	if got, err := CodebaseLedgersFor(root, "/repo/unrelated", "CODE-1"); err != nil || got != nil {
		t.Errorf("no match: got (%+v, %v), want (nil, nil)", got, err)
	}
}

// TestCodebaseLedgersForSkipsArchived: a retired (archived) ledger is not surfaced.
func TestCodebaseLedgersForSkipsArchived(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedTicket(t, root, "LEDGER", "The Codebase", "codebase", "0001", "/repo/app")
	if _, err := ticketlog.Append(root, "LEDGER", "0001", event.Event{Type: "archive", Actor: "a"}); err != nil {
		t.Fatal(err)
	}
	got, err := CodebaseLedgersFor(root, "/repo/app", "CODE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an archived ledger was surfaced: %+v", got)
	}
}
