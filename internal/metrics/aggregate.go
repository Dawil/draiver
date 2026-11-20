package metrics

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// Numeric is one numeric metric the codebase aggregate summarizes and --sort keys
// on. A nil value (not applicable, or no metrics) is skipped by the aggregate and
// sorts last.
type Numeric struct {
	Name string
	Get  func(r Row) *float64
}

func fi(n int) *float64 { f := float64(n); return &f }
func fp(n *int) *float64 {
	if n == nil {
		return nil
	}
	return fi(*n)
}
func ff(n *float64) *float64 { return n }
func onM(get func(v *Values) *float64) func(Row) *float64 {
	return func(r Row) *float64 {
		if r.Metrics == nil {
			return nil
		}
		return get(r.Metrics)
	}
}

// NumericMetrics is the ordered table of every numeric v1 metric.
var NumericMetrics = []Numeric{
	{"wall_time_s", onM(func(v *Values) *float64 { return ff(v.WallTimeSeconds) })},
	{"tool_calls", onM(func(v *Values) *float64 { return fi(v.ToolCalls) })},
	{"review_round_trips", onM(func(v *Values) *float64 { return fi(v.ReviewRoundTrips) })},
	{"escalations", onM(func(v *Values) *float64 { return fi(v.Escalations) })},
	{"human_interrupts", onM(func(v *Values) *float64 { return fi(v.HumanInterrupts) })},
	{"diff_files", onM(func(v *Values) *float64 { return fp(v.DiffFiles) })},
	{"diff_lines", onM(func(v *Values) *float64 { return fp(v.DiffLines) })},
	{"exploration_prefix_calls", onM(func(v *Values) *float64 { return fi(v.ExplorationPrefixCalls) })},
	{"exploration_prefix_tokens", onM(func(v *Values) *float64 { return fi(v.ExplorationPrefixTokens) })},
	{"search_prefix_calls", onM(func(v *Values) *float64 { return fi(v.SearchPrefixCalls) })},
	{"empty_searches", onM(func(v *Values) *float64 { return fi(v.EmptySearches) })},
	{"repeated_searches", onM(func(v *Values) *float64 { return fi(v.RepeatedSearches) })},
	{"files_read_distinct", onM(func(v *Values) *float64 { return fi(v.FilesReadDistinct) })},
	{"files_read_unused", onM(func(v *Values) *float64 { return fi(v.FilesReadUnused) })},
	{"wasted_context_ratio", onM(func(v *Values) *float64 { return ff(v.WastedContextRatio) })},
	{"rereads", onM(func(v *Values) *float64 { return fi(v.Rereads) })},
	{"docs_read", onM(func(v *Values) *float64 { return fi(len(v.DocsRead)) })},
	{"first_doc_read_index", onM(func(v *Values) *float64 { return fp(v.FirstDocReadIndex) })},
	{"env_discovery_errors", onM(func(v *Values) *float64 { return fi(v.EnvDiscoveryErrors) })},
	{"test_failures", onM(func(v *Values) *float64 { return fi(v.TestFailures) })},
	{"build_failures", onM(func(v *Values) *float64 { return fi(v.BuildFailures) })},
	{"edit_mechanics_errors", onM(func(v *Values) *float64 { return fi(v.EditMechanicsErrors) })},
	{"permission_denials", onM(func(v *Values) *float64 { return fi(v.PermissionDenials) })},
	{"other_errors", onM(func(v *Values) *float64 { return fi(v.OtherErrors) })},
	{"tries_to_first_successful_test", onM(func(v *Values) *float64 { return fp(v.TriesToFirstSuccessfulTest) })},
	{"tries_to_first_successful_build", onM(func(v *Values) *float64 { return fp(v.TriesToFirstSuccessfulBuild) })},
	{"tokens_to_first_green_test", onM(func(v *Values) *float64 { return fp(v.TokensToFirstGreenTest) })},
	{"time_to_first_green_test_s", onM(func(v *Values) *float64 { return ff(v.TimeToFirstGreenTestSeconds) })},
	{"reverted_edits", onM(func(v *Values) *float64 { return fp(v.RevertedEdits) })},
	{"normalized_work", func(r Row) *float64 {
		if r.Usage == nil {
			return nil
		}
		return fi(r.Usage.NormalizedWork)
	}},
	{"cost_usd", func(r Row) *float64 {
		if r.Usage == nil {
			return nil
		}
		return ff(r.Usage.CostUSD)
	}},
}

// LookupNumeric finds a numeric metric by name.
func LookupNumeric(name string) (Numeric, bool) {
	for _, n := range NumericMetrics {
		if n.Name == name {
			return n, true
		}
	}
	return Numeric{}, false
}

// Stat summarizes one numeric metric over a group: how many rows had a value, and
// their median and p90 (nearest-rank).
type Stat struct {
	N      int     `json:"n"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
}

// Group is the aggregate for one model. Model changes swamp codebase changes, so
// the codebase aggregate is always grouped by model.
type Group struct {
	Model       string          `json:"model"`
	Count       int             `json:"count"`
	WithMetrics int             `json:"with_metrics"`
	Outcomes    map[string]int  `json:"outcomes"`
	Metrics     map[string]Stat `json:"metrics"`
}

// Aggregate groups rows by model and summarizes every numeric metric.
func Aggregate(rows []Row) []Group {
	byModel := map[string][]Row{}
	for _, r := range rows {
		m := r.Provenance.Model.Value
		if m == "" {
			m = "unknown"
		}
		byModel[m] = append(byModel[m], r)
	}
	var out []Group
	for model, rs := range byModel {
		g := Group{Model: model, Count: len(rs), Outcomes: map[string]int{}, Metrics: map[string]Stat{}}
		for _, r := range rs {
			if r.Metrics != nil {
				g.WithMetrics++
				g.Outcomes[r.Metrics.Outcome]++
			}
		}
		for _, n := range NumericMetrics {
			var vals []float64
			for _, r := range rs {
				if v := n.Get(r); v != nil {
					vals = append(vals, *v)
				}
			}
			if len(vals) == 0 {
				continue
			}
			sort.Float64s(vals)
			g.Metrics[n.Name] = Stat{N: len(vals), Median: median(vals), P90: nearestRank(vals, 0.9)}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func nearestRank(sorted []float64, p float64) float64 {
	k := int(math.Ceil(p*float64(len(sorted)))) - 1
	if k < 0 {
		k = 0
	}
	return sorted[k]
}

// SortRows orders rows worst-first by a numeric metric (descending; rows without a
// value last, ties by ticket/attempt). Every v1 metric reads "higher is worse".
func SortRows(rows []Row, by Numeric) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := by.Get(rows[i]), by.Get(rows[j])
		switch {
		case a == nil && b == nil:
			return false
		case a == nil:
			return false
		case b == nil:
			return true
		}
		return *a > *b
	})
}

// Codebase is a resolved --codebase target.
type Codebase struct {
	Toplevel string `json:"toplevel"`
	RepoID   string `json:"repo_id"`
}

// ResolveCodebase resolves a path to its repository's main toplevel and identity.
func ResolveCodebase(path string) (Codebase, error) {
	top, err := Toplevel(path)
	if err != nil {
		return Codebase{}, fmt.Errorf("metrics: %s is not inside a git working tree: %w", path, err)
	}
	id, err := RepoID(top)
	if err != nil {
		return Codebase{}, err
	}
	return Codebase{Toplevel: top, RepoID: id}, nil
}

// Ref names one attempt.
type Ref struct{ Ticket, Attempt string }

// SelectCodebase returns every work attempt whose codebase matches cb. It matches on
// the recorded repo_id when there is one, else on the attempt's repo path
// canonicalized to its toplevel, else on a repo_id inferred from that path (so a
// legacy attempt in a second clone of the same remote still matches). Codebase
// ledger tickets are never selected: they are not work. since, when non-zero, drops
// attempts started before it.
func SelectCodebase(root store.Root, cb Codebase, since time.Time) ([]Ref, error) {
	tickets, err := root.ListTickets()
	if err != nil {
		return nil, err
	}
	var out []Ref
	for _, t := range tickets {
		ids, err := root.ListAttempts(t)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			if a, err := project.LoadAttempt(root, t, ids[0]); err == nil && a.Type == project.TypeCodebase {
				continue
			}
		}
		for _, id := range ids {
			m, err := attempt.LoadMeta(root, t, id)
			if err != nil {
				continue
			}
			if !since.IsZero() && m.Started.Before(since) {
				continue
			}
			if matchesCodebase(m, cb) {
				out = append(out, Ref{t, id})
			}
		}
	}
	return out, nil
}

func matchesCodebase(m attempt.Meta, cb Codebase) bool {
	if m.RepoID != "" {
		return m.RepoID == cb.RepoID
	}
	if m.Repo == "" {
		return false
	}
	if top, err := Toplevel(m.Repo); err == nil {
		if top == cb.Toplevel {
			return true
		}
		if id, err := RepoID(top); err == nil && id == cb.RepoID {
			return true
		}
		return false
	}
	return canonical(m.Repo) == cb.Toplevel
}

// UnreadDocs lists the doc files at the codebase's HEAD that no row with metrics
// ever read: candidates for being undiscoverable or dead.
func UnreadDocs(cb Codebase, rows []Row) ([]string, error) {
	docs, err := DocsAtHead(cb.Toplevel)
	if err != nil {
		return nil, err
	}
	read := map[string]bool{}
	for _, r := range rows {
		if r.Metrics == nil {
			continue
		}
		for _, d := range r.Metrics.DocsRead {
			read[d] = true
		}
	}
	out := []string{}
	for _, d := range docs {
		if !read[d] {
			out = append(out, d)
		}
	}
	return out, nil
}
