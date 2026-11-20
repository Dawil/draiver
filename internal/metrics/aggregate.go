package metrics

import (
	"fmt"
	"math"
	"slices"
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
	{"active_time_s", onM(func(v *Values) *float64 { return ff(v.ActiveTimeSeconds) })},
	{"sessions", onM(func(v *Values) *float64 { return fi(v.SessionCount) })},
	{"segments", onM(func(v *Values) *float64 { return fi(v.Segments) })},
	{"context_auto_stops", onM(func(v *Values) *float64 { return fi(v.ContextAutoStops) })},
	{"context_floor", onM(func(v *Values) *float64 { return fp(v.ContextFloor) })},
	{"compactions", onM(func(v *Values) *float64 { return fi(v.Compactions) })},
	{"tool_calls", onM(func(v *Values) *float64 { return fi(v.ToolCalls) })},
	{"review_round_trips", onM(func(v *Values) *float64 { return fi(v.ReviewRoundTrips) })},
	{"escalations", onM(func(v *Values) *float64 { return fi(v.Escalations) })},
	{"human_interrupts", onM(func(v *Values) *float64 { return fi(v.HumanInterrupts) })},
	{"diff_files", onM(func(v *Values) *float64 { return fp(v.DiffFiles) })},
	{"diff_lines", onM(func(v *Values) *float64 { return fp(v.DiffLines) })},
	{"exploration_prefix_calls", onM(func(v *Values) *float64 { return fi(v.ExplorationPrefixCalls) })},
	{"exploration_prefix_tokens", onM(func(v *Values) *float64 { return fi(v.ExplorationPrefixTokens) })},
	{"exploration_prefix_tokens_net", onM(func(v *Values) *float64 { return fp(v.ExplorationPrefixTokensNet) })},
	{"search_prefix_calls", onM(func(v *Values) *float64 { return fi(v.SearchPrefixCalls) })},
	{"empty_searches", onM(func(v *Values) *float64 { return fi(v.EmptySearches) })},
	{"repeated_searches", onM(func(v *Values) *float64 { return fi(v.RepeatedSearches) })},
	{"files_read_distinct", onM(func(v *Values) *float64 { return fi(v.FilesReadDistinct) })},
	{"files_read_unused", onM(func(v *Values) *float64 { return fi(v.FilesReadUnused) })},
	{"wasted_context_ratio", onM(func(v *Values) *float64 { return ff(v.WastedContextRatio) })},
	{"rereads", onM(func(v *Values) *float64 { return fi(v.Rereads) })},
	{"rereads_cross_session", onM(func(v *Values) *float64 { return fi(v.RereadsCrossSession) })},
	{"rereads_same_range", onM(func(v *Values) *float64 { return fi(v.RereadsSameRange) })},
	{"ranged_reads", onM(func(v *Values) *float64 { return fi(v.RangedReads) })},
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
	{"transient_files", onM(func(v *Values) *float64 {
		if v.TransientFiles == nil {
			return nil
		}
		return fi(len(v.TransientFiles))
	})},
	{"ticket_scoped_test_files", onM(func(v *Values) *float64 {
		if v.TicketScopedTestFiles == nil {
			return nil
		}
		return fi(len(v.TicketScopedTestFiles))
	})},
	{"normalized_work", func(r Row) *float64 {
		if r.Usage == nil {
			return nil
		}
		return fi(r.Usage.NormalizedWork)
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

// CurrentRows splits off the rows an aggregate may use: those whose metrics are under
// the current Version. A stale row (stored under older definitions) keeps its values
// for display but would mix definitions in a median, so it is counted, not folded.
// Rows without metrics pass through: aggregates count them but they carry no values.
func CurrentRows(rows []Row) (current []Row, stale int) {
	for _, r := range rows {
		if r.Metrics != nil && r.MetricsVersion != Version {
			stale++
			continue
		}
		current = append(current, r)
	}
	return current, stale
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

// FileHotspot is one file's read footprint across the selected attempts, for
// `--by file`. It is ranked by context tokens, not calls: one whole-file Read of a
// large file is one call but can be tens of thousands of tokens.
type FileHotspot struct {
	Path          string `json:"path"`
	Attempts      int    `json:"attempts"` // attempts that read it
	Reads         int    `json:"reads"`
	WholeReads    int    `json:"whole_reads"`
	ResultChars   int    `json:"result_chars"`
	ContextTokens int    `json:"context_tokens"`
	UnusedIn      int    `json:"unused_in"` // attempts that read it without editing it or shipping it
	// CrossTicketReaders are the attempts (ticket@attempt) that read the file while
	// its name carries another ticket's id.
	CrossTicketReaders []string `json:"cross_ticket_readers"`
	// LinesAtHead is the file's line count at the codebase's HEAD; null when it is
	// not tracked there (deleted, renamed, or binary).
	LinesAtHead *int `json:"lines_at_head"`
}

// CommandHotspot is one normalized Bash command or search that recurs across
// attempts, for `--by command`.
type CommandHotspot struct {
	Command  string   `json:"command"`
	Attempts int      `json:"attempts"`
	Count    int      `json:"count"`
	Readers  []string `json:"in"` // ticket@attempt, sorted
}

// MinCommandAttempts is how many attempts a command must recur in to be a hotspot.
const MinCommandAttempts = 2

func rowRef(r Row) string { return r.Ticket + "@" + r.Attempt }

// FileHotspots folds rows' per-file stats into a ranking by context tokens (then
// result characters, then path). lines maps a path to its line count at HEAD.
func FileHotspots(rows []Row, lines map[string]int) []FileHotspot {
	by := map[string]*FileHotspot{}
	for _, r := range rows {
		if r.Metrics == nil {
			continue
		}
		for _, f := range r.Metrics.Files {
			h := by[f.Path]
			if h == nil {
				h = &FileHotspot{Path: f.Path, CrossTicketReaders: []string{}}
				by[f.Path] = h
			}
			h.Attempts++
			h.Reads += f.Reads
			h.WholeReads += f.WholeReads
			h.ResultChars += f.ResultChars
			h.ContextTokens += f.ContextTokens
			if f.Unused {
				h.UnusedIn++
			}
			if f.CrossTicket {
				h.CrossTicketReaders = append(h.CrossTicketReaders, rowRef(r))
			}
		}
	}
	out := make([]FileHotspot, 0, len(by))
	for _, h := range by {
		if n, ok := lines[h.Path]; ok {
			h.LinesAtHead = &n
		}
		sort.Strings(h.CrossTicketReaders)
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ContextTokens != b.ContextTokens {
			return a.ContextTokens > b.ContextTokens
		}
		if a.ResultChars != b.ResultChars {
			return a.ResultChars > b.ResultChars
		}
		return a.Path < b.Path
	})
	return out
}

// CommandHotspots returns the normalized commands that ran in at least
// MinCommandAttempts attempts, most widespread first (then most runs, then text).
func CommandHotspots(rows []Row) []CommandHotspot {
	by := map[string]*CommandHotspot{}
	for _, r := range rows {
		if r.Metrics == nil {
			continue
		}
		for _, c := range r.Metrics.Commands {
			h := by[c.Command]
			if h == nil {
				h = &CommandHotspot{Command: c.Command}
				by[c.Command] = h
			}
			h.Attempts++
			h.Count += c.Count
			h.Readers = append(h.Readers, rowRef(r))
		}
	}
	out := []CommandHotspot{}
	for _, h := range by {
		if h.Attempts < MinCommandAttempts {
			continue
		}
		sort.Strings(h.Readers)
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Attempts != b.Attempts {
			return a.Attempts > b.Attempts
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Command < b.Command
	})
	return out
}

// Invention is one normalized file-name pattern that attempts invented: transient
// scratch files (created, then not shipped) or ticket-scoped throwaway tests.
type Invention struct {
	Kind     string   `json:"kind"` // transient | ticket-scoped-test
	Pattern  string   `json:"pattern"`
	Attempts []string `json:"attempts"` // ticket@attempt, sorted
	Files    []string `json:"files"`    // the concrete paths, sorted
}

// Inventions aggregates rows' transient files and ticket-scoped test files by
// NamePattern. Patterns seen in more attempts sort first.
func Inventions(rows []Row) []Invention {
	type key struct{ kind, pat string }
	by := map[key]*Invention{}
	seen := map[key]map[string]bool{}
	add := func(kind, file string, r Row) {
		k := key{kind, NamePattern(file)}
		inv := by[k]
		if inv == nil {
			inv = &Invention{Kind: kind, Pattern: k.pat}
			by[k], seen[k] = inv, map[string]bool{}
		}
		if ref := rowRef(r); !seen[k][ref] {
			seen[k][ref] = true
			inv.Attempts = append(inv.Attempts, ref)
		}
		if !slices.Contains(inv.Files, file) {
			inv.Files = append(inv.Files, file)
		}
	}
	for _, r := range rows {
		if r.Metrics == nil {
			continue
		}
		for _, f := range r.Metrics.TransientFiles {
			add("transient", f, r)
		}
		for _, f := range r.Metrics.TicketScopedTestFiles {
			add("ticket-scoped-test", f, r)
		}
	}
	out := []Invention{}
	for _, inv := range by {
		sort.Strings(inv.Attempts)
		sort.Strings(inv.Files)
		out = append(out, *inv)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if len(a.Attempts) != len(b.Attempts) {
			return len(a.Attempts) > len(b.Attempts)
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Pattern < b.Pattern
	})
	return out
}
