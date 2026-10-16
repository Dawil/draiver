package acceptance

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/brief"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// These steps exercise drvctl-053's wants:-branched brief over a real on-disk,
// hash-chained set of attempts — the manager P, its two children, and the
// spine-authored `woken-by` activation event P cold-starts from. brief.Build is the
// agent-facing surface `draiver brief` wraps, so asserting on its output is
// asserting on exactly what a woken coordinator reads.

const (
	mgrParent = "MGR-P"
	mgrChildA = "MGR-A"
	mgrChildB = "MGR-B"
	mgrAtt    = "0001"
)

// seedManagerData builds a throwaway data root with a coordinator P that
// `wants: [A, B]`: child A carries an open escalation (Needs me), child B reached
// Review, and P's own log holds a `woken-by A #2` activation event — the wake
// reason the brief renders as its tail.
func seedManagerData(w *World) (store.Root, error) {
	dir, err := os.MkdirTemp("", "acc-mgr-")
	if err != nil {
		return store.Root{}, err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}

	for _, c := range []struct{ id, title string }{{mgrChildA, "Child A"}, {mgrChildB, "Child B"}} {
		if err := root.EnsureAttemptDirs(c.id, mgrAtt); err != nil {
			return store.Root{}, err
		}
		spec := "---\nid: " + c.id + "\ntitle: " + c.title + "\n---\n\n# " + c.title + "\n"
		if err := os.WriteFile(root.SpecPath(c.id), []byte(spec), 0o644); err != nil {
			return store.Root{}, err
		}
	}
	// Child A: an open escalation → Needs me.
	if _, err := seedAppend(root, mgrChildA, event.Event{Type: "created", Body: "start"}); err != nil {
		return store.Root{}, err
	}
	if _, err := seedAppend(root, mgrChildA, event.Event{Type: "escalation", Body: "which endpoint should the worker call?"}); err != nil {
		return store.Root{}, err
	}
	// Child B: a review claim → Review.
	if _, err := seedAppend(root, mgrChildB, event.Event{Type: "created", Body: "start"}); err != nil {
		return store.Root{}, err
	}
	if _, err := seedAppend(root, mgrChildB, event.Event{Type: "review", Body: "claim: done, see PR"}); err != nil {
		return store.Root{}, err
	}

	// The coordinator P wants: [A, B].
	if err := root.EnsureAttemptDirs(mgrParent, mgrAtt); err != nil {
		return store.Root{}, err
	}
	spec := "---\nid: " + mgrParent + "\ntitle: Integrate the parts\nwants:\n  - " + mgrChildA + "\n  - " + mgrChildB + "\n---\n\n# Integrate\n\nMake the pieces fit together."
	if err := os.WriteFile(root.SpecPath(mgrParent), []byte(spec), 0o644); err != nil {
		return store.Root{}, err
	}
	if _, err := seedAppend(root, mgrParent, event.Event{Type: "created", Body: "manager start"}); err != nil {
		return store.Root{}, err
	}
	if _, err := seedAppend(root, mgrParent, event.Event{Type: "enable", Body: "supervised"}); err != nil {
		return store.Root{}, err
	}
	// The spine's activation event on P's own log — authored at wake time.
	if _, err := seedAppend(root, mgrParent, event.Event{
		Type: "woken-by", WokenChild: mgrChildA, WokenSeq: 2,
		Body: "Woken by `" + mgrChildA + "` #2 — a child `escalation` lifecycle doorbell.",
	}); err != nil {
		return store.Root{}, err
	}
	return root, nil
}

func seedAppend(root store.Root, ticket string, e event.Event) (event.Event, error) {
	e.Actor = "agent:acceptance"
	return ticketlog.Append(root, ticket, mgrAtt, e)
}

func givenCoordinatorWokenByChild(w *World, sr *StepRun) error {
	root, err := seedManagerData(w)
	if err != nil {
		return err
	}
	out, err := brief.Build(root, mgrParent, mgrAtt)
	if err != nil {
		return err
	}
	w.set("mgrBrief", out)
	attachText(sr, "manager-brief", "$ draiver brief "+mgrParent+"\n\n"+out)
	return nil
}

func thenBriefListsTeam(w *World, sr *StepRun) error {
	out := w.get("mgrBrief").(string)
	for _, want := range []string{
		"## Your team",
		"`MGR-A`/0001 — **Needs me**",
		"#2: which endpoint should the worker call?",
		"`MGR-B`/0001 — **Review**",
	} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("manager brief missing team line %q\n%s", want, out)
		}
	}
	return nil
}

func thenBriefInjectsProtocol(w *World, sr *StepRun) error {
	out := w.get("mgrBrief").(string)
	for _, want := range []string{"## Coordinator protocol", "charter", "draiver park"} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("manager brief missing protocol marker %q\n%s", want, out)
		}
	}
	return nil
}

func thenWakeReasonIsTail(w *World, sr *StepRun) error {
	out := w.get("mgrBrief").(string)
	wake := strings.Index(out, "You were woken for this wake:")
	if wake < 0 {
		return fmt.Errorf("manager brief has no wake-reason tail\n%s", out)
	}
	if !strings.Contains(out, "Woken by `MGR-A` #2") {
		return fmt.Errorf("wake reason does not name the activating doorbell\n%s", out)
	}
	// The tail is the freshest line: nothing of substance follows it, and it comes
	// after the recall hint.
	if hint := strings.Index(out, "draiver recall <seq>"); hint < 0 || wake < hint {
		return fmt.Errorf("wake reason must be the brief's tail, after the recall hint\n%s", out)
	}
	return nil
}

func givenPlainWorkerAttempt(w *World, sr *StepRun) error {
	dir, err := os.MkdirTemp("", "acc-worker-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}
	const id = "WRK-1"
	if err := root.EnsureAttemptDirs(id, mgrAtt); err != nil {
		return err
	}
	spec := "---\nid: " + id + "\ntitle: Do the work\n---\n\n# Do the work\n\nImplement the spec."
	if err := os.WriteFile(root.SpecPath(id), []byte(spec), 0o644); err != nil {
		return err
	}
	if _, err := ticketlog.Append(root, id, mgrAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return err
	}
	out, err := brief.Build(root, id, mgrAtt)
	if err != nil {
		return err
	}
	w.set("workerBrief", out)
	attachText(sr, "worker-brief", "$ draiver brief "+id+"\n\n"+out)
	return nil
}

func thenNoManagerInjection(w *World, sr *StepRun) error {
	out := w.get("workerBrief").(string)
	for _, gone := range []string{"## Your team", "## Coordinator protocol", "You were woken"} {
		if strings.Contains(out, gone) {
			return fmt.Errorf("worker brief leaked manager injection %q\n%s", gone, out)
		}
	}
	// The per-ticket half still renders.
	if !strings.Contains(out, "Implement the spec.") {
		return fmt.Errorf("worker brief lost its spec body\n%s", out)
	}
	return nil
}
