package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
)

// The review gate's refusal diagnosis (drv-063). A bare "rung X not green" sends the
// agent hunting: which test failed, and is it even theirs? For each outstanding
// required rung this names the failing tests (from the latest FAILING test-result a
// detached climb recorded, which carries failed_rung/failed_tests) and says whether
// the rung was already red at the merge-base with the attempt's base branch —
// i.e. inherited from main rather than introduced by this branch — judged from the
// results any attempt on the board recorded at exactly that commit.

// mergeBaseState is what the board's logs say about a rung at the merge-base commit.
type mergeBaseState int

const (
	baseUnknown mergeBaseState = iota // no attempt recorded a result at the merge-base
	baseRed                           // a failing result at the merge-base names this rung
	baseGreen                         // a passing result at the merge-base covers this rung
)

// baseEvidence is the board-wide test-result record at one commit.
type baseEvidence struct {
	state mergeBaseState
	tests []string // failing tests at the merge-base (baseRed)
	where string   // "#<seq> on <ticket>/<attempt>"
}

// refusalDiagnosis renders the per-rung lines appended to a refusal. head is the
// attempt's HEAD, mergeBase the merge-base with base ("" when it cannot be found),
// all every attempt on the board (for the merge-base lookup).
func refusalDiagnosis(p *pyramid.Pyramid, outstanding []pyramid.Level, own []event.Event, head, base, mergeBase string, all []project.Attempt) string {
	var b strings.Builder
	fail, failAtHead := latestFailure(own, head)
	for _, lv := range outstanding {
		fmt.Fprintf(&b, "\n  - rung %q: ", lv.Name)
		switch {
		case fail != nil && failedRungOf(*fail) == lv.Name:
			if len(fail.FailedTests) > 0 {
				fmt.Fprintf(&b, "failing %s", strings.Join(fail.FailedTests, ", "))
			} else {
				b.WriteString("red (no failing test could be named — see the console log)")
			}
			fmt.Fprintf(&b, " (test-result #%d @ %s", fail.Seq, shortSHA(fail.Commit))
			if !failAtHead {
				b.WriteString(", an earlier commit than HEAD")
			}
			b.WriteString(")")
		case fail != nil && failAtHead && failedRungOf(*fail) != "" && rungIndex(p, failedRungOf(*fail)) < rungIndex(p, lv.Name):
			fmt.Fprintf(&b, "not reached — the climb halted at red rung %q", failedRungOf(*fail))
			// Nothing to say about the merge-base for a rung that never ran.
			continue
		default:
			b.WriteString("no failing result recorded for it (a synchronous `draiver test` records nothing on red; `draiver test --detach` records which test fails)")
		}
		b.WriteString("\n    ")
		b.WriteString(mergeBaseLine(p, lv, base, mergeBase, all))
	}
	return b.String()
}

// mergeBaseLine says whether lv was already red at the merge-base with base.
func mergeBaseLine(p *pyramid.Pyramid, lv pyramid.Level, base, mergeBase string, all []project.Attempt) string {
	if mergeBase == "" {
		return fmt.Sprintf("merge-base with %s: could not be resolved, so whether this is inherited is unknown", base)
	}
	ev := evidenceAt(p, lv.Name, mergeBase, all)
	at := fmt.Sprintf("at the merge-base %s with %s", shortSHA(mergeBase), base)
	switch ev.state {
	case baseRed:
		tests := ""
		if len(ev.tests) > 0 {
			tests = " (failing " + strings.Join(ev.tests, ", ") + ")"
		}
		return fmt.Sprintf("%s: already red%s — test-result %s. Inherited from %s, not introduced by this branch.", at, tests, ev.where, base)
	case baseGreen:
		return fmt.Sprintf("%s: green — test-result %s. This branch introduced the failure.", at, ev.where)
	default:
		return fmt.Sprintf("%s: no result recorded, so whether it is inherited is unknown — check with `git worktree add --detach /tmp/mb %s && cd /tmp/mb && %s`.", at, shortSHA(mergeBase), lv.Run)
	}
}

// evidenceAt folds every attempt's test-results at commit into what they prove about
// rung: a failing result whose failed rung is rung → red; a passing result at rung or
// any higher rung → green (a logged green at rung j means every required rung ≤ j
// passed). Red wins over green: a recorded failure is the more specific evidence.
func evidenceAt(p *pyramid.Pyramid, rung, commit string, all []project.Attempt) baseEvidence {
	var green *baseEvidence
	want := rungIndex(p, rung)
	for _, a := range all {
		for _, e := range a.Events {
			if e.Type != "test-result" || e.Commit == "" || !sameCommit(e.Commit, commit) {
				continue
			}
			where := fmt.Sprintf("#%d on %s/%s", e.Seq, a.Ticket, a.ID)
			if e.Outcome == "fail" {
				if failedRungOf(e) == rung {
					return baseEvidence{state: baseRed, tests: e.FailedTests, where: where}
				}
				continue
			}
			if green == nil && want >= 0 && rungIndex(p, e.Rung) >= want {
				green = &baseEvidence{state: baseGreen, where: where}
			}
		}
	}
	if green != nil {
		return *green
	}
	return baseEvidence{state: baseUnknown}
}

// latestFailure returns the attempt's most recent failing test-result, preferring one
// at head; atHead reports whether it is at head.
func latestFailure(events []event.Event, head string) (*event.Event, bool) {
	var latest, latestAtHead *event.Event
	for i := range events {
		e := &events[i]
		if e.Type != "test-result" || e.Outcome != "fail" {
			continue
		}
		latest = e
		if sameCommit(e.Commit, head) {
			latestAtHead = e
		}
	}
	if latestAtHead != nil {
		return latestAtHead, true
	}
	return latest, false
}

// failedRungOf is the rung a failing result halted at: failed_rung when recorded
// (drv-063), else the legacy result's own rung.
func failedRungOf(e event.Event) string {
	if e.FailedRung != "" {
		return e.FailedRung
	}
	return e.Rung
}

func rungIndex(p *pyramid.Pyramid, name string) int {
	for i, lv := range p.Levels {
		if lv.Name == name {
			return i
		}
	}
	return -1
}

func sameCommit(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// gitMergeBase resolves the merge-base of HEAD with base in dir, "" when it cannot
// (no such branch, unrelated history). Best-effort: the diagnosis only ever adds
// detail to a refusal and must never turn into one.
func gitMergeBase(ctx context.Context, dir, base string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "HEAD", base).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
