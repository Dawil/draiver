package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/Dawil/draiver/internal/protocol"
)

// These steps exercise drvctl-056's agent-facing half over the real protocol
// engine: the driving turn a *resumed* session is given. Priming that turn before
// confirming online is what brings a headless `claude --resume` process up at all
// (it emits no system/init until it receives a user turn), and on an async wake
// the turn carries the reason it was woken so the agent acts on the landed result
// without re-deriving its own just-done state. The bring-up ordering and the
// context-headroom gate that chooses resume-vs-cold-start are reconciler-internal
// and covered at the unit rung (internal/reconcile), mirroring how drvctl-055
// keeps its `woken-by` re-admit at the unit rung.

// capturePrompter records the single driving turn injected into a session — the
// protocol.Prompter the reconcile loop satisfies with a live manage.Handle.
type capturePrompter struct{ got string }

func (c *capturePrompter) Prompt(ctx context.Context, text string) error {
	c.got = text
	return nil
}

const resumeTicket = "RES-1"

// ---- a resumed session is nudged to continue, not re-briefed ----

func givenResumedSessionDriven(w *World, sr *StepRun) error {
	p := &capturePrompter{}
	// No wake note: the plain continuation nudge a resumed session receives in place
	// of the full cold-start brief (the drvctl-022 brief-vs-nudge split).
	if err := protocol.InjectResumeNudge(context.Background(), resumeTicket, "", p); err != nil {
		return err
	}
	w.set("nudge", p.got)
	return nil
}

func thenNudgedNotBriefed(w *World, sr *StepRun) error {
	nudge := w.get("nudge").(string)
	if strings.Contains(nudge, "cold-starting a draiver attempt") || strings.Contains(nudge, "BRIEF") {
		return fmt.Errorf("a resume must be nudged, not re-briefed, but the turn is a cold-start brief:\n%s", nudge)
	}
	for _, want := range []string{"resuming", "draiver brief " + resumeTicket} {
		if !strings.Contains(nudge, want) {
			return fmt.Errorf("resume nudge missing %q:\n%s", want, nudge)
		}
	}
	attachText(sr, "resume-nudge",
		"# a resumed session keeps its context, so the driving turn continues the work\n"+
			"# rather than re-dumping the brief (drvctl-016/drvctl-022):\n"+nudge)
	return nil
}

// ---- an async wake carries the reason it was woken ----

func givenResumedByAsyncResult(w *World, sr *StepRun) error {
	p := &capturePrompter{}
	// The wake note the reconciler composes when a detached test's result lands on
	// the worker's own log (drvctl-056): it names the result so the resumed agent
	// reads the verdict and hands off, instead of cold-starting to rederive it.
	wakeNote := "You were woken because the detached `draiver test` you launched has landed its verdict: `test-result` #7 is now on your log. Read it with `draiver recall " + resumeTicket + " 7`, then claim `review` if it is green or iterate if it is red."
	if err := protocol.InjectResumeNudge(context.Background(), resumeTicket, wakeNote, p); err != nil {
		return err
	}
	w.set("nudge", p.got)
	return nil
}

func thenWakeNudgeNamesResult(w *World, sr *StepRun) error {
	nudge := w.get("nudge").(string)
	if strings.Contains(nudge, "BRIEF") {
		return fmt.Errorf("an async wake must resume (nudge), not cold-start (brief):\n%s", nudge)
	}
	// The composed turn carries BOTH the continuation guidance and the wake reason.
	for _, want := range []string{"resuming", "test-result", "#7", "review"} {
		if !strings.Contains(nudge, want) {
			return fmt.Errorf("async wake nudge missing %q:\n%s", want, nudge)
		}
	}
	attachText(sr, "async-wake-nudge",
		"# the resume nudge delivered on an async wake names the landed result, so the\n"+
			"# agent reads the verdict and hands off with no recall round-trip:\n"+nudge)
	return nil
}

// ---- a decision-reopen resume names the unseen decision (drvctl-058) ----

func givenResumedAfterDecisionReopen(w *World, sr *StepRun) error {
	p := &capturePrompter{}
	// The generalised "what changed while you were parked" note (drvctl-058): a human
	// added a `decision` *after* the session claimed review, so the restored context
	// predates it. The note names the unseen decision by seq + headline and tells the
	// agent to read and act on it before handing off — the watermark derivation that
	// produces it is pinned at the unit rung (internal/reconcile), mirroring the async
	// wake's split; here we pin the agent-facing turn the resume actually delivers.
	wakeNote := "You were resumed, but authoritative events landed on your log after the point your restored context ends — you have not seen them:\n" +
		"- `decision` #12 — Pull, resolve conflicts, and re-raise the PR\n\n" +
		"Read each with `draiver recall " + resumeTicket + " <seq>` (or re-ground with `draiver brief " + resumeTicket + "`), act on it, and only then claim `review` or `escalate`. Do not re-claim review with stale context."
	if err := protocol.InjectResumeNudge(context.Background(), resumeTicket, wakeNote, p); err != nil {
		return err
	}
	w.set("nudge", p.got)
	return nil
}

func thenReopenNudgeNamesDecision(w *World, sr *StepRun) error {
	nudge := w.get("nudge").(string)
	if strings.Contains(nudge, "BRIEF") {
		return fmt.Errorf("a decision-reopen must resume (nudge), not cold-start (brief):\n%s", nudge)
	}
	// The composed turn continues the work AND names the unseen decision, pointing the
	// agent at recall so it reads and acts before re-claiming review.
	for _, want := range []string{"resuming", "decision", "#12", "recall"} {
		if !strings.Contains(nudge, want) {
			return fmt.Errorf("decision-reopen nudge missing %q:\n%s", want, nudge)
		}
	}
	attachText(sr, "decision-reopen-nudge",
		"# a Review-then-reopened attempt resumes into context that predates the human\n"+
			"# decision; the delivered turn names it so the agent reads and acts before\n"+
			"# re-claiming review, instead of silently re-claiming with stale context:\n"+nudge)
	return nil
}
