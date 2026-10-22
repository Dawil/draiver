package acceptance

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/audit"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// These steps exercise drv-040's --name over the real attempt-creation chokepoint
// (attempt.Create) and the same on-disk primitives the CLI wraps — ticketlog for
// the @-address log/recall resolve to, audit for the hash chain — so asserting on
// them is asserting on the agent-facing surface, not a mock.

const namedTicket = "LEDGER-1"

// seedNamedTicket makes a throwaway data root with namedTicket present (spec, but
// no attempts yet), registered for cleanup on the World, and returns it. Its data
// dir doubles as the required repo path, exactly as the CLI tests do.
func seedNamedTicket(w *World) (store.Root, error) {
	dir, err := os.MkdirTemp("", "acc-named-")
	if err != nil {
		return store.Root{}, err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}
	if err := root.EnsureTicketDir(namedTicket); err != nil {
		return store.Root{}, err
	}
	spec := "---\nid: " + namedTicket + "\ntitle: named attempts\n---\n\n# named attempts\n"
	if err := os.WriteFile(root.SpecPath(namedTicket), []byte(spec), 0o644); err != nil {
		return store.Root{}, err
	}
	return root, nil
}

func givenBareTicket(w *World, sr *StepRun) error {
	root, err := seedNamedTicket(w)
	if err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

func givenTicketWithNamedAttempt(w *World, sr *StepRun) error {
	root, err := seedNamedTicket(w)
	if err != nil {
		return err
	}
	if _, err := attempt.Create(root, namedTicket, attempt.New{Repo: root.Dir, Actor: "agent:acceptance", Name: "repo-facts"}); err != nil {
		return fmt.Errorf("seed named attempt: %w", err)
	}
	w.set("root", root)
	return nil
}

// thenNamedAttemptAddressable: --name uses the slug verbatim as the id, the attempt
// is reachable at the <TICKET>@<slug> address the shared parser resolves (a fact
// logged there reads back from that attempt's log), and its hash chain audits clean.
func thenNamedAttemptAddressable(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	const slug = "repo-facts"
	m, err := attempt.Create(root, namedTicket, attempt.New{Repo: root.Dir, Actor: "agent:acceptance", Name: slug})
	if err != nil {
		return fmt.Errorf("create named attempt: %w", err)
	}
	if m.ID != slug {
		return fmt.Errorf("named attempt id = %q, want %q", m.ID, slug)
	}
	if !root.AttemptExists(namedTicket, slug) {
		return fmt.Errorf("attempt dir %s@%s not created", namedTicket, slug)
	}
	// The @-address form resolves to this attempt: a fact logged to the slug reads
	// back from exactly that attempt's hash-chained log and no other.
	if _, err := ticketlog.Append(root, namedTicket, slug, event.Event{Type: "note", Actor: "agent:acceptance", Body: "a fact on the named perspective"}); err != nil {
		return err
	}
	events, err := ticketlog.Read(root, namedTicket, slug)
	if err != nil {
		return err
	}
	f, ok := kb.Fold(events).Get(2)
	if !ok || !strings.Contains(f.Body, "named perspective") {
		return fmt.Errorf("recall %s@%s did not surface the logged fact: %+v", namedTicket, slug, f)
	}
	res, err := audit.VerifyAttempt(root, namedTicket, slug)
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("audit failed on named attempt: %s", res.Reason)
	}
	attachText(sr, "named-attempt-addressable",
		"$ draiver attempt new "+namedTicket+" --name "+slug+" --repo <path>\n"+
			"started "+namedTicket+" attempt "+slug+"\n\n"+
			"$ draiver log "+namedTicket+"@"+slug+" --type note \"...\"   # resolves to the named attempt\n"+
			"$ draiver recall "+namedTicket+"@"+slug+"\n"+recallIndexLines(kb.Fold(events).Live())+"\n"+
			"$ draiver audit "+namedTicket+" --attempt "+slug+"\nOK\n")
	return nil
}

// thenUnnamedStillAutoNumbers: a non-numeric slug does not participate in nextID's
// max, so the first unnamed attempt beside it is still 0001 — AC "no behaviour
// change when --name is omitted".
func thenUnnamedStillAutoNumbers(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	m, err := attempt.Create(root, namedTicket, attempt.New{Repo: root.Dir, Actor: "agent:acceptance"})
	if err != nil {
		return err
	}
	if m.ID != "0001" {
		return fmt.Errorf("unnamed attempt id = %q, want 0001 (a slug must not perturb auto-numbering)", m.ID)
	}
	ids, _ := attempt.List(root, namedTicket)
	attachText(sr, "named-attempt-autonumber",
		"# a named attempt already exists:\n$ draiver attempt ls "+namedTicket+"\n"+strings.Join(ids, "\n")+"\n\n"+
			"$ draiver attempt new "+namedTicket+" --repo <path>   # no --name\n"+
			"started "+namedTicket+" attempt "+m.ID+"   # still auto-numbered, unperturbed by the slug\n")
	return nil
}

// thenCollidingNameRefused: a --name equal to an existing attempt id is a hard
// error (not a bump to the next number), and the rejected call writes nothing —
// the existing attempt keeps its provenance.
func thenCollidingNameRefused(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	before, _ := attempt.List(root, namedTicket)
	if _, err := attempt.Create(root, namedTicket, attempt.New{Repo: "/other", Actor: "agent:acceptance", Name: "repo-facts"}); err == nil {
		return fmt.Errorf("expected a collision error re-creating repo-facts")
	}
	after, _ := attempt.List(root, namedTicket)
	if len(after) != len(before) {
		return fmt.Errorf("a refused collision wrote an attempt: %v -> %v", before, after)
	}
	if m, _ := attempt.LoadMeta(root, namedTicket, "repo-facts"); m.Repo == "/other" {
		return fmt.Errorf("a refused collision clobbered the existing attempt's repo")
	}
	attachText(sr, "named-attempt-collision",
		"# repo-facts already exists:\n"+
			"$ draiver attempt new "+namedTicket+" --name repo-facts --repo /other\n"+
			"Error: attempt: "+namedTicket+"@repo-facts already exists\n"+
			"# nothing written; the existing attempt is untouched\n")
	return nil
}

// thenUnsafeNameRefused: a slug containing a path separator, an '@', or whitespace
// — anything that would break the on-disk layout or the @-address parsing — is
// refused before anything is written.
func thenUnsafeNameRefused(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	for _, bad := range []string{"a/b", "a@b", "a b"} {
		if _, err := attempt.Create(root, namedTicket, attempt.New{Repo: root.Dir, Actor: "agent:acceptance", Name: bad}); err == nil {
			return fmt.Errorf("attempt new --name %q succeeded, want rejection", bad)
		}
	}
	if ids, _ := attempt.List(root, namedTicket); len(ids) != 0 {
		return fmt.Errorf("a rejected unsafe name left an attempt behind: %v", ids)
	}
	attachText(sr, "named-attempt-unsafe",
		"$ draiver attempt new "+namedTicket+" --name a/b --repo <path>\n"+
			"Error: attempt name \"a/b\" must not contain a path separator\n"+
			"$ draiver attempt new "+namedTicket+" --name a@b --repo <path>\n"+
			"Error: attempt name \"a@b\" must not contain '@' (reserved for the ticket@attempt address form)\n"+
			"$ draiver attempt new "+namedTicket+" --name 'a b' --repo <path>\n"+
			"Error: attempt name \"a b\" must not contain whitespace\n"+
			"# each refused before any directory is created\n")
	return nil
}
