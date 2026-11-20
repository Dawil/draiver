package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent/claudecode"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestFromLogOutcomes(t *testing.T) {
	ev := func(types ...string) []event.Event {
		var out []event.Event
		for i, ty := range types {
			out = append(out, event.Event{Seq: i + 1, Type: ty, Body: ty + " body"})
		}
		return out
	}
	cases := []struct {
		name     string
		a        project.Attempt
		want     string
		running  bool
		reviews  int
		escalate int
	}{
		{"ratified", project.Attempt{State: project.Done, Events: ev("created", "review", "done")}, OutcomeRatified, false, 1, 0},
		{"in review", project.Attempt{State: project.Review, Events: ev("created", "review")}, OutcomeInReview, false, 1, 0},
		{"escalated", project.Attempt{State: project.NeedsMe, Events: ev("created", "escalation")}, OutcomeEscalatedUnresolved, false, 0, 1},
		{"rejected, back at work", project.Attempt{State: project.Running, Events: ev("created", "review", "decision")}, OutcomeRejected, true, 1, 0},
		{"rejected, abandoned", project.Attempt{State: project.Review, Archived: true, Events: ev("created", "review")}, OutcomeRejected, false, 1, 0},
		{"abandoned", project.Attempt{State: project.Running, Archived: true, Events: ev("created")}, OutcomeAbandoned, false, 0, 0},
		{"running", project.Attempt{State: project.Running, Events: ev("created", "note")}, OutcomeRunning, true, 0, 0},
	}
	for _, c := range cases {
		f := FromLog(c.a)
		if f.Outcome != c.want || f.Running != c.running || f.ReviewRoundTrips != c.reviews || len(f.Escalations) != c.escalate {
			t.Errorf("%s: got %+v, want outcome %s running %v reviews %d escalations %d", c.name, f, c.want, c.running, c.reviews, c.escalate)
		}
	}
}

func TestAggregateGroupsByModel(t *testing.T) {
	row := func(model string, calls int, outcome string) Row {
		return Row{Provenance: Provenance{Model: Value{model, "recorded"}},
			Metrics: &Values{Outcome: outcome, Transcript: Transcript{ExplorationPrefixCalls: calls}}}
	}
	rows := []Row{
		row("m1", 1, OutcomeRatified), row("m1", 2, OutcomeRatified), row("m1", 3, OutcomeInReview),
		row("m1", 10, OutcomeRejected), row("m2", 7, OutcomeRatified), {Provenance: Provenance{}},
	}
	groups := Aggregate(rows)
	if len(groups) != 3 || groups[0].Model != "m1" || groups[0].Count != 4 {
		t.Fatalf("groups = %+v", groups)
	}
	st := groups[0].Metrics["exploration_prefix_calls"]
	if st.N != 4 || st.Median != 2.5 || st.P90 != 10 {
		t.Fatalf("m1 exploration_prefix_calls = %+v", st)
	}
	if groups[0].Outcomes[OutcomeRatified] != 2 || groups[0].Outcomes[OutcomeRejected] != 1 {
		t.Fatalf("outcomes = %v", groups[0].Outcomes)
	}
	var unknown *Group
	for i := range groups {
		if groups[i].Model == "unknown" {
			unknown = &groups[i]
		}
	}
	if unknown == nil || unknown.WithMetrics != 0 {
		t.Fatalf("a row with no model groups as unknown: %+v", groups)
	}

	n, _ := LookupNumeric("exploration_prefix_calls")
	SortRows(rows, n)
	if rows[0].Metrics.ExplorationPrefixCalls != 10 || rows[len(rows)-1].Metrics != nil {
		t.Fatalf("sort worst-first, nil last: first=%v", rows[0].Metrics.ExplorationPrefixCalls)
	}
}

// seedAttempt makes a data root with one finished (Review) attempt whose transcript
// is the widget fixture. It has no repo, so the final diff is unavailable.
func seedAttempt(t *testing.T) (store.Root, string, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	ticket := "drv-901"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath(ticket), []byte("---\nid: drv-901\ntitle: widget\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Repo: "/nonexistent/repo", Actor: "agent:x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, ticket, m.ID, event.Event{Type: "review", Actor: "agent:x", Body: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := root.EnsureSessionDir(ticket, m.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(widgetFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SessionStreamPath(ticket, m.ID), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, ticket, m.ID
}

func TestBuildModes(t *testing.T) {
	root, ticket, att := seedAttempt(t)
	now := func() time.Time { return time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC) }
	opts := func(m Mode) Options { return Options{Mode: m, Normalize: claudecode.Normalize, Now: now} }
	path := root.AttemptMetricsPath(ticket, att)

	row, err := Build(root, ticket, att, opts(ModeStored))
	if err != nil {
		t.Fatal(err)
	}
	if row.Source != SourceMissing || row.Metrics != nil {
		t.Fatalf("stored on a never-computed attempt: %+v", row)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("--stored must write nothing")
	}

	row, err = Build(root, ticket, att, opts(ModeCompute))
	if err != nil {
		t.Fatal(err)
	}
	if row.Source != SourceComputed || row.Metrics == nil || row.Metrics.Outcome != OutcomeInReview {
		t.Fatalf("compute: %+v", row)
	}
	if row.Metrics.FinalDiff != "unavailable" || row.Provenance.Model.Value != "claude-fixture-1" || row.Provenance.Model.Source != "observed" {
		t.Fatalf("compute provenance/diff: %+v %s", row.Provenance, row.Metrics.FinalDiff)
	}
	if row.Provenance.BaseCommit.Source != "unknown" {
		t.Fatalf("legacy attempt with no repo: base_commit = %+v", row.Provenance.BaseCommit)
	}
	computed := *row.Metrics

	row, err = Build(root, ticket, att, opts(ModeStored))
	if err != nil {
		t.Fatal(err)
	}
	if row.Source != SourceStored || row.Metrics.ExplorationPrefixCalls != computed.ExplorationPrefixCalls || row.Metrics.ToolCalls != computed.ToolCalls {
		t.Fatalf("stored after compute: %+v", row)
	}

	// --compute on a current record does not recompute.
	row, _ = Build(root, ticket, att, opts(ModeCompute))
	if row.Source != SourceStored {
		t.Fatalf("compute on a current record: %s", row.Source)
	}

	old := Version
	Version = old + 1
	defer func() { Version = old }()
	row, _ = Build(root, ticket, att, opts(ModeStored))
	if row.Source != SourceStale {
		t.Fatalf("after a version bump: %s", row.Source)
	}
	row, _ = Build(root, ticket, att, opts(ModeCompute))
	if row.Source != SourceComputed || row.MetricsVersion != Version {
		t.Fatalf("compute on stale: %+v", row)
	}
	row, _ = Build(root, ticket, att, opts(ModeRecompute))
	if row.Source != SourceComputed {
		t.Fatalf("recompute: %s", row.Source)
	}
}

func TestBuildRunningIsPartialAndNeverPersisted(t *testing.T) {
	root, ticket, att := seedAttempt(t)
	// A decision after the review reopens the attempt: it is running again.
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "decision", Actor: "human:x", Body: "rework"}); err != nil {
		t.Fatal(err)
	}
	row, err := Build(root, ticket, att, Options{Mode: ModeCompute, Normalize: claudecode.Normalize})
	if err != nil {
		t.Fatal(err)
	}
	if !row.Running() || row.Metrics != nil {
		t.Fatalf("excluded running attempt must not compute: %+v", row)
	}
	row, err = Build(root, ticket, att, Options{Mode: ModeCompute, IncludeRunning: true, Normalize: claudecode.Normalize})
	if err != nil {
		t.Fatal(err)
	}
	if !row.Partial || row.Metrics == nil || row.Metrics.Outcome != OutcomeRejected {
		t.Fatalf("included running attempt: %+v", row)
	}
	if _, err := os.Stat(root.AttemptMetricsPath(ticket, att)); !os.IsNotExist(err) {
		t.Fatal("partial numbers must never be persisted")
	}
}

func TestMetricsPathIsOutsideSession(t *testing.T) {
	root := store.Root{Dir: "/d"}
	p := root.AttemptMetricsPath("T", "0001")
	if filepath.Base(p) != "metrics.json" || filepath.Base(filepath.Dir(p)) != "0001" {
		t.Fatalf("path = %s", p)
	}
}
