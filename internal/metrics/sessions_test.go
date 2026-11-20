package metrics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent/claudecode"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// sessionsFixture is the metrics-v2 transcript (gen_sessions.py): two sessions, three
// segments. Expected values are tabulated in features/metrics/README.md.
var sessionsFixture = filepath.Join("..", "..", "features", "metrics", "fixtures", "sessions.stream.jsonl")

var sessionsDiff = Diff{
	Available: true,
	Files:     map[string]bool{"src/big.go": true, "src/zz_drv905_focus_test.go": true},
	Lines:     3,
	BaseFiles: map[string]bool{"AGENTS.md": true, "src/big.go": true, "src/steps_drv901.go": true},
}

func foldSessions(t *testing.T) Transcript {
	t.Helper()
	st := NewState()
	if present, err := foldStream(sessionsFixture, claudecode.Normalize, st); err != nil || !present {
		t.Fatalf("fold: present=%v err=%v", present, err)
	}
	return st.Finalize(sessionsDiff)
}

func TestFoldSessionsFixture(t *testing.T) {
	got := foldSessions(t)
	checks := []struct {
		name      string
		got, want any
	}{
		{"sessions", got.SessionCount, 2},
		{"segments", got.Segments, 3},
		{"context_floor", deref(got.ContextFloor), 30000},
		{"compactions", got.Compactions, 1},
		{"wall_time_s", deref(got.WallTimeSeconds), 2090.0},
		{"active_time_s", deref(got.ActiveTimeSeconds), 350.0},
		{"exploration_prefix_tokens", got.ExplorationPrefixTokens, 40000},
		{"exploration_prefix_tokens_net", deref(got.ExplorationPrefixTokensNet), 10000},
		{"rereads", got.Rereads, 3},
		{"rereads_cross_session", got.RereadsCrossSession, 1},
		{"rereads_same_range", got.RereadsSameRange, 1},
		{"ranged_reads", got.RangedReads, 1},
		{"env_discovery_errors", got.EnvDiscoveryErrors, 1},
		{"transient_files", got.TransientFiles, []string{"tmp/notes.txt"}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	type sess struct {
		id                     string
		segs, calls            int
		start, peak, firstEdit any
	}
	var ss []sess
	for _, s := range got.Sessions {
		ss = append(ss, sess{s.ID, s.Segments, s.Calls, deref(s.StartContext), deref(s.PeakContext), deref(s.ContextAtFirstProductiveEdit)})
	}
	want := []sess{
		{"fixture-session-a", 2, 10, 30000, 60000, 40000},
		{"fixture-session-b", 1, 3, 31000, 36500, 35000},
	}
	if !reflect.DeepEqual(ss, want) {
		t.Errorf("sessions = %+v, want %+v", ss, want)
	}
	files := map[string]FileStat{}
	for _, f := range got.Files {
		files[f.Path] = f
	}
	if f := files["src/big.go"]; f.Reads != 4 || f.WholeReads != 1 || f.ContextTokens != 9000 || f.Unused {
		t.Errorf("big.go = %+v, want 4 reads, 1 whole, 9000 context tokens, used", f)
	}
	if f := files["src/steps_drv901.go"]; f.ContextTokens != 4000 || !f.Unused {
		t.Errorf("steps_drv901.go = %+v, want 4000 context tokens, unused", f)
	}
}

// TestComputeIsDeterministic computes the same fixture twice and requires
// byte-identical JSON apart from computed_at (drv-054 #19).
func TestComputeIsDeterministic(t *testing.T) {
	root, ticket, att := seedSessionsAttempt(t)
	var out [][]byte
	for i := 0; i < 2; i++ {
		now := func() time.Time { return time.Date(2026, 11, 20, i, 0, 0, 0, time.UTC) }
		row, err := Build(root, ticket, att, Options{Mode: ModeRecompute, Normalize: claudecode.Normalize, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		row.ComputedAt = nil
		b, _ := json.Marshal(row)
		out = append(out, b)
	}
	if !bytes.Equal(out[0], out[1]) {
		t.Fatalf("two computes differ:\n%s\n%s", out[0], out[1])
	}
}

func seedSessionsAttempt(t *testing.T) (store.Root, string, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	ticket := "drv-905"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(ticket), []byte("---\nid: drv-905\ntitle: s\n---\n"), 0o644)
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Repo: "/nonexistent/repo", Actor: "agent:t"})
	if err != nil {
		t.Fatal(err)
	}
	at := func(sec int) time.Time { return time.Date(2026, 11, 2, 10, 0, sec, 0, time.UTC) }
	for _, e := range []event.Event{
		{Type: "escalation", Actor: "agent:t", TS: at(1075), Tags: []string{TagContextAutoStop}, Body: "context exhausted"},
		{Type: "review", Actor: "agent:t", TS: at(2085), Body: "done"},
	} {
		if _, err := ticketlog.Append(root, ticket, m.ID, e); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(sessionsFixture)
	if err != nil {
		t.Fatal(err)
	}
	root.EnsureSessionDir(ticket, m.ID)
	if err := os.WriteFile(root.SessionStreamPath(ticket, m.ID), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, ticket, m.ID
}

func TestSessionsEndedByFromLog(t *testing.T) {
	root, ticket, att := seedSessionsAttempt(t)
	row, err := Build(root, ticket, att, Options{Mode: ModeCompute, Normalize: claudecode.Normalize})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range row.Metrics.Sessions {
		got = append(got, s.EndedBy)
	}
	if !reflect.DeepEqual(got, []string{"context-auto-stop", "review"}) || row.Metrics.ContextAutoStops != 1 {
		t.Fatalf("ended_by = %v, context_auto_stops = %d", got, row.Metrics.ContextAutoStops)
	}
	for _, f := range row.Metrics.Files {
		if want := f.Path == "src/steps_drv901.go"; f.CrossTicket != want {
			t.Errorf("%s cross_ticket = %v, want %v", f.Path, f.CrossTicket, want)
		}
	}
}

func TestEndedBy(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC) }
	endings := []Ending{{at(5), "park"}, {at(20), "review"}, {at(21), TagContextAutoStop}, {at(40), "done"}}
	cases := []struct {
		end, next time.Time
		want      string
	}{
		{at(6), at(15), "park"},              // park 1m before the end
		{at(19), at(30), TagContextAutoStop}, // auto-stop beats the review in the window
		{at(16), at(19), "none"},             // park is >10m before the end; review after next start
		{at(39), time.Time{}, "done"},        // last session, open-ended window
		{time.Time{}, time.Time{}, "none"},   // no transcript events
	}
	for _, c := range cases {
		if got := EndedBy(endings, c.end, c.next); got != c.want {
			t.Errorf("EndedBy(end %v, next %v) = %s, want %s", c.end, c.next, got, c.want)
		}
	}
}

func TestFromLogRoundTrips(t *testing.T) {
	a := project.Attempt{State: project.Review, Events: []event.Event{
		{Seq: 1, Type: "created", Actor: "agent:a"},
		{Seq: 2, Type: "review", Actor: "agent:a"},
		{Seq: 3, Type: "decision", Actor: "human:dave"},
		{Seq: 4, Type: "note", Actor: "human:dave"},
		{Seq: 5, Type: "note", Actor: "human:dave", Supersedes: []int{4}},
		{Seq: 6, Type: "gotcha", Actor: "human:dave"}, // not a decision/note
		{Seq: 7, Type: "note", Actor: "agent:a"},      // not human
		{Seq: 8, Type: "review", Actor: "agent:a"},
	}}
	got := FromLog(a).RoundTrips
	want := []RoundTrip{{ReviewSeq: 2, NextReviewSeq: 8, HumanEvents: 3, HumanSuperseded: 1}, {ReviewSeq: 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trips = %+v, want %+v", got, want)
	}
}

func TestNormCommand(t *testing.T) {
	cases := map[string]string{
		"cd /home/x/wt/drv-044/0001 && go test ./internal/web/... 2>&1 | tail -20": "go test ./internal/web/...",
		"go test ./x -run Drv047":        "go test ./x -run drvN",
		"go  test ./x -run drv044":       "go test ./x -run drvN",
		"draiver recall drv-012 14":      "draiver recall <ticket> N",
		"cd a; cd b && make | head -n 3": "make",
	}
	for in, want := range cases {
		if got := NormCommand(in); got != want {
			t.Errorf("NormCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTicketScopedAndPatterns(t *testing.T) {
	files := []string{
		"features/acceptance/zz_drv044_focus_test.go", // ticket-scoped
		"features/acceptance/zz_drv043_focus_test.go", // another ticket's
		"internal/x/steps_drv044.go",                  // not a test, no prefix
		"internal/x/tmp_drv044.go",                    // not a test
		"internal/x/x_test.go",
	}
	if got := TicketScopedTestFiles(files, "drv-044"); !reflect.DeepEqual(got, []string{"features/acceptance/zz_drv044_focus_test.go"}) {
		t.Errorf("TicketScopedTestFiles = %v", got)
	}
	for in, want := range map[string]string{
		"features/acceptance/zz_drv044_focus_test.go":  "features/acceptance/zz_*_test.go",
		"features/acceptance/zz_drv047_subset_test.go": "features/acceptance/zz_*_test.go",
		"tmp/notes.txt":                        "tmp/notes.txt",
		"internal/a/steps_promotion_drv043.go": "internal/a/steps_promotion_*.go",
	} {
		if got := NamePattern(in); got != want {
			t.Errorf("NamePattern(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		file, ticket string
		want         bool
	}{
		{"internal/acceptance/steps_boardcontrols_drv046.go", "drv-048", true},
		{"internal/acceptance/steps_boardcontrols_drv046.go", "drv-046", false},
		{"internal/crypto/sha256.go", "drv-048", false},
		{"internal/acceptance/steps_drvweb027.go", "drv-048", true}, // drvweb is in drv's family
		{"features/drvweb-027.feature", "drvweb-031", true},
	} {
		if got := IsCrossTicket(c.file, c.ticket); got != c.want {
			t.Errorf("IsCrossTicket(%s, %s) = %v, want %v", c.file, c.ticket, got, c.want)
		}
	}
}

func TestHotspotsAndInventions(t *testing.T) {
	row := func(ticket string, files []FileStat, cmds []CommandStat, transient, scoped []string) Row {
		return Row{Ticket: ticket, Attempt: "0001", Metrics: &Values{Transcript: Transcript{
			Files: files, Commands: cmds, TransientFiles: transient, TicketScopedTestFiles: scoped}}}
	}
	rows := []Row{
		row("drv-048", []FileStat{{Path: "web.go", Reads: 1, WholeReads: 1, ContextTokens: 15000},
			{Path: "steps_drv046.go", Reads: 2, ContextTokens: 3000, Unused: true, CrossTicket: true}},
			[]CommandStat{{"go test ./...", 3}, {"make x", 1}}, nil, []string{"a/zz_drv048_focus_test.go"}),
		row("drv-049", []FileStat{{Path: "steps_drv046.go", Reads: 1, ContextTokens: 3000, CrossTicket: true}},
			[]CommandStat{{"go test ./...", 1}}, []string{"tmp/notes.txt"}, []string{"a/zz_drv049_focus_test.go"}),
		{Ticket: "drv-050", Attempt: "0001"}, // no metrics: ignored
	}
	files := FileHotspots(rows, map[string]int{"web.go": 2970})
	if len(files) != 2 || files[0].Path != "web.go" || deref(files[0].LinesAtHead) != 2970 {
		t.Fatalf("files = %+v", files)
	}
	if f := files[1]; f.Attempts != 2 || f.ContextTokens != 6000 || f.UnusedIn != 1 ||
		!reflect.DeepEqual(f.CrossTicketReaders, []string{"drv-048@0001", "drv-049@0001"}) || f.LinesAtHead != nil {
		t.Fatalf("steps_drv046.go = %+v", f)
	}
	cmds := CommandHotspots(rows)
	if len(cmds) != 1 || cmds[0].Command != "go test ./..." || cmds[0].Attempts != 2 || cmds[0].Count != 4 {
		t.Fatalf("commands = %+v", cmds)
	}
	inv := Inventions(rows)
	if len(inv) != 2 || inv[0].Pattern != "a/zz_*_test.go" || len(inv[0].Attempts) != 2 || inv[1].Kind != "transient" {
		t.Fatalf("inventions = %+v", inv)
	}
}
