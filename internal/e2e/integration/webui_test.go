//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestWebuiBoardFromSeededVolume drives the real `draiver webui` binary, served
// over HTTP against reproducibly-seeded data-root "volumes", and asserts the
// board renders each named board state correctly. This is the read path exercised
// end to end — the served binary reading straight from a seeded data root with no
// control plane — rather than the in-process handler tests in internal/web.
//
// No container is needed: the board is entirely server-rendered HTML reachable by
// plain GET, so these scenarios run even on a host without Docker (only the forge
// tests gate on RequireContainerRuntime).
func TestWebuiBoardFromSeededVolume(t *testing.T) {
	// One card per attempt + column tallies: the mid-escalation volume has one
	// ticket with two attempts — 0001 blocked on an open escalation (→ Stuck) and
	// 0002 freshly running (→ Running) — so the same ticket must appear twice and
	// the Stuck/Running columns must each tally one.
	t.Run("mid-escalation shows one card per attempt and correct tallies", func(t *testing.T) {
		d := NewDataRoot(t, MidEscalation)
		w := ServeWebui(t, d)
		body := w.GetOK("/")

		for _, want := range []string{
			`data-testid="board"`,
			`data-testid="col-running"`,
			`data-testid="col-pending"`,
			`data-testid="col-stuck"`, // the real testid is col-stuck, not the spec's col-needs-me
			`data-testid="col-review"`,
			`data-testid="col-done"`,
			// The same ticket appears once per attempt — two distinct cards.
			`data-testid="card-PROJ-101-0001"`,
			`data-testid="card-PROJ-101-0002"`,
			// 0001 is blocked on an open escalation → Stuck; 0002 is Running.
			`data-testid="count-stuck">1<`,
			`data-testid="count-running">1<`,
			// The blocked attempt surfaces its open-escalation badge.
			`1 open`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("board missing %q\n\n%s", want, body)
			}
		}
	})

	// The review-ready volume has one ticket claimed for review, so the Review
	// column tallies one and carries that attempt's card.
	t.Run("review-ready volume populates the Review column", func(t *testing.T) {
		d := NewDataRoot(t, ReviewReadyToMerge)
		w := ServeWebui(t, d)
		body := w.GetOK("/")

		for _, want := range []string{
			`data-testid="count-review">1<`,
			`data-testid="card-PROJ-102-0001"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("review board missing %q\n\n%s", want, body)
			}
		}
	})

	// The empty volume is the cold-start state: every column renders with a zero
	// tally and no cards, so the read path is robust to an empty board.
	t.Run("empty volume renders every column at zero", func(t *testing.T) {
		d := NewDataRoot(t, EmptyBoard)
		w := ServeWebui(t, d)
		body := w.GetOK("/")

		for _, want := range []string{
			`data-testid="count-running">0<`,
			`data-testid="count-stuck">0<`,
			`data-testid="count-review">0<`,
			`data-testid="count-done">0<`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("empty board missing %q\n\n%s", want, body)
			}
		}
	})
}

// TestWebuiLogBodyMarkdownSanitized pins the XSS guard on the served binary: a
// log body is agent-authored input, so its Markdown structure must render while
// raw HTML and dangerous link schemes are neutralised. internal/web proves this
// for the in-process handler (TestLogBodyMarkdownRenderedAndSanitized); here we
// prove the same contract holds for the real served binary reading a seeded
// volume — the path an operator actually runs. No container needed.
func TestWebuiLogBodyMarkdownSanitized(t *testing.T) {
	d := NewDraiver(t)

	// Seed a bespoke volume with a decision log that mixes safe Markdown with an
	// XSS payload an agent might write. Args are passed as an argv slice (no shell),
	// so the payload reaches the log body verbatim.
	d.Run("new", "XSS-1", "--title", "Sanitize me", "--repo", NewGitRepo(t, SeedSingleCommit).Dir)
	d.Run("log", "XSS-1",
		"Chose **server-side** paging; see `pager.go`. "+
			"<script>alert(1)</script> [x](javascript:alert(1))",
		"--type", "decision")

	w := ServeWebui(t, d.DataRootHandle())
	body := w.GetOK("/ticket/XSS-1/0001")

	// Markdown structure is rendered to HTML.
	for _, want := range []string{"<strong>server-side</strong>", "<code>pager.go</code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected rendered markdown %q in served log body", want)
		}
	}
	// The XSS payload is neutralised: the raw <script> tag is dropped and the
	// javascript: link destination is blanked. Check the exact payloads so the
	// assertion can't collide with the page's own legitimate <script> chrome.
	for _, bad := range []string{"<script>alert(1)", "javascript:alert(1)"} {
		if strings.Contains(body, bad) {
			t.Errorf("unsanitised %q leaked into the served log body", bad)
		}
	}
}
