package cmd

import (
	"strings"
	"testing"
)

// TestRecallAndSupersedeFlow drives acceptance criteria 1–4: logging with
// --supersedes retires a fact from the live set (audit still passes), the brief/
// recall index drops it, recall <seq> pulls a full body, recall --chain walks the
// history, and --tag / --type / --grep / --all filter the index.
func TestRecallAndSupersedeFlow(t *testing.T) {
	dir := newTicket(t)
	actor := []string{"--data", dir, "--actor", "agent:x"}

	// #2 gotcha (tagged), then #3 superseding it (also tagged auth).
	run(t, append(actor, "log", "PROJ-1", "stripe test keys only work in test mode", "--type", "gotcha", "--tag", "auth")...)
	run(t, append(actor, "log", "PROJ-1", "actually live keys 401 until the account is verified", "--type", "gotcha", "--tag", "auth", "--supersedes", "2")...)
	// #4 an unrelated decision, untagged.
	run(t, append(actor, "log", "PROJ-1", "chose server-side pagination", "--type", "decision")...)

	// AC1: audit still passes over the superseding chain.
	if out, code := run(t, "--data", dir, "audit", "PROJ-1"); code != 0 {
		t.Fatalf("audit failed after supersede: %d\n%s", code, out)
	}

	// AC2/AC3 (index): the default recall prints the live index; #2 is gone, #3 live.
	idx, code := run(t, "--data", dir, "recall", "PROJ-1")
	if code != 0 {
		t.Fatalf("recall index exited %d: %s", code, idx)
	}
	if !strings.Contains(idx, "#3 gotcha") || !strings.Contains(idx, "actually live keys 401") {
		t.Errorf("index missing live fact #3:\n%s", idx)
	}
	if strings.Contains(idx, "#2 gotcha") || strings.Contains(idx, "only work in test mode") {
		t.Errorf("index still shows superseded #2:\n%s", idx)
	}

	// AC3 (body): recall <seq> returns the full body of a specific event.
	body, _ := run(t, "--data", dir, "recall", "PROJ-1", "4")
	if !strings.Contains(body, "chose server-side pagination") || !strings.Contains(body, "#4 decision") {
		t.Errorf("recall 4 body wrong:\n%s", body)
	}

	// AC3 (chain): the supersession history, oldest-to-newest, includes both facts.
	chain, _ := run(t, "--data", dir, "recall", "PROJ-1", "--chain", "3")
	i2 := strings.Index(chain, "only work in test mode")
	i3 := strings.Index(chain, "actually live keys 401")
	if i2 < 0 || i3 < 0 || i2 > i3 {
		t.Errorf("chain not oldest→newest (#2 before #3):\n%s", chain)
	}

	// AC4 (tag): --tag auth returns the live tagged fact; draiver attaches no meaning.
	byTag, _ := run(t, "--data", dir, "recall", "PROJ-1", "--tag", "auth")
	if !strings.Contains(byTag, "#3 gotcha") || strings.Contains(byTag, "#4 decision") {
		t.Errorf("--tag auth should match only #3:\n%s", byTag)
	}

	// --type filters the index to one type.
	byType, _ := run(t, "--data", dir, "recall", "PROJ-1", "--type", "decision")
	if !strings.Contains(byType, "#4 decision") || strings.Contains(byType, "#3 gotcha") {
		t.Errorf("--type decision should match only #4:\n%s", byType)
	}

	// --grep filters by body/headline match.
	byGrep, _ := run(t, "--data", dir, "recall", "PROJ-1", "--grep", "pagination")
	if !strings.Contains(byGrep, "#4 decision") || strings.Contains(byGrep, "#3 gotcha") {
		t.Errorf("--grep pagination should match only #4:\n%s", byGrep)
	}

	// --all surfaces the superseded fact with its retirement marker.
	all, _ := run(t, "--data", dir, "recall", "PROJ-1", "--all")
	if !strings.Contains(all, "#2 gotcha") || !strings.Contains(all, "superseded by #3") {
		t.Errorf("--all should show #2 as superseded by #3:\n%s", all)
	}
}

// --supersedes must name an existing event on the attempt, else the append is
// refused and nothing is written.
func TestLogSupersedesUnknownSeqRejected(t *testing.T) {
	dir := newTicket(t)
	out, code, err := runE(t, "--data", dir, "--actor", "agent:x",
		"log", "PROJ-1", "retire a ghost", "--type", "note", "--supersedes", "999")
	if code == 0 {
		t.Fatalf("expected non-zero exit for unknown supersedes seq:\n%s", out)
	}
	if err == nil || !strings.Contains(err.Error(), "999") {
		t.Errorf("error should name the bad seq, got: %v", err)
	}
	// Nothing new was written: the index holds only the genesis event.
	idx, _ := run(t, "--data", dir, "recall", "PROJ-1")
	if strings.Contains(idx, "retire a ghost") {
		t.Errorf("the rejected event must not be in the log:\n%s", idx)
	}
}

// AC5: an attempt whose log predates this change (no supersedes/tags anywhere)
// briefs and recalls with every event live and no errors.
func TestRecallBackwardCompatAllLive(t *testing.T) {
	dir := newTicket(t)
	actor := []string{"--data", dir, "--actor", "agent:x"}
	run(t, append(actor, "log", "PROJ-1", "first note", "--type", "note")...)
	run(t, append(actor, "log", "PROJ-1", "second note", "--type", "note")...)

	idx, code := run(t, "--data", dir, "recall", "PROJ-1")
	if code != 0 {
		t.Fatalf("recall exited %d: %s", code, idx)
	}
	for _, want := range []string{"#1 created", "#2 note", "#3 note"} {
		if !strings.Contains(idx, want) {
			t.Errorf("legacy-shaped log should list %q as live:\n%s", want, idx)
		}
	}
}
