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

// recall --tags enumerates the distinct tags of the in-scope facts, sorted
// alphabetically with a per-tag fact count, and composes with --type/--grep/--all.
func TestRecallTagsEnumerates(t *testing.T) {
	dir := newTicket(t)
	actor := []string{"--data", dir, "--actor", "agent:x"}

	run(t, append(actor, "log", "PROJ-1", "auth tokens expire hourly", "--type", "gotcha", "--tag", "auth")...)                       // #2
	run(t, append(actor, "log", "PROJ-1", "shard the db by tenant", "--type", "decision", "--tag", "db", "--tag", "auth")...)         // #3
	run(t, append(actor, "log", "PROJ-1", "retire the auth gotcha", "--type", "gotcha", "--tag", "auth", "--supersedes", "2")...)     // #4 retires #2
	run(t, append(actor, "log", "PROJ-1", "note without tags", "--type", "note")...)                                                 // #5

	// Live scope: #3 (db, auth) and #4 (auth) carry tags; #2 is retired. auth=2, db=1,
	// sorted alphabetically.
	out, code := run(t, "--data", dir, "recall", "PROJ-1", "--tags")
	if code != 0 {
		t.Fatalf("recall --tags exited %d: %s", code, out)
	}
	ai, di := strings.Index(out, "auth"), strings.Index(out, "db")
	if ai < 0 || di < 0 || ai > di {
		t.Errorf("tags not sorted alphabetically (auth before db):\n%s", out)
	}
	if !strings.Contains(out, "auth") || !strings.Contains(out, "(2)") {
		t.Errorf("auth should carry count 2 (live #3 and #4):\n%s", out)
	}
	if !strings.Contains(out, "db") || !strings.Contains(out, "(1)") {
		t.Errorf("db should carry count 1:\n%s", out)
	}

	// --all widens scope to the retired #2 (also auth-tagged): auth=3.
	all, _ := run(t, "--data", dir, "recall", "PROJ-1", "--tags", "--all")
	if !strings.Contains(all, "(3)") {
		t.Errorf("--all should count the superseded auth fact (auth=3):\n%s", all)
	}

	// --type narrows the scope the tags are drawn from: only the decision #3 (db, auth).
	byType, _ := run(t, "--data", dir, "recall", "PROJ-1", "--tags", "--type", "decision")
	if !strings.Contains(byType, "auth") || !strings.Contains(byType, "db") || strings.Contains(byType, "(2)") {
		t.Errorf("--type decision should scope tags to #3 (auth=1, db=1):\n%s", byType)
	}

	// --grep narrows too: only #4's body matches "retire".
	byGrep, _ := run(t, "--data", dir, "recall", "PROJ-1", "--tags", "--grep", "retire")
	if !strings.Contains(byGrep, "auth") || !strings.Contains(byGrep, "(1)") || strings.Contains(byGrep, "db") {
		t.Errorf("--grep retire should scope tags to #4 (auth=1 only):\n%s", byGrep)
	}
}

// A ticket/attempt with no tags in scope prints the empty-set marker, mirroring the
// index's `(no matching facts)`.
func TestRecallTagsEmptyMarker(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "untagged note", "--type", "note")
	out, code := run(t, "--data", dir, "recall", "PROJ-1", "--tags")
	if code != 0 {
		t.Fatalf("recall --tags exited %d: %s", code, out)
	}
	if !strings.Contains(out, "(no tags)") {
		t.Errorf("expected the empty-tags marker:\n%s", out)
	}
}

// --tags is an index-level summary, so it is mutually exclusive with positional seqs
// and with --chain; either combination errors and writes nothing.
func TestRecallTagsRejectsSeqsAndChain(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "a gotcha", "--type", "gotcha", "--tag", "x")

	out, code, err := runE(t, "--data", dir, "recall", "PROJ-1", "--tags", "2")
	if code == 0 {
		t.Fatalf("--tags with a positional seq should error:\n%s", out)
	}
	if err == nil || !strings.Contains(err.Error(), "--tags") {
		t.Errorf("error should name --tags, got: %v", err)
	}
	if strings.Contains(out, "x ") || strings.Contains(out, "(1)") {
		t.Errorf("rejected --tags should write nothing:\n%s", out)
	}

	_, code2, err2 := runE(t, "--data", dir, "recall", "PROJ-1", "--tags", "--chain", "2")
	if code2 == 0 {
		t.Fatal("--tags with --chain should error")
	}
	if err2 == nil || !strings.Contains(err2.Error(), "--tags") {
		t.Errorf("error should name --tags, got: %v", err2)
	}
}

// secondAttempt starts attempt 0002 on PROJ-1 (data dir doubles as a repo path; an
// explicit --base sidesteps git branch resolution the throwaway dir can't answer).
func secondAttempt(t *testing.T, dir string) {
	t.Helper()
	if _, code := run(t, "--data", dir, "--actor", "human:test", "attempt", "new", "PROJ-1", "--repo", dir, "--base", "main"); code != 0 {
		t.Fatalf("attempt new exited %d", code)
	}
}

// recall accepts the ticket@attempt address form for every mode, with @attempt taking
// precedence over --attempt, and a missing attempt erroring (writing nothing).
func TestRecallAtAttemptAddressForm(t *testing.T) {
	dir := newTicket(t)
	secondAttempt(t, dir)
	// A fact unique to each attempt so we can prove which one recall targeted.
	run(t, "--data", dir, "--actor", "agent:x", "--attempt", "0001", "log", "PROJ-1", "lives on attempt one", "--type", "note")
	run(t, "--data", dir, "--actor", "agent:x", "--attempt", "0002", "log", "PROJ-1", "lives on attempt two", "--type", "note")

	one, code := run(t, "--data", dir, "recall", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("recall @0001 exited %d: %s", code, one)
	}
	if !strings.Contains(one, "lives on attempt one") || strings.Contains(one, "attempt two") {
		t.Errorf("@0001 did not target attempt 0001:\n%s", one)
	}

	two, _ := run(t, "--data", dir, "recall", "PROJ-1@0002")
	if !strings.Contains(two, "lives on attempt two") || strings.Contains(two, "attempt one") {
		t.Errorf("@0002 did not target attempt 0002:\n%s", two)
	}

	// @attempt wins over --attempt (resolveCtlTarget's precedence).
	win, _ := run(t, "--data", dir, "--attempt", "0002", "recall", "PROJ-1@0001")
	if !strings.Contains(win, "lives on attempt one") {
		t.Errorf("@0001 should win over --attempt 0002:\n%s", win)
	}

	// The address form reaches positional seqs too.
	body, _ := run(t, "--data", dir, "recall", "PROJ-1@0002", "2")
	if !strings.Contains(body, "lives on attempt two") {
		t.Errorf("recall PROJ-1@0002 2 wrong body:\n%s", body)
	}

	// A missing attempt errors and writes no index.
	out, mc, err := runE(t, "--data", dir, "recall", "PROJ-1@0099")
	if mc == 0 {
		t.Fatalf("recall of a missing attempt should error:\n%s", out)
	}
	if err == nil || !strings.Contains(err.Error(), "0099") {
		t.Errorf("error should name the missing attempt, got: %v", err)
	}
	if strings.Contains(out, "lives on attempt") {
		t.Errorf("a failed address lookup wrote an index:\n%s", out)
	}
}

// recall always names the resolved ticket@attempt, and — when the attempt was not
// chosen explicitly and the ticket has siblings — flags their existence and points at
// `attempt ls`. An explicit @attempt suppresses the sibling note.
func TestRecallContextHeaderAndSiblings(t *testing.T) {
	dir := newTicket(t)

	// Single attempt: the header names it, with no sibling note.
	solo, code := run(t, "--data", dir, "recall", "PROJ-1")
	if code != 0 {
		t.Fatalf("recall exited %d: %s", code, solo)
	}
	if !strings.Contains(solo, "# PROJ-1@0001") {
		t.Errorf("header does not name the resolved target:\n%s", solo)
	}
	if strings.Contains(solo, "attempts") {
		t.Errorf("a single-attempt ticket should carry no sibling note:\n%s", solo)
	}

	// With a sibling, an implicit recall resolves to the latest (0002) and flags both.
	secondAttempt(t, dir)
	implicit, _ := run(t, "--data", dir, "recall", "PROJ-1")
	if !strings.Contains(implicit, "# PROJ-1@0002") {
		t.Errorf("implicit recall should resolve to the latest attempt:\n%s", implicit)
	}
	if !strings.Contains(implicit, "latest of 2 attempts") || !strings.Contains(implicit, "attempt ls PROJ-1") {
		t.Errorf("implicit multi-attempt recall should flag siblings:\n%s", implicit)
	}

	// An explicit @attempt is a deliberate choice: name it, but no sibling note.
	explicit, _ := run(t, "--data", dir, "recall", "PROJ-1@0001")
	if !strings.Contains(explicit, "# PROJ-1@0001") {
		t.Errorf("explicit header missing:\n%s", explicit)
	}
	if strings.Contains(explicit, "attempts") {
		t.Errorf("an explicit @attempt should suppress the sibling note:\n%s", explicit)
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
