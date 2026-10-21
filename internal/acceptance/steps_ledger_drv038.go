package acceptance

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/brief"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/handbook"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// These steps exercise drv-038 — the consumption side of the codebase-ledger epic
// — end to end: the cold-start brief surfaces the repo's bound ledger(s) as a
// library pointer (repo-derived binding), a repo with no ledger is unchanged, the
// write address form lets an agent record findings into its own scoped attempt on
// the ledger, and the handbook teaches the ticket-local vs codebase-level rule.

const cbRepo = "/repo/app"

// givenCodeTicketWithLedger stands up a code ticket and a type:codebase ledger that
// share a repo (plus a decoy ledger on a different repo), so the brief must bind
// the matching one and only that one.
func givenCodeTicketWithLedger(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "CODE-1", "")
	if err != nil {
		return err
	}
	// seedTypedTicket bound CODE-1 to its temp dir; rewrite it onto the shared cbRepo
	// so the repo-derived binding has a deterministic repo to match on.
	if err := addTypedTicket(root, cbRepo, "CODE-1", ""); err != nil {
		return err
	}
	if err := addTypedTicket(root, cbRepo, "APP-CB", "type: codebase\n"); err != nil {
		return err
	}
	if err := addTypedTicket(root, "/repo/other", "OTHER-CB", "type: codebase\n"); err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "CODE-1")
	return nil
}

func thenBriefSurfacesOnlyBoundLedger(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	out, err := brief.Build(root, w.get("ticket").(string), cbAtt)
	if err != nil {
		return err
	}
	for _, want := range []string{"## Codebase ledger for this repo", "`APP-CB`", "draiver recall <ledger>"} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("brief missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "OTHER-CB") {
		return fmt.Errorf("brief surfaced a ledger bound to a different repo:\n%s", out)
	}
	attachText(sr, "brief-ledger-pointer", out)
	return nil
}

// givenCodeTicketNoLedger stands up a lone code ticket whose repo binds no ledger.
func givenCodeTicketNoLedger(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "CODE-1", "")
	if err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "CODE-1")
	return nil
}

func thenBriefHasNoLedgerSection(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	out, err := brief.Build(root, w.get("ticket").(string), cbAtt)
	if err != nil {
		return err
	}
	if strings.Contains(out, "Codebase ledger") {
		return fmt.Errorf("brief rendered a ledger section for a repo with no ledger bound:\n%s", out)
	}
	attachText(sr, "brief-no-ledger", "no 'Codebase ledger' section — cold-start unchanged")
	return nil
}

// givenLedgerWithSecondPerspective stands up a ledger and opens a second attempt on
// it — the sovereign, scoped perspective an agent records findings into.
func givenLedgerWithSecondPerspective(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "APP-CB", "type: codebase\n")
	if err != nil {
		return err
	}
	// A second attempt "0002" is the contributing perspective (its own log).
	const persp = "0002"
	if err := root.EnsureAttemptDirs("APP-CB", persp); err != nil {
		return err
	}
	meta := "---\nid: " + persp + "\nticket: APP-CB\nrepo: " + cbRepo + "\nbase: main\n---\n"
	if err := os.WriteFile(root.AttemptMetaPath("APP-CB", persp), []byte(meta), 0o644); err != nil {
		return err
	}
	if _, err := ticketlog.Append(root, "APP-CB", persp, event.Event{Type: "created", Actor: "agent:code-1", Body: "scoped perspective from CODE-1"}); err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

func thenFindingLandsOnThatPerspective(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	const persp = "0002"
	// Record a codebase-level finding onto the scoped perspective, tagged.
	if _, err := ticketlog.Append(root, "APP-CB", persp, event.Event{
		Type: "gotcha", Actor: "agent:code-1", Tags: []string{"build"},
		Body: "The shared build caches node_modules under a fixed path.",
	}); err != nil {
		return err
	}
	// The defining attempt 0001 must be untouched; the finding is scoped to 0002.
	ev1, err := ticketlog.Read(root, "APP-CB", "0001")
	if err != nil {
		return err
	}
	if len(ev1) != 1 {
		return fmt.Errorf("defining attempt 0001 should be untouched, has %d events", len(ev1))
	}
	a2, err := project.LoadAttempt(root, "APP-CB", persp)
	if err != nil {
		return err
	}
	last := a2.Events[len(a2.Events)-1]
	if last.Type != "gotcha" || len(last.Tags) != 1 || last.Tags[0] != "build" {
		return fmt.Errorf("finding did not land tagged on the perspective: %+v", last)
	}
	attachText(sr, "scoped-perspective", "finding on APP-CB@0002 (tag: build); defining attempt 0001 untouched")
	return nil
}

// thenHandbookTeachesLedger asserts the invariant protocol teaches both sides of
// the epic: consult the ledger at cold-start, and externalise codebase-level (vs
// ticket-local) findings into it.
func thenHandbookTeachesLedger(w *World, sr *StepRun) error {
	c := handbook.Content()
	for _, want := range []string{
		"codebase ledger",
		"Externalise codebase-level findings",
		"would this still matter to someone who never touches your ticket",
		"draiver attempt new <LEDGER>",
	} {
		if !strings.Contains(c, want) {
			return fmt.Errorf("handbook missing the codebase-ledger guidance %q", want)
		}
	}
	attachText(sr, "handbook-ledger-rule", "handbook teaches read-at-cold-start + the ticket-local vs codebase-level write rule")
	return nil
}
