package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/metrics"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
)

// steps_metrics_drv057.go realises drv-057's acceptance criteria
// (features/metrics/metrics_drv057.feature): a `cd` earlier in a Bash command moves
// where its relative paths resolve (paths outside the worktree drop out), the
// metrics_version bump that makes older rows stale, and `--split-at COMMIT` cohorts.
// It reuses drv-054's runner (m54Run) and output checks over its own scratch world,
// stored under the same world key.

const m57Scratch = "/tmp/drv057-scratch"

func init() {
	steps := map[string]StepFunc{
		"a drv-057 metrics world split by an AGENTS.md change on widget-d":         m57GivenWorld,
		"drv-910 counts no file written or read under /tmp after a cd":             m57ThenNoScratch,
		"drv-910 resolves `cd src && cat > a.md` to the shipped src/a.md":          m57ThenShipped,
		"drv-910's only transient file is the throwaway notes.md at the repo root": m57ThenTransient,
		"the row is computed under metrics_version 3":                              m57ThenVersion(metrics.SourceComputed, 3),
		"drv-910's stored record is rewritten as metrics_version 2":                m57GivenV2,
		"the row reads as stale": m57ThenVersion(metrics.SourceStale, 2),
		"the before cohort is drv-910 and the after cohort is drv-911 and drv-912":           m57ThenCohorts,
		"drv-913 is excluded from both cohorts as an unknown base":                           m57ThenUnknown,
		"each cohort carries its own aggregate: transient_files median 1 before and 0 after": m57ThenCohortAggregates,
		"the output shows the before and after cohorts and the excluded unknown base": m54ThenOutputHas(
			"split at agents-md-change", "== before", "== after", "1 attempt(s) excluded, base_commit unknown: drv-913@0001",
			"transient_files"),
		"each cohort ranks AGENTS.md with its own reader count": m57ThenCohortFiles,
		"the output shows a file hotspot table per cohort": m57ThenOutputCount(
			map[string]int{"files by context tokens:": 2, "== before": 1, "== after": 1, "AGENTS.md": 2}),
		"it fails naming the unknown --split-at commit": m54ThenOutputHas(`--split-at "no-such-commit" is not a commit`),
	}
	for _, args := range []string{
		"metrics drv-910 --compute --json",
		"metrics drv-910 --json",
		"metrics --codebase widget-d --compute --split-at agents-md-change --json",
		"metrics --codebase widget-d --compute --split-at agents-md-change",
		"metrics --codebase widget-d --compute --split-at agents-md-change --by file --json",
		"metrics --codebase widget-d --stored --split-at agents-md-change --by file",
	} {
		steps[`I run draiver "`+args+`"`] = m54Run(args)
	}
	steps[`I run draiver "metrics --codebase widget-d --split-at no-such-commit" expecting failure`] = m57RunFails("metrics --codebase widget-d --split-at no-such-commit")
	for text, fn := range steps {
		registry[text] = fn
	}
}

// m57Transcript builds a minimal claude-code stream for a worktree at cwd: one
// AGENTS.md Read, then the given Bash commands, each succeeding.
func m57Transcript(cwd string, cmds []string) string {
	t0 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	at := func(i int) string { return t0.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05.000Z") }
	const sess, model = "fixture-session-drv057", "claude-fixture-1"
	var lines []any
	lines = append(lines, map[string]any{"type": "system", "subtype": "init", "cwd": cwd, "session_id": sess, "model": model, "timestamp": at(0)})
	call := func(i int, name string, input map[string]any, result string) {
		id := fmt.Sprintf("toolu_drv057_%02d", i)
		lines = append(lines,
			map[string]any{"type": "assistant", "parent_tool_use_id": nil, "session_id": sess, "timestamp": at(i), "message": map[string]any{
				"model": model, "id": fmt.Sprintf("msg_drv057_%02d", i), "type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}},
				"usage":   map[string]any{"input_tokens": 1000, "cache_read_input_tokens": 1000 * i, "cache_creation_input_tokens": 0, "output_tokens": 100},
			}},
			map[string]any{"type": "user", "parent_tool_use_id": nil, "session_id": sess, "timestamp": at(i), "message": map[string]any{
				"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": result, "is_error": false}},
			}},
		)
	}
	call(1, "Read", map[string]any{"file_path": filepath.Join(cwd, "AGENTS.md")}, "# Agents\nScratch in /tmp.")
	for i, c := range cmds {
		call(i+2, "Bash", map[string]any{"command": c}, "ok")
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

// m57Commands is drv-910's Bash: the spec's three cd shapes, a cd'd read in /tmp and
// one in src/, and (before only) a throwaway notes.md at the repo root.
func m57Commands(throwaway bool) []string {
	cmds := []string{
		"mkdir -p " + m57Scratch,
		"cd " + m57Scratch + "; cat > a.md <<'EOF'\nscratch\nEOF",
		"cd " + m57Scratch + " && sed -i 's/scratch/draft/' a.md",
		"cd " + m57Scratch + " && cat notes.md",
		"cd src && cat widget.go",
		"cd src && cat > a.md <<'EOF'\n# shipped\nEOF",
	}
	if throwaway {
		cmds = append(cmds, "cat > notes.md <<'EOF'\nthrowaway\nEOF")
	}
	return cmds
}

// m57Attempt cuts ticket from repo's main (recording provenance), folds the
// transcript in, and commits the shipped src/a.md on the attempt branch.
func m57Attempt(m *m54World, ticket, repo string, throwaway bool) error {
	wt := filepath.Join(m.dir, "wt-"+ticket)
	id, err := m54Attempt(m.root, ticket, repo, m57Transcript(wt, m57Commands(throwaway)), "review:a.md shipped")
	if err != nil {
		return err
	}
	if err := gitc(repo, "worktree", "add", "-q", "-b", metrics.AttemptBranch(ticket, id), wt, "main"); err != nil {
		return err
	}
	if err := reconcile.RecordCut(m.root, ticket, id, wt, "0.0.0-acceptance"); err != nil {
		return err
	}
	if err := m54v2WriteFiles(wt, map[string]string{"src/a.md": "# shipped\n"}); err != nil {
		return err
	}
	if err := gitc(wt, "add", "-A"); err != nil {
		return err
	}
	return gitc(wt, "commit", "-qm", ticket+" ships src/a.md")
}

func m57GivenWorld(w *World, sr *StepRun) error {
	dir, err := os.MkdirTemp("", "drv057-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	m := &m54World{dir: dir, data: filepath.Join(dir, "data")}
	m.root = store.Root{Dir: m.data}

	repo := filepath.Join(dir, "widget-d")
	if err := gitc(dir, "init", "-q", "-b", "main", repo); err != nil {
		return err
	}
	if err := m54v2WriteFiles(repo, map[string]string{
		"AGENTS.md":     "# Agents\nRun `go test ./...`.\n",
		"src/widget.go": "package widget\n",
	}); err != nil {
		return err
	}
	if err := gitc(repo, "add", "-A"); err != nil {
		return err
	}
	if err := gitc(repo, "commit", "-qm", "C0 widget-d"); err != nil {
		return err
	}
	if err := m57Attempt(m, "drv-910", repo, true); err != nil {
		return fmt.Errorf("drv-910: %w", err)
	}
	// C1: the change under study, tagged so the scenario can name it.
	if err := m54v2WriteFiles(repo, map[string]string{"AGENTS.md": "# Agents\nRun `go test ./...`.\nScratch goes in /tmp, never the repo root.\n"}); err != nil {
		return err
	}
	if err := gitc(repo, "commit", "-qam", "C1 AGENTS.md: scratch in /tmp"); err != nil {
		return err
	}
	if err := gitc(repo, "tag", "agents-md-change"); err != nil {
		return err
	}
	for _, t := range []string{"drv-911", "drv-912"} {
		if err := m57Attempt(m, t, repo, false); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	}
	// drv-913: on widget-d, but no branch and no recorded base.
	if _, err := m54Attempt(m.root, "drv-913", repo, m57Transcript(repo, nil), "review:legacy"); err != nil {
		return err
	}
	w.set("drv054", m)
	c0, _ := m54Out(repo, "rev-parse", "agents-md-change^")
	c1, _ := m54Out(repo, "rev-parse", "agents-md-change")
	attachText(sr, "metrics-drv057-world", fmt.Sprintf("widget-d %s\nC0 %s  drv-910 cut here (before)\nC1 %s  agents-md-change; drv-911, drv-912 cut here (after)\ndrv-913 no branch, no base (unknown)\n\ndrv-910 Bash:\n%s\n",
		repo, c0, c1, strings.Join(m57Commands(true), "\n---\n")))
	return nil
}

func m57Row(w *World) (*metrics.Row, error) {
	m, err := m54Get(w)
	if err != nil {
		return nil, err
	}
	r, err := m54AttemptRow(m)
	if err != nil {
		return nil, err
	}
	if r.Metrics == nil {
		return nil, fmt.Errorf("%s row has no metrics (source %s)", r.Ticket, r.Source)
	}
	return &r, nil
}

func m57ThenNoScratch(w *World, sr *StepRun) error {
	r, err := m57Row(w)
	if err != nil {
		return err
	}
	var read []string
	for _, f := range r.Metrics.Files {
		read = append(read, f.Path)
	}
	return m54Check(sr, "no-scratch", map[string]any{
		"files_read":          read,
		"files_read_distinct": r.Metrics.FilesReadDistinct,
		"reverted_edits":      deref(r.Metrics.RevertedEdits),
	}, map[string]any{
		"files_read":          []string{"AGENTS.md", "src/widget.go"},
		"files_read_distinct": 2,
		"reverted_edits":      1, // notes.md only: /tmp/…/a.md is not a repo file
	})
}

func m57ThenShipped(w *World, sr *StepRun) error {
	r, err := m57Row(w)
	if err != nil {
		return err
	}
	// Without cd tracking the edit is "a.md", not in the diff, and the prefix falls
	// back to first-edit.
	return m54Check(sr, "shipped", map[string]any{
		"prefix_basis": r.Metrics.PrefixBasis, "diff_files": deref(r.Metrics.DiffFiles),
	}, map[string]any{
		"prefix_basis": "first-productive-edit", "diff_files": 1,
	})
}

func m57ThenTransient(w *World, sr *StepRun) error {
	r, err := m57Row(w)
	if err != nil {
		return err
	}
	return m54Check(sr, "transient", map[string]any{"transient_files": r.Metrics.TransientFiles},
		map[string]any{"transient_files": []string{"notes.md"}})
}

func m57ThenVersion(src metrics.Source, version int) StepFunc {
	return func(w *World, sr *StepRun) error {
		m, err := m54Get(w)
		if err != nil {
			return err
		}
		r, err := m54AttemptRow(m)
		if err != nil {
			return err
		}
		return m54Check(sr, "version", map[string]any{"source": r.Source, "metrics_version": r.MetricsVersion, "current_version": metrics.Version},
			map[string]any{"source": src, "metrics_version": version, "current_version": 3})
	}
}

func m57GivenV2(w *World, sr *StepRun) error {
	m, err := m54Get(w)
	if err != nil {
		return err
	}
	p := m.root.AttemptMetricsPath("drv-910", "0001")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var rec map[string]any
	if err := json.Unmarshal(b, &rec); err != nil {
		return err
	}
	rec["metrics_version"] = 2
	b, _ = json.MarshalIndent(rec, "", "  ")
	attachText(sr, "metrics-json-v2", fmt.Sprintf("%s: metrics_version 3 → 2", p))
	return os.WriteFile(p, b, 0o644)
}

type m57Cohort struct {
	Attempts  int      `json:"attempts"`
	Refs      []string `json:"refs"`
	Aggregate struct {
		Groups []metrics.Group `json:"groups"`
	} `json:"aggregate"`
	Files []metrics.FileHotspot `json:"files"`
}

type m57SplitReport struct {
	Split *struct {
		Commit              string    `json:"commit"`
		SHA                 string    `json:"sha"`
		ExcludedUnknownBase int       `json:"excluded_unknown_base"`
		UnknownBase         []string  `json:"unknown_base"`
		Before              m57Cohort `json:"before"`
		After               m57Cohort `json:"after"`
	} `json:"split"`
}

func m57Split(w *World) (*m57SplitReport, error) {
	m, err := m54Get(w)
	if err != nil {
		return nil, err
	}
	var rep m57SplitReport
	if err := json.Unmarshal([]byte(m.out), &rep); err != nil {
		return nil, fmt.Errorf("parse --split-at JSON: %v\n%s", err, m.out)
	}
	if rep.Split == nil {
		return nil, fmt.Errorf("no split object in:\n%s", m.out)
	}
	return &rep, nil
}

func m57ThenCohorts(w *World, sr *StepRun) error {
	rep, err := m57Split(w)
	if err != nil {
		return err
	}
	s := rep.Split
	return m54Check(sr, "cohorts", map[string]any{
		"commit": s.Commit, "sha_len": len(s.SHA), "before": s.Before.Refs, "after": s.After.Refs,
		"before_attempts": s.Before.Attempts, "after_attempts": s.After.Attempts,
	}, map[string]any{
		"commit": "agents-md-change", "sha_len": 40, "before": []string{"drv-910@0001"}, "after": []string{"drv-911@0001", "drv-912@0001"},
		"before_attempts": 1, "after_attempts": 2,
	})
}

func m57ThenUnknown(w *World, sr *StepRun) error {
	rep, err := m57Split(w)
	if err != nil {
		return err
	}
	return m54Check(sr, "unknown-base", map[string]any{"excluded_unknown_base": rep.Split.ExcludedUnknownBase, "unknown_base": rep.Split.UnknownBase},
		map[string]any{"excluded_unknown_base": 1, "unknown_base": []string{"drv-913@0001"}})
}

func m57Median(c m57Cohort, metric string) any {
	if len(c.Aggregate.Groups) != 1 {
		return fmt.Sprintf("%d groups", len(c.Aggregate.Groups))
	}
	g := c.Aggregate.Groups[0]
	s, ok := g.Metrics[metric]
	if !ok {
		return "absent"
	}
	return fmt.Sprintf("n=%d median=%v", s.N, s.Median)
}

func m57ThenCohortAggregates(w *World, sr *StepRun) error {
	rep, err := m57Split(w)
	if err != nil {
		return err
	}
	s := rep.Split
	return m54Check(sr, "cohort-aggregates", map[string]any{
		"before transient_files": m57Median(s.Before, "transient_files"), "after transient_files": m57Median(s.After, "transient_files"),
		"before reverted_edits": m57Median(s.Before, "reverted_edits"), "after reverted_edits": m57Median(s.After, "reverted_edits"),
	}, map[string]any{
		"before transient_files": "n=1 median=1", "after transient_files": "n=2 median=0",
		"before reverted_edits": "n=1 median=1", "after reverted_edits": "n=2 median=0",
	})
}

func m57ThenCohortFiles(w *World, sr *StepRun) error {
	rep, err := m57Split(w)
	if err != nil {
		return err
	}
	readers := func(c m57Cohort) any {
		for _, f := range c.Files {
			if f.Path == "AGENTS.md" {
				return f.Attempts
			}
		}
		return "absent"
	}
	return m54Check(sr, "cohort-files", map[string]any{"before AGENTS.md readers": readers(rep.Split.Before), "after AGENTS.md readers": readers(rep.Split.After)},
		map[string]any{"before AGENTS.md readers": 1, "after AGENTS.md readers": 2})
}

func m57ThenOutputCount(want map[string]int) StepFunc {
	return func(w *World, sr *StepRun) error {
		m, err := m54Get(w)
		if err != nil {
			return err
		}
		got := map[string]any{}
		wantAny := map[string]any{}
		for s, n := range want {
			got[s], wantAny[s] = strings.Count(m.out, s), n
		}
		if !reflect.DeepEqual(got, wantAny) {
			return fmt.Errorf("output counts %v, want %v:\n%s", got, wantAny, m.out)
		}
		return nil
	}
}

// m57RunFails runs draiver expecting a non-zero exit, keeping the output for the Then.
func m57RunFails(args string) StepFunc {
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
		cmd.Env = append(os.Environ(), "DRAIVER_DATA="+m.data, "DRAIVER_CONFIG="+filepath.Join(m.dir, "config.json"),
			"XDG_CACHE_HOME="+filepath.Join(m.dir, "cache"), "DRAIVER_ACTOR=agent:acceptance", "DRAIVER_ATTEMPT=")
		out, runErr := cmd.CombinedOutput()
		m.out = string(out)
		attachText(sr, "draiver-metrics-fails", fmt.Sprintf("$ draiver %s\n%s", args, out))
		if runErr == nil {
			return fmt.Errorf("draiver %s succeeded, want failure:\n%s", args, out)
		}
		return nil
	}
}
