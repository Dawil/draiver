package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/metrics"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// steps_metrics_v2_drv054.go realises drv-054's metrics-v2 acceptance criteria
// (features/metrics/metrics_v2.feature, review feedback #19): sessions and context
// figures, the --by hotspot views, invention detection, cobra unknown-command
// classification, the landing-merge guard in base inference and determinism. It
// reuses metrics.feature's runner (m54Run) over its own scratch world.

func init() {
	steps := map[string]StepFunc{
		"a metrics v2 world with two restart-spanning attempts on widget-c and a merged-branch attempt":   m54v2GivenWorld,
		"drv-905 reports 2 sessions, 3 segments, a 30000-token context floor and 1 compaction":            m54v2ThenSessions,
		"drv-905's sessions ended by context-auto-stop then review, from the log":                         m54v2ThenEndedBy,
		"drv-905 splits its rereads into cross-session 1, same-range 1 and ranged 1":                      m54v2ThenRereads,
		"drv-905 reports active time 350s against wall time 2090s and a net exploration prefix of 10000":  m54v2ThenTimes,
		"drv-905 reports 1 env_discovery_error from `draiver promote` with a non-error result":            m54v2ThenUnknownCommand,
		"drv-905 reports transient file tmp/notes.txt and ticket-scoped test src/zz_drv905_focus_test.go": m54v2ThenInvention,
		"the codebase inventions group src/zz_*_test.go across drv-905 and drv-906":                       m54v2ThenInventions,
		"the output shows the v2 sections for drv-905": m54ThenOutputHas("sessions / segments", "2 / 3", "context_floor", "ended_by context-auto-stop",
			"ended_by review", "active_time", "cross-session 1, same range 1; ranged reads 1", "invention", "tmp/notes.txt", "src/zz_drv905_focus_test.go"),
		"the file hotspots rank src/big.go first with 18000 context tokens and 200 lines at HEAD": m54v2ThenFileTop,
		"src/steps_drv901.go is read cross-ticket by drv-905 and drv-906":                         m54v2ThenCrossTicket,
		"the output shows the file hotspot table": m54ThenOutputHas("files by context tokens:", "CONTEXT_TOKENS", "LINES@HEAD", "src/big.go",
			"drv-905@0001, drv-906@0001"),
		`the command hotspots include "draiver promote <ticket>" and "go test ./src/..." in 2 attempts each`: m54v2ThenCommands,
		"the output shows the command hotspot table": m54ThenOutputHas("commands recurring across >= 2 attempts:", "draiver promote <ticket>",
			"go test ./src/..."),
		"drv-801's base_commit is inferred as main at the merge and its diff is its own one file": m54v2ThenMergedBase,
		"the two outputs are byte-identical apart from computed_at":                               m54v2ThenDeterministic,
	}
	for _, args := range []string{
		"metrics drv-905 --compute --json",
		"metrics drv-905 --compute",
		"metrics --codebase widget-c --compute --json",
		"metrics --codebase widget-c --compute --by file --json",
		"metrics --codebase widget-c --stored --by file --limit 2",
		"metrics --codebase widget-c --compute --by command --json",
		"metrics --codebase widget-c --stored --by command",
		"metrics drv-801 --compute --json",
	} {
		steps[`I run draiver "`+args+`"`] = m54Run(args)
	}
	steps[`I run draiver "metrics drv-905 --recompute --json"`] = m54v2RunKeep(false)
	steps[`I run draiver "metrics drv-905 --recompute --json" again`] = m54v2RunKeep(true)
	for text, fn := range steps {
		registry[text] = fn
	}
}

// m54v2Prev holds the first of the two determinism runs' output.
type m54v2Prev struct{ out string }

func m54v2RunKeep(second bool) StepFunc {
	return func(w *World, sr *StepRun) error {
		if err := m54Run("metrics drv-905 --recompute --json")(w, sr); err != nil {
			return err
		}
		m, _ := m54Get(w)
		if !second {
			w.set("drv054-prev", &m54v2Prev{out: m.out})
		}
		return nil
	}
}

// m54v2At is the sessions fixture's clock (gen_sessions.py T0 + sec).
func m54v2At(sec int) time.Time {
	return time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
}

func m54v2Fixture(ticket string) (string, error) {
	root, err := ovRepoRoot()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(root, "features", "metrics", "fixtures", "sessions.stream.jsonl"))
	if err != nil {
		return "", err
	}
	num := strings.TrimPrefix(ticket, "drv-")
	s := strings.ReplaceAll(string(b), "drv905", "drv"+num)
	return strings.ReplaceAll(s, "drv-905", ticket), nil
}

func m54v2WriteFiles(dir string, files map[string]string) error {
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// m54v2Attempt seeds a restart-spanning attempt on repo: a context-auto-stop
// escalation at the end of session A and a review at the end of session B (the
// fixture's clock), the sessions transcript, a worktree cut by the real provenance
// writer, and a commit shipping src/big.go and the ticket's zz_ focus test.
func m54v2Attempt(m *m54World, ticket, repo string) error {
	transcript, err := m54v2Fixture(ticket)
	if err != nil {
		return err
	}
	id, err := m54Attempt(m.root, ticket, repo, transcript)
	if err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "escalation", Actor: "agent:acceptance", TS: m54v2At(1075), Tags: []string{metrics.TagContextAutoStop}, Body: "Context window exhausted."},
		{Type: "review", Actor: "agent:acceptance", TS: m54v2At(2085), Body: "big.go fixed"},
	} {
		if _, err := ticketlog.Append(m.root, ticket, id, e); err != nil {
			return err
		}
	}
	wt := filepath.Join(m.dir, "wt-"+ticket)
	if err := gitc(repo, "worktree", "add", "-q", "-b", metrics.AttemptBranch(ticket, id), wt, "main"); err != nil {
		return err
	}
	if err := reconcile.RecordCut(m.root, ticket, id, wt, "0.0.0-acceptance"); err != nil {
		return err
	}
	num := strings.TrimPrefix(ticket, "drv-")
	if err := m54v2WriteFiles(wt, map[string]string{
		"src/big.go":                          "line one of big.go\n",
		"src/zz_drv" + num + "_focus_test.go": "package widget\n",
	}); err != nil {
		return err
	}
	if err := gitc(wt, "add", "-A"); err != nil {
		return err
	}
	return gitc(wt, "commit", "-qm", ticket+" work")
}

func m54v2GivenWorld(w *World, sr *StepRun) error {
	dir, err := os.MkdirTemp("", "drv054v2-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	m := &m54World{dir: dir, data: filepath.Join(dir, "data")}
	m.root = store.Root{Dir: m.data}

	repo := filepath.Join(dir, "widget-c")
	if err := gitc(dir, "init", "-q", "-b", "main", repo); err != nil {
		return err
	}
	var big []string
	for i := 1; i <= 200; i++ {
		big = append(big, fmt.Sprintf("line %03d of big.go", i))
	}
	if err := m54v2WriteFiles(repo, map[string]string{
		"AGENTS.md":           "# Agents\nRun `go test ./...`.\n",
		"src/big.go":          strings.Join(big, "\n") + "\n",
		"src/steps_drv901.go": "package widget // drv-901 steps\n",
	}); err != nil {
		return err
	}
	if err := gitc(repo, "add", "-A"); err != nil {
		return err
	}
	if err := gitc(repo, "commit", "-qm", "widget-c"); err != nil {
		return err
	}
	for _, t := range []string{"drv-905", "drv-906"} {
		if err := m54v2Attempt(m, t, repo); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	}

	// drv-801: legacy attempt on the merged-repo fixture (no recorded provenance).
	root, err := ovRepoRoot()
	if err != nil {
		return err
	}
	merged := filepath.Join(dir, "merged")
	if out, err := exec.Command("sh", filepath.Join(root, "features", "metrics", "fixtures", "merged-repo.sh"), merged).CombinedOutput(); err != nil {
		return fmt.Errorf("merged-repo fixture: %v\n%s", err, out)
	}
	transcript, err := m54v2Fixture("drv-905")
	if err != nil {
		return err
	}
	if _, err := m54Attempt(m.root, "drv-801", merged, transcript, "review:landed"); err != nil {
		return err
	}
	w.set("drv054", m)
	attachText(sr, "metrics-v2-world", fmt.Sprintf("widget-c %s (drv-905, drv-906 cut from main)\nmerged %s (drv-801, legacy)\n", repo, merged))
	return nil
}

func m54v2Row(w *World) (*m54World, *metrics.Values, error) {
	m, err := m54Get(w)
	if err != nil {
		return nil, nil, err
	}
	r, err := m54AttemptRow(m)
	if err != nil {
		return nil, nil, err
	}
	if r.Metrics == nil {
		return nil, nil, fmt.Errorf("%s row has no metrics (source %s)", r.Ticket, r.Source)
	}
	return m, r.Metrics, nil
}

func m54v2Check(label string, pick func(v *metrics.Values) (got, want map[string]any)) StepFunc {
	return func(w *World, sr *StepRun) error {
		_, v, err := m54v2Row(w)
		if err != nil {
			return err
		}
		got, want := pick(v)
		return m54Check(sr, label, got, want)
	}
}

var m54v2ThenSessions = m54v2Check("sessions", func(v *metrics.Values) (map[string]any, map[string]any) {
	return map[string]any{
			"sessions": v.SessionCount, "segments": v.Segments, "context_floor": deref(v.ContextFloor), "compactions": v.Compactions,
		}, map[string]any{
			"sessions": 2, "segments": 3, "context_floor": 30000, "compactions": 1,
		}
})

var m54v2ThenEndedBy = m54v2Check("sessions-ended-by", func(v *metrics.Values) (map[string]any, map[string]any) {
	var ended []string
	var peaks []any
	for _, s := range v.Sessions {
		ended = append(ended, s.EndedBy)
		peaks = append(peaks, deref(s.PeakContext))
	}
	return map[string]any{
			"ended_by": strings.Join(ended, ","), "context_auto_stops": v.ContextAutoStops, "peak_context": peaks,
		}, map[string]any{
			"ended_by": "context-auto-stop,review", "context_auto_stops": 1, "peak_context": []any{60000, 36500},
		}
})

var m54v2ThenRereads = m54v2Check("rereads", func(v *metrics.Values) (map[string]any, map[string]any) {
	return map[string]any{
			"rereads": v.Rereads, "rereads_cross_session": v.RereadsCrossSession, "rereads_same_range": v.RereadsSameRange, "ranged_reads": v.RangedReads,
		}, map[string]any{
			"rereads": 3, "rereads_cross_session": 1, "rereads_same_range": 1, "ranged_reads": 1,
		}
})

var m54v2ThenTimes = m54v2Check("times-and-prefix", func(v *metrics.Values) (map[string]any, map[string]any) {
	return map[string]any{
			"active_time_s": deref(v.ActiveTimeSeconds), "wall_time_s": deref(v.WallTimeSeconds),
			"exploration_prefix_tokens": v.ExplorationPrefixTokens, "exploration_prefix_tokens_net": deref(v.ExplorationPrefixTokensNet),
		}, map[string]any{
			"active_time_s": 350.0, "wall_time_s": 2090.0, "exploration_prefix_tokens": 40000, "exploration_prefix_tokens_net": 10000,
		}
})

var m54v2ThenUnknownCommand = m54v2Check("unknown-command", func(v *metrics.Values) (map[string]any, map[string]any) {
	return map[string]any{"env_discovery_errors": v.EnvDiscoveryErrors, "other_errors": v.OtherErrors},
		map[string]any{"env_discovery_errors": 1, "other_errors": 0}
})

var m54v2ThenInvention = m54v2Check("invention", func(v *metrics.Values) (map[string]any, map[string]any) {
	return map[string]any{"transient_files": v.TransientFiles, "ticket_scoped_test_files": v.TicketScopedTestFiles},
		map[string]any{"transient_files": []string{"tmp/notes.txt"}, "ticket_scoped_test_files": []string{"src/zz_drv905_focus_test.go"}}
})

type m54v2CodebaseReport struct {
	Aggregate struct {
		Inventions []metrics.Invention `json:"inventions"`
	} `json:"aggregate"`
	Files    []metrics.FileHotspot    `json:"files"`
	Commands []metrics.CommandHotspot `json:"commands"`
}

func m54v2Codebase(w *World) (m54v2CodebaseReport, error) {
	var rep m54v2CodebaseReport
	m, err := m54Get(w)
	if err != nil {
		return rep, err
	}
	if err := json.Unmarshal([]byte(m.out), &rep); err != nil {
		return rep, fmt.Errorf("parse codebase JSON: %v\n%s", err, m.out)
	}
	return rep, nil
}

func m54v2ThenInventions(w *World, sr *StepRun) error {
	rep, err := m54v2Codebase(w)
	if err != nil {
		return err
	}
	for _, inv := range rep.Aggregate.Inventions {
		if inv.Pattern == "src/zz_*_test.go" && inv.Kind == "ticket-scoped-test" {
			if !reflect.DeepEqual(inv.Attempts, []string{"drv-905@0001", "drv-906@0001"}) {
				return fmt.Errorf("zz_ pattern attempts = %v", inv.Attempts)
			}
			return nil
		}
	}
	return fmt.Errorf("no src/zz_*_test.go invention in %+v", rep.Aggregate.Inventions)
}

func m54v2ThenFileTop(w *World, sr *StepRun) error {
	rep, err := m54v2Codebase(w)
	if err != nil {
		return err
	}
	if len(rep.Files) == 0 {
		return fmt.Errorf("no file hotspots")
	}
	f := rep.Files[0]
	fj, _ := json.Marshal(f)
	attachText(sr, "top-file", string(fj))
	if f.Path != "src/big.go" || f.ContextTokens != 18000 || f.Attempts != 2 || deref(f.LinesAtHead) != 200 {
		return fmt.Errorf("top file = %+v, want src/big.go, 18000 tokens, 2 attempts, 200 lines", f)
	}
	return nil
}

func m54v2ThenCrossTicket(w *World, sr *StepRun) error {
	rep, err := m54v2Codebase(w)
	if err != nil {
		return err
	}
	for _, f := range rep.Files {
		if f.Path == "src/steps_drv901.go" {
			if !reflect.DeepEqual(f.CrossTicketReaders, []string{"drv-905@0001", "drv-906@0001"}) || f.UnusedIn != 2 {
				return fmt.Errorf("steps_drv901.go = %+v", f)
			}
			return nil
		}
	}
	return fmt.Errorf("src/steps_drv901.go not among file hotspots")
}

func m54v2ThenCommands(w *World, sr *StepRun) error {
	rep, err := m54v2Codebase(w)
	if err != nil {
		return err
	}
	want := map[string]bool{"draiver promote <ticket>": false, "go test ./src/...": false}
	for _, c := range rep.Commands {
		if _, ok := want[c.Command]; ok && c.Attempts == 2 {
			want[c.Command] = true
		}
	}
	for c, ok := range want {
		if !ok {
			return fmt.Errorf("command %q not recurring in 2 attempts: %+v", c, rep.Commands)
		}
	}
	return nil
}

func m54v2ThenMergedBase(w *World, sr *StepRun) error {
	m, v, err := m54v2Row(w)
	if err != nil {
		return err
	}
	r, _ := m54AttemptRow(m)
	want, err := m54Out(filepath.Join(m.dir, "merged"), "rev-parse", "m801-base")
	if err != nil {
		return err
	}
	return m54Check(sr, "merged-base", map[string]any{
		"base_commit": r.Provenance.BaseCommit, "diff_files": deref(v.DiffFiles),
	}, map[string]any{
		"base_commit": metrics.Value{Value: want, Source: "inferred"}, "diff_files": 1,
	})
}

var m54v2ComputedAt = regexp.MustCompile(`"computed_at": "[^"]*"`)

func m54v2ThenDeterministic(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	prev, _ := w.get("drv054-prev").(*m54v2Prev)
	if prev == nil {
		return fmt.Errorf("no first run recorded")
	}
	a := m54v2ComputedAt.ReplaceAllString(prev.out, `"computed_at": "-"`)
	b := m54v2ComputedAt.ReplaceAllString(m.out, `"computed_at": "-"`)
	if a != b {
		return fmt.Errorf("outputs differ:\n--- first\n%s\n--- second\n%s", a, b)
	}
	attachText(sr, "determinism", fmt.Sprintf("two --recompute runs, %d bytes each, identical apart from computed_at", len(b)))
	return nil
}
