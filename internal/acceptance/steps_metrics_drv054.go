package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/metrics"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// steps_metrics_drv054.go realises drv-054's acceptance criteria
// (features/metrics/metrics.feature): the `draiver metrics` verb over recorded
// stream.jsonl fixtures and seeded attempt logs. Every When runs the real draiver
// binary against a scratch data root and attaches its output as text evidence.

func init() {
	steps := map[string]StepFunc{
		"a metrics world of fixture attempts across two clones of one remote and an unrelated repo":       m54GivenWorld,
		"the drv-901 row reports the widget fixture's exploration and context metrics":                    m54ThenExploration,
		"the drv-901 row reports the widget fixture's error-kind split":                                   m54ThenErrors,
		"the drv-901 row reports outcome in-review with a one-file final diff":                            m54ThenOutcome,
		"the output shows the provenance, outcome, totals, exploration and errors sections":               m54ThenHumanAttempt,
		`the output names the transcript command "draiver ctl logs drv-901 --attempt 0001"`:               m54ThenOutputHas("draiver ctl logs drv-901 --attempt 0001"),
		`the drv-901 row source is "missing" and no metrics.json was written`:                             m54ThenSource("missing", false),
		`the drv-901 row source is "computed" and metrics.json was written`:                               m54ThenSource("computed", true),
		`the drv-901 row source is "stored" with values identical to the computed run`:                    m54ThenStoredIdentical,
		`the drv-901 row source is "stale"`:                                                               m54ThenSource("stale", true),
		"drv-901's stored record was computed under an older metrics_version":                             m54GivenOlderVersion,
		`every codebase row has source "computed"`:                                                        m54ThenAllComputed,
		"the codebase rows are drv-901, drv-902 and drv-903 but not drv-904":                              m54ThenSelection,
		"the aggregate is grouped by model claude-fixture-1 and claude-fixture-2 with outcome counts":     m54ThenGroups,
		"the aggregate lists the unread docs README.md and docs/guide.md":                                 m54ThenUnreadDocs,
		"the codebase rows are the 2 worst by exploration_prefix_calls, each with its transcript pointer": m54ThenWorstN,
		"the output shows per-model aggregates and the worst-first table with transcript pointers":        m54ThenHumanCodebase,
		"drv-901's base_commit and repo_id are recorded and its model is observed":                        m54ThenRecorded,
		"drv-902's base_commit is labelled inferred":                                                      m54ThenBase("drv-902", "inferred"),
		"drv-903's base_commit is unknown":                                                                m54ThenBase("drv-903", "unknown"),
	}
	for _, args := range []string{
		"metrics drv-901 --compute --json",
		"metrics drv-901 --compute",
		"metrics drv-901 --json",
		"metrics drv-901 --stored --json",
		"metrics --codebase widget-a --compute --json",
		"metrics --codebase widget-a --recompute --json",
		"metrics --codebase widget-a --compute --sort exploration_prefix_calls --limit 2 --json",
		"metrics --codebase widget-a --stored --sort exploration_prefix_calls --limit 2",
	} {
		steps[`I run draiver "`+args+`"`] = m54Run(args)
	}
	for text, fn := range steps {
		registry[text] = fn
	}
}

const m54Model1, m54Model2 = "claude-fixture-1", "claude-fixture-2"

type m54World struct {
	dir, data string
	root      store.Root
	widgetA   string
	baseA     string // the commit drv-901's worktree was cut from
	out       string
	computed  *metrics.Row // drv-901's row from the last --compute run
}

type m54AttemptReport struct {
	Row metrics.Row `json:"row"`
}

type m54CodebaseReport struct {
	Total     int `json:"total"`
	Aggregate struct {
		Groups     []metrics.Group `json:"groups"`
		UnreadDocs []string        `json:"unread_docs"`
	} `json:"aggregate"`
	Rows []metrics.Row `json:"rows"`
}

func m54Get(w *World) (*m54World, error) {
	m, _ := w.get("drv054").(*m54World)
	if m == nil {
		return nil, fmt.Errorf("no drv-054 metrics world: the Background must run first")
	}
	return m, nil
}

func m54Fixture() (string, error) {
	root, err := ovRepoRoot()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(root, "features", "metrics", "fixtures", "widget.stream.jsonl"))
	return string(b), err
}

// m54Lighter is the widget transcript under a second model with the three middle
// exploration calls (cat, the unused Read, the Task subagent) dropped, so drv-902
// explores less than drv-901 and sorts below it.
func m54Lighter(fixture string) string {
	var keep []string
	for _, l := range strings.Split(fixture, "\n") {
		if strings.Contains(l, `"toolu_fixture_04"`) || strings.Contains(l, `"toolu_fixture_05"`) || strings.Contains(l, `"toolu_fixture_06"`) {
			continue
		}
		keep = append(keep, l)
	}
	return strings.ReplaceAll(strings.Join(keep, "\n"), m54Model1, m54Model2)
}

func m54Out(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// m54Repo writes the widget codebase into a fresh clone dir and commits it on main.
func m54SeedRepo(dir string) error {
	files := map[string]string{
		"AGENTS.md":          "# Agents\nRun `go test ./...`.\n",
		"README.md":          "# widget\n",
		"docs/guide.md":      "# Guide\n",
		"src/widget.go":      "package widget\n\nfunc widget() int {\n\treturn 1\n}\n",
		"src/unused.go":      "package widget // unused\n",
		"src/widget_test.go": "package widget\n",
	}
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			return err
		}
	}
	if err := gitc(dir, "add", "-A"); err != nil {
		return err
	}
	return gitc(dir, "commit", "-qm", "widget")
}

// m54Commit edits src/widget.go on the checkout at dir and commits it: the attempt's
// final diff is that one file.
func m54Commit(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "src/widget.go"), []byte("package widget\n\nfunc widget() int {\n\treturn 2\n}\n"), 0o644); err != nil {
		return err
	}
	return gitc(dir, "commit", "-qam", "Fix widget")
}

func m54Attempt(root store.Root, ticket, repo, transcript string, events ...string) (string, error) {
	if err := root.EnsureTicketDir(ticket); err != nil {
		return "", err
	}
	spec := "---\nid: " + ticket + "\ntitle: widget " + ticket + "\n---\n\n# widget\n"
	if err := os.WriteFile(root.SpecPath(ticket), []byte(spec), 0o644); err != nil {
		return "", err
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Repo: repo, Base: "main", Actor: "agent:acceptance"})
	if err != nil {
		return "", err
	}
	for _, e := range events {
		typ, body, _ := strings.Cut(e, ":")
		actor := "agent:acceptance"
		if typ == "done" {
			actor = "human:acceptance"
		}
		if _, err := ticketlog.Append(root, ticket, m.ID, event.Event{Type: typ, Actor: actor, Body: body}); err != nil {
			return "", err
		}
	}
	if err := root.EnsureSessionDir(ticket, m.ID); err != nil {
		return "", err
	}
	return m.ID, os.WriteFile(root.SessionStreamPath(ticket, m.ID), []byte(transcript), 0o644)
}

func m54GivenWorld(w *World, sr *StepRun) error {
	fixture, err := m54Fixture()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "drv054-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	m := &m54World{dir: dir, data: filepath.Join(dir, "data"), widgetA: filepath.Join(dir, "widget-a")}
	m.root = store.Root{Dir: m.data}

	// One bare remote, two local clones of it, and an unrelated repo with no remote.
	origin := filepath.Join(dir, "origin.git")
	if err := gitc(dir, "init", "-q", "--bare", "-b", "main", origin); err != nil {
		return err
	}
	if err := gitc(dir, "clone", "-q", origin, m.widgetA); err != nil {
		return err
	}
	if err := gitc(m.widgetA, "checkout", "-q", "-b", "main"); err != nil {
		return err
	}
	if err := m54SeedRepo(m.widgetA); err != nil {
		return err
	}
	if err := gitc(m.widgetA, "push", "-q", "origin", "main"); err != nil {
		return err
	}
	widgetB := filepath.Join(dir, "widget-b")
	if err := gitc(dir, "clone", "-q", origin, widgetB); err != nil {
		return err
	}
	other := filepath.Join(dir, "other")
	if err := gitc(dir, "init", "-q", "-b", "main", other); err != nil {
		return err
	}
	if err := m54SeedRepo(other); err != nil {
		return err
	}

	// drv-901: freshly cut from widget-a, provenance recorded by the real writer.
	id, err := m54Attempt(m.root, "drv-901", m.widgetA, fixture, "review:widget fixed")
	if err != nil {
		return err
	}
	wt := filepath.Join(dir, "wt-901")
	if err := gitc(m.widgetA, "worktree", "add", "-q", "-b", metrics.AttemptBranch("drv-901", id), wt, "main"); err != nil {
		return err
	}
	if err := reconcile.RecordCut(m.root, "drv-901", id, wt, "0.0.0-acceptance"); err != nil {
		return err
	}
	if m.baseA, err = m54Out(wt, "rev-parse", "HEAD"); err != nil {
		return err
	}
	if err := m54Commit(wt); err != nil {
		return err
	}

	// drv-902: legacy (no provenance), in the second clone, branch still present, ratified.
	id, err = m54Attempt(m.root, "drv-902", widgetB, m54Lighter(fixture), "review:widget fixed", "done:ratified")
	if err != nil {
		return err
	}
	if err := gitc(widgetB, "checkout", "-q", "-b", metrics.AttemptBranch("drv-902", id)); err != nil {
		return err
	}
	if err := m54Commit(widgetB); err != nil {
		return err
	}
	if err := gitc(widgetB, "checkout", "-q", "main"); err != nil {
		return err
	}

	// drv-903: widget-a, no branch (base unknown, diff unavailable), escalated.
	if _, err := m54Attempt(m.root, "drv-903", m.widgetA, fixture, "escalation:Which widget registry should this use?"); err != nil {
		return err
	}
	// drv-904: an unrelated repository.
	if _, err := m54Attempt(m.root, "drv-904", other, fixture, "review:other"); err != nil {
		return err
	}

	w.set("drv054", m)
	attachText(sr, "metrics-world", fmt.Sprintf("origin %s\nwidget-a %s (drv-901 cut @ %s, drv-903)\nwidget-b %s (drv-902, legacy)\nother %s (drv-904)\n",
		origin, m.widgetA, m.baseA, widgetB, other))
	return nil
}

func m54Run(args string) StepFunc {
	return func(w *World, sr *StepRun) error {
		m, err := m54Get(w)
		if err != nil {
			return err
		}
		bin, err := overlayBin()
		if err != nil {
			return err
		}
		cmd := exec.Command(bin, strings.Fields(args)...)
		cmd.Dir = m.dir
		cmd.Env = append(os.Environ(),
			"DRAIVER_DATA="+m.data,
			"DRAIVER_CONFIG="+filepath.Join(m.dir, "config.json"),
			"XDG_CACHE_HOME="+filepath.Join(m.dir, "cache"),
			"DRAIVER_ACTOR=agent:acceptance",
			"DRAIVER_ATTEMPT=",
		)
		out, runErr := cmd.CombinedOutput()
		m.out = string(out)
		attachText(sr, "draiver-"+strings.ReplaceAll(strings.Fields(args)[1], "-", ""), fmt.Sprintf("$ draiver %s\n%s", args, out))
		if runErr != nil {
			return fmt.Errorf("draiver %s: %v\n%s", args, runErr, out)
		}
		if strings.HasPrefix(args, "metrics drv-901") && strings.Contains(args, "--json") && strings.Contains(args, "--compute") {
			r, err := m54AttemptRow(m)
			if err != nil {
				return err
			}
			m.computed = &r
		}
		return nil
	}
}

func m54AttemptRow(m *m54World) (metrics.Row, error) {
	var rep m54AttemptReport
	if err := json.Unmarshal([]byte(m.out), &rep); err != nil {
		return metrics.Row{}, fmt.Errorf("parse attempt JSON: %v\n%s", err, m.out)
	}
	return rep.Row, nil
}

func m54Codebase(m *m54World) (m54CodebaseReport, error) {
	var rep m54CodebaseReport
	if err := json.Unmarshal([]byte(m.out), &rep); err != nil {
		return rep, fmt.Errorf("parse codebase JSON: %v\n%s", err, m.out)
	}
	return rep, nil
}

func m54Row901(w *World) (*m54World, *metrics.Values, error) {
	m, err := m54Get(w)
	if err != nil {
		return nil, nil, err
	}
	r, err := m54AttemptRow(m)
	if err != nil {
		return nil, nil, err
	}
	if r.Metrics == nil {
		return nil, nil, fmt.Errorf("drv-901 row has no metrics (source %s)", r.Source)
	}
	return m, r.Metrics, nil
}

// m54Check compares named values, reporting every mismatch at once.
func m54Check(sr *StepRun, label string, got, want map[string]any) error {
	var bad []string
	var keys []string
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%-30s got %v, want %v\n", k, got[k], want[k])
		if !reflect.DeepEqual(got[k], want[k]) {
			bad = append(bad, k)
		}
	}
	attachText(sr, label, b.String())
	if len(bad) > 0 {
		return fmt.Errorf("%s mismatches %v:\n%s", label, bad, b.String())
	}
	return nil
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func m54ThenExploration(w *World, sr *StepRun) error {
	_, v, err := m54Row901(w)
	if err != nil {
		return err
	}
	return m54Check(sr, "exploration-and-context", map[string]any{
		"prefix_basis":             v.PrefixBasis,
		"exploration_prefix_calls": v.ExplorationPrefixCalls,
		"search_prefix_calls":      v.SearchPrefixCalls,
		"empty_searches":           v.EmptySearches,
		"repeated_searches":        v.RepeatedSearches,
		"files_read_distinct":      v.FilesReadDistinct,
		"files_read_unused":        v.FilesReadUnused,
		"rereads":                  v.Rereads,
		"docs_read":                strings.Join(v.DocsRead, ","),
		"first_doc_read_index":     deref(v.FirstDocReadIndex),
		"tool_calls":               v.ToolCalls,
	}, map[string]any{
		"prefix_basis":             "first-productive-edit",
		"exploration_prefix_calls": 4,
		"search_prefix_calls":      3,
		"empty_searches":           2,
		"repeated_searches":        1,
		"files_read_distinct":      3,
		"files_read_unused":        2,
		"rereads":                  1,
		"docs_read":                "AGENTS.md",
		"first_doc_read_index":     0,
		"tool_calls":               20,
	})
}

func m54ThenErrors(w *World, sr *StepRun) error {
	_, v, err := m54Row901(w)
	if err != nil {
		return err
	}
	return m54Check(sr, "error-kind-split", map[string]any{
		"env_discovery_errors":            v.EnvDiscoveryErrors,
		"test_failures":                   v.TestFailures,
		"edit_mechanics_errors":           v.EditMechanicsErrors,
		"permission_denials":              v.PermissionDenials,
		"tries_to_first_successful_test":  deref(v.TriesToFirstSuccessfulTest),
		"tries_to_first_successful_build": deref(v.TriesToFirstSuccessfulBuild),
		"time_to_first_green_test_s":      deref(v.TimeToFirstGreenTestSeconds),
		"reverted_edits":                  deref(v.RevertedEdits),
	}, map[string]any{
		"env_discovery_errors":            2,
		"test_failures":                   1,
		"edit_mechanics_errors":           1,
		"permission_denials":              1,
		"tries_to_first_successful_test":  2,
		"tries_to_first_successful_build": 2,
		"time_to_first_green_test_s":      550.0,
		"reverted_edits":                  1,
	})
}

func m54ThenOutcome(w *World, sr *StepRun) error {
	_, v, err := m54Row901(w)
	if err != nil {
		return err
	}
	return m54Check(sr, "outcome-and-diff", map[string]any{
		"outcome":            v.Outcome,
		"review_round_trips": v.ReviewRoundTrips,
		"escalations":        v.Escalations,
		"final_diff":         v.FinalDiff,
		"diff_files":         deref(v.DiffFiles),
	}, map[string]any{
		"outcome":            metrics.OutcomeInReview,
		"review_round_trips": 1,
		"escalations":        0,
		"final_diff":         "available",
		"diff_files":         1,
	})
}

func m54ThenOutputHas(subs ...string) StepFunc {
	return func(w *World, sr *StepRun) error {
		m, err := m54Get(w)
		if err != nil {
			return err
		}
		for _, s := range subs {
			if !strings.Contains(m.out, s) {
				return fmt.Errorf("output lacks %q:\n%s", s, m.out)
			}
		}
		return nil
	}
}

func m54ThenHumanAttempt(w *World, sr *StepRun) error {
	return m54ThenOutputHas("source: computed", "provenance", "outcome", "totals",
		"exploration & context", "errors & discovery", "env_discovery_errors", "in-review")(w, sr)
}

func m54ThenSource(want string, fileExists bool) StepFunc {
	return func(w *World, sr *StepRun) error {
		m, err := m54Get(w)
		if err != nil {
			return err
		}
		r, err := m54AttemptRow(m)
		if err != nil {
			return err
		}
		if string(r.Source) != want {
			return fmt.Errorf("drv-901 source = %q, want %q", r.Source, want)
		}
		_, statErr := os.Stat(m.root.AttemptMetricsPath("drv-901", "0001"))
		if exists := statErr == nil; exists != fileExists {
			return fmt.Errorf("metrics.json exists = %v, want %v", exists, fileExists)
		}
		if want == "missing" && r.Metrics != nil {
			return fmt.Errorf("--stored must not compute, got metrics %+v", r.Metrics)
		}
		return nil
	}
}

func m54ThenStoredIdentical(w *World, sr *StepRun) error {
	if err := m54ThenSource("stored", true)(w, sr); err != nil {
		return err
	}
	m, _ := m54Get(w)
	r, _ := m54AttemptRow(m)
	if m.computed == nil {
		return fmt.Errorf("no prior --compute row to compare against")
	}
	if !reflect.DeepEqual(r.Metrics, m.computed.Metrics) {
		return fmt.Errorf("stored metrics differ from computed:\nstored   %+v\ncomputed %+v", r.Metrics, m.computed.Metrics)
	}
	return nil
}

// m54GivenOlderVersion rewrites drv-901's metrics.json as if computed under an older
// definition set — the on-disk effect of bumping metrics.Version after computing.
func m54GivenOlderVersion(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	p := m.root.AttemptMetricsPath("drv-901", "0001")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var rec map[string]any
	if err := json.Unmarshal(b, &rec); err != nil {
		return err
	}
	rec["metrics_version"] = metrics.Version - 1
	b, _ = json.MarshalIndent(rec, "", "  ")
	attachText(sr, "metrics-json-downgraded", fmt.Sprintf("%s: metrics_version %d → %d", p, metrics.Version, metrics.Version-1))
	return os.WriteFile(p, b, 0o644)
}

func m54ThenAllComputed(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	rep, err := m54Codebase(m)
	if err != nil {
		return err
	}
	if len(rep.Rows) == 0 {
		return fmt.Errorf("no codebase rows")
	}
	for _, r := range rep.Rows {
		if r.Source != metrics.SourceComputed || r.MetricsVersion != metrics.Version {
			return fmt.Errorf("%s@%s source %s v%d, want computed v%d", r.Ticket, r.Attempt, r.Source, r.MetricsVersion, metrics.Version)
		}
	}
	return nil
}

func m54Tickets(rows []metrics.Row) []string {
	var t []string
	for _, r := range rows {
		t = append(t, r.Ticket)
	}
	return t
}

func m54ThenSelection(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	rep, err := m54Codebase(m)
	if err != nil {
		return err
	}
	got := m54Tickets(rep.Rows)
	sort.Strings(got)
	if strings.Join(got, ",") != "drv-901,drv-902,drv-903" {
		return fmt.Errorf("codebase rows = %v, want drv-901, drv-902, drv-903", got)
	}
	return nil
}

func m54ThenGroups(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	rep, err := m54Codebase(m)
	if err != nil {
		return err
	}
	byModel := map[string]metrics.Group{}
	for _, g := range rep.Aggregate.Groups {
		byModel[g.Model] = g
	}
	g1, g2 := byModel[m54Model1], byModel[m54Model2]
	if len(byModel) != 2 || g1.Count != 2 || g2.Count != 1 {
		return fmt.Errorf("groups = %+v, want %s×2 and %s×1", rep.Aggregate.Groups, m54Model1, m54Model2)
	}
	if g1.Outcomes[metrics.OutcomeInReview] != 1 || g1.Outcomes[metrics.OutcomeEscalatedUnresolved] != 1 || g2.Outcomes[metrics.OutcomeRatified] != 1 {
		return fmt.Errorf("outcome counts: %s %v, %s %v", m54Model1, g1.Outcomes, m54Model2, g2.Outcomes)
	}
	st, ok := g1.Metrics["exploration_prefix_calls"]
	if !ok || st.N != 2 || st.Median != 4 || st.P90 != 4 {
		return fmt.Errorf("%s exploration_prefix_calls stat = %+v", m54Model1, st)
	}
	return nil
}

func m54ThenUnreadDocs(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	rep, err := m54Codebase(m)
	if err != nil {
		return err
	}
	if got := strings.Join(rep.Aggregate.UnreadDocs, ","); got != "README.md,docs/guide.md" {
		return fmt.Errorf("unread_docs = %q", got)
	}
	return nil
}

func m54ThenWorstN(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	rep, err := m54Codebase(m)
	if err != nil {
		return err
	}
	if len(rep.Rows) != 2 || rep.Total != 3 {
		return fmt.Errorf("rows %d of total %d, want 2 of 3", len(rep.Rows), rep.Total)
	}
	for _, r := range rep.Rows {
		if r.Ticket == "drv-902" {
			return fmt.Errorf("drv-902 explores least and must not be among the worst 2: %v", m54Tickets(rep.Rows))
		}
		if r.TranscriptCmd != metrics.TranscriptCommand(r.Ticket, r.Attempt) {
			return fmt.Errorf("%s transcript pointer = %q", r.Ticket, r.TranscriptCmd)
		}
	}
	return nil
}

func m54ThenHumanCodebase(w *World, sr *StepRun) error {
	return m54ThenOutputHas("model "+m54Model1+": 2 attempt(s)", "model "+m54Model2+": 1 attempt(s)",
		"attempts, worst first by exploration_prefix_calls (limit 2)",
		"draiver ctl logs drv-901 --attempt 0001", "draiver ctl logs drv-903 --attempt 0001",
		"unread docs (in the repo at HEAD, read by no selected attempt): README.md, docs/guide.md")(w, sr)
}

func m54Row(w *World, ticket string) (*m54World, metrics.Row, error) {
	m, err := m54Get(w)
	if err != nil {
		return nil, metrics.Row{}, err
	}
	rep, err := m54Codebase(m)
	if err != nil {
		return nil, metrics.Row{}, err
	}
	for _, r := range rep.Rows {
		if r.Ticket == ticket {
			return m, r, nil
		}
	}
	return nil, metrics.Row{}, fmt.Errorf("no %s row", ticket)
}

func m54ThenRecorded(w *World, sr *StepRun) error {
	m, r, err := m54Row(w, "drv-901")
	if err != nil {
		return err
	}
	meta, err := attempt.LoadMeta(m.root, "drv-901", r.Attempt)
	if err != nil {
		return err
	}
	attachText(sr, "drv-901-provenance", fmt.Sprintf("attempt.md: base_commit %s repo_id %s draiver_version %s\nrow: %+v\n",
		meta.BaseCommit, meta.RepoID, meta.DraiverVersion, r.Provenance))
	p := r.Provenance
	if meta.BaseCommit != m.baseA || p.BaseCommit != (metrics.Value{Value: m.baseA, Source: "recorded"}) {
		return fmt.Errorf("base_commit: attempt.md %q, row %+v, want %s recorded", meta.BaseCommit, p.BaseCommit, m.baseA)
	}
	if meta.RepoID == "" || p.RepoID.Source != "recorded" || p.RepoID.Value != meta.RepoID {
		return fmt.Errorf("repo_id: attempt.md %q, row %+v", meta.RepoID, p.RepoID)
	}
	if p.Model.Value != m54Model1 || p.DraiverVersion.Value != "0.0.0-acceptance" {
		return fmt.Errorf("model %+v, draiver_version %+v", p.Model, p.DraiverVersion)
	}
	return nil
}

func m54ThenBase(ticket, source string) StepFunc {
	return func(w *World, sr *StepRun) error {
		_, r, err := m54Row(w, ticket)
		if err != nil {
			return err
		}
		b := r.Provenance.BaseCommit
		attachText(sr, ticket+"-base-commit", fmt.Sprintf("%s base_commit %+v", ticket, b))
		if b.Source != source || (source == "unknown") != (b.Value == "") {
			return fmt.Errorf("%s base_commit = %+v, want source %s", ticket, b, source)
		}
		return nil
	}
}
