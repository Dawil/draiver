package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/agent/claudecode"
	"github.com/Dawil/draiver/internal/metrics"
)

var (
	metricsCodebase       string
	metricsStored         bool
	metricsCompute        bool
	metricsRecompute      bool
	metricsJSON           bool
	metricsIncludeRunning bool
	metricsSince          string
	metricsSort           string
	metricsLimit          int
	metricsBy             string
)

var metricsCmd = &cobra.Command{
	Use:   "metrics [TICKET | --codebase PATH]",
	Short: "Per-attempt and per-codebase agent-performance metrics from session transcripts",
	Long: "metrics reports how accessible a codebase was to the agents that worked it: how\n" +
		"much they explored, searched, guessed and asked before they could act (drv-054).\n" +
		"Metrics are folded from each attempt's session transcript, its log and its final\n" +
		"diff, and persisted per attempt in attempts/<id>/metrics.json (derived, outside\n" +
		"the hash chain, safe to delete).\n\n" +
		"Targets (exactly one):\n" +
		"  draiver metrics TICKET [--attempt A]   one attempt (default: the latest)\n" +
		"  draiver metrics --codebase PATH        every attempt on PATH's repository,\n" +
		"                                         with an aggregate grouped by model\n\n" +
		"Computed vs stored is explicit:\n" +
		"  --stored (default)  return only persisted values; never compute. Each row's\n" +
		"                      source is stored, missing or stale.\n" +
		"  --compute           compute missing or stale attempts, persist, return all.\n" +
		"  --recompute         recompute every selected attempt.\n\n" +
		"Running attempts are excluded unless --include-running. Their numbers are then\n" +
		"marked partial and never persisted. --json emits the stable schema documented in\n" +
		"docs/metrics.md. Every row carries the `draiver ctl logs` command for its transcript.",
	Args: cobra.MaximumNArgs(1),
	RunE: runMetrics,
}

func init() {
	f := metricsCmd.Flags()
	f.StringVar(&metricsCodebase, "codebase", "", "select every attempt whose repository is the one containing PATH")
	f.BoolVar(&metricsStored, "stored", false, "return only persisted metrics, never compute (the default)")
	f.BoolVar(&metricsCompute, "compute", false, "compute and persist metrics for missing or stale attempts")
	f.BoolVar(&metricsRecompute, "recompute", false, "recompute and persist metrics for every selected attempt")
	f.BoolVar(&metricsJSON, "json", false, "emit JSON (stable schema, see docs/metrics.md)")
	f.BoolVar(&metricsIncludeRunning, "include-running", false, "include running attempts, marked partial and never persisted")
	f.StringVar(&metricsSince, "since", "", "codebase: only attempts started on or after DATE (YYYY-MM-DD or RFC3339)")
	f.StringVar(&metricsSort, "sort", "", "codebase: sort rows worst-first by METRIC (see docs/metrics.md)")
	f.IntVar(&metricsLimit, "limit", 0, "codebase: show at most N rows (after --sort)")
	f.StringVar(&metricsBy, "by", "", "codebase: hotspot view instead of attempt rows: file (ranked by context tokens) or command (recurring across attempts)")
	rootCmd.AddCommand(metricsCmd)
}

// metricsFlagsReset clears the verb's flags; the cmd tests run many invocations in
// one process.
func metricsFlagsReset() {
	metricsCodebase, metricsSince, metricsSort, metricsBy = "", "", "", ""
	metricsStored, metricsCompute, metricsRecompute, metricsJSON, metricsIncludeRunning = false, false, false, false, false
	metricsLimit = 0
}

func runMetrics(cmd *cobra.Command, args []string) error {
	defer metricsFlagsReset()
	mode, err := metricsMode()
	if err != nil {
		return err
	}
	if (len(args) == 1) == (metricsCodebase != "") {
		return fmt.Errorf("metrics: give exactly one target, a TICKET or --codebase PATH")
	}
	if len(args) == 1 && (metricsSince != "" || metricsSort != "" || metricsLimit != 0 || metricsBy != "") {
		return fmt.Errorf("metrics: --since, --sort, --limit and --by apply to --codebase only")
	}
	if metricsBy != "" && metricsBy != "file" && metricsBy != "command" {
		return fmt.Errorf("metrics: --by must be file or command, not %q", metricsBy)
	}
	if metricsBy != "" && metricsSort != "" {
		return fmt.Errorf("metrics: --by views have a fixed ranking; drop --sort")
	}
	root, err := resolveRoot()
	if err != nil {
		return err
	}
	opts := metrics.Options{Mode: mode, IncludeRunning: metricsIncludeRunning, Normalize: claudecode.Normalize}
	out := cmd.OutOrStdout()

	if len(args) == 1 {
		ticket, att, err := resolveTarget(root, args[0])
		if err != nil {
			return err
		}
		row, err := metrics.Build(root, ticket, att, opts)
		if err != nil {
			return err
		}
		if row.Running() && !metricsIncludeRunning {
			return fmt.Errorf("metrics: %s@%s is still running, so its numbers are partial; pass --include-running to see them", ticket, att)
		}
		if metricsJSON {
			return writeJSON(out, attemptReport{
				Target: attemptTarget{Ticket: ticket, Attempt: att}, Mode: mode,
				MetricsVersion: metrics.Version, Row: row,
			})
		}
		renderAttempt(out, row)
		return nil
	}

	var sortBy *metrics.Numeric
	if metricsSort != "" {
		n, ok := metrics.LookupNumeric(metricsSort)
		if !ok {
			return fmt.Errorf("metrics: unknown --sort metric %q (one of: %s)", metricsSort, numericNames())
		}
		sortBy = &n
	}
	var since time.Time
	if metricsSince != "" {
		if since, err = parseSince(metricsSince); err != nil {
			return err
		}
	}
	if metricsLimit < 0 {
		return fmt.Errorf("metrics: --limit must be >= 0")
	}
	cb, err := metrics.ResolveCodebase(metricsCodebase)
	if err != nil {
		return err
	}
	refs, err := metrics.SelectCodebase(root, cb, since)
	if err != nil {
		return err
	}
	rep := codebaseReport{
		Target: cb, Mode: mode, MetricsVersion: metrics.Version,
		Filters: codebaseFilters{Since: metricsSince, Sort: metricsSort, Limit: metricsLimit, IncludeRunning: metricsIncludeRunning},
		Rows:    []metrics.Row{},
	}
	for _, r := range refs {
		row, err := metrics.Build(root, r.Ticket, r.Attempt, opts)
		if err != nil {
			return fmt.Errorf("metrics: %s@%s: %w", r.Ticket, r.Attempt, err)
		}
		if row.Running() && !metricsIncludeRunning {
			rep.ExcludedRunning++
			continue
		}
		rep.Rows = append(rep.Rows, row)
	}
	if metricsBy != "" {
		return runHotspots(out, cb, mode, rep)
	}
	current, stale := metrics.CurrentRows(rep.Rows)
	rep.Aggregate.ExcludedStale = stale
	rep.Aggregate.Groups = metrics.Aggregate(current)
	rep.Aggregate.Inventions = metrics.Inventions(current)
	if docs, err := metrics.UnreadDocs(cb, current); err == nil {
		rep.Aggregate.UnreadDocs = docs
	}
	rep.Total = len(rep.Rows)
	if sortBy != nil {
		metrics.SortRows(rep.Rows, *sortBy)
	}
	if metricsLimit > 0 && len(rep.Rows) > metricsLimit {
		rep.Rows = rep.Rows[:metricsLimit]
	}
	if metricsJSON {
		return writeJSON(out, rep)
	}
	renderCodebase(out, rep, sortBy)
	return nil
}

func metricsMode() (metrics.Mode, error) {
	n := 0
	mode := metrics.ModeStored
	for _, f := range []struct {
		on bool
		m  metrics.Mode
	}{{metricsStored, metrics.ModeStored}, {metricsCompute, metrics.ModeCompute}, {metricsRecompute, metrics.ModeRecompute}} {
		if f.on {
			n++
			mode = f.m
		}
	}
	if n > 1 {
		return "", fmt.Errorf("metrics: --stored, --compute and --recompute are mutually exclusive")
	}
	return mode, nil
}

func parseSince(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("metrics: --since %q is not YYYY-MM-DD or RFC3339", s)
}

func numericNames() string {
	var names []string
	for _, n := range metrics.NumericMetrics {
		names = append(names, n.Name)
	}
	return strings.Join(names, ", ")
}

// --- JSON shapes (documented in docs/metrics.md) ---

type attemptTarget struct {
	Ticket  string `json:"ticket"`
	Attempt string `json:"attempt"`
}

type attemptReport struct {
	Target         attemptTarget `json:"target"`
	Mode           metrics.Mode  `json:"mode"`
	MetricsVersion int           `json:"metrics_version"`
	Row            metrics.Row   `json:"row"`
}

type codebaseFilters struct {
	Since          string `json:"since,omitempty"`
	Sort           string `json:"sort,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeRunning bool   `json:"include_running"`
}

type codebaseReport struct {
	Target          metrics.Codebase `json:"target"`
	Mode            metrics.Mode     `json:"mode"`
	MetricsVersion  int              `json:"metrics_version"`
	Filters         codebaseFilters  `json:"filters"`
	Total           int              `json:"total"`
	ExcludedRunning int              `json:"excluded_running"`
	Aggregate       struct {
		Groups     []metrics.Group `json:"groups"`
		UnreadDocs []string        `json:"unread_docs"`
		// ExcludedStale counts rows whose stored values are under an older
		// metrics_version: they are listed, but left out of every aggregate.
		ExcludedStale int                 `json:"excluded_stale"`
		Inventions    []metrics.Invention `json:"inventions"`
	} `json:"aggregate"`
	Rows []metrics.Row `json:"rows"`
}

// hotspotReport is the --by view: a codebase-wide ranking instead of attempt rows.
type hotspotReport struct {
	Target         metrics.Codebase         `json:"target"`
	Mode           metrics.Mode             `json:"mode"`
	MetricsVersion int                      `json:"metrics_version"`
	By             string                   `json:"by"`
	Filters        codebaseFilters          `json:"filters"`
	Attempts       int                      `json:"attempts"`       // selected attempts
	WithMetrics    int                      `json:"with_metrics"`   // rows with current-version metrics
	ExcludedStale  int                      `json:"excluded_stale"` // rows with older-version metrics, left out
	Files          []metrics.FileHotspot    `json:"files,omitempty"`
	Commands       []metrics.CommandHotspot `json:"commands,omitempty"`
}

func runHotspots(out io.Writer, cb metrics.Codebase, mode metrics.Mode, rep codebaseReport) error {
	h := hotspotReport{
		Target: cb, Mode: mode, MetricsVersion: metrics.Version, By: metricsBy,
		Filters:  codebaseFilters{Since: metricsSince, Limit: metricsLimit, IncludeRunning: metricsIncludeRunning},
		Attempts: len(rep.Rows),
	}
	current, stale := metrics.CurrentRows(rep.Rows)
	h.ExcludedStale = stale
	for _, r := range current {
		if r.Metrics != nil {
			h.WithMetrics++
		}
	}
	switch metricsBy {
	case "file":
		lines, _ := metrics.LinesAtHead(cb.Toplevel)
		h.Files = metrics.FileHotspots(current, lines)
		if metricsLimit > 0 && len(h.Files) > metricsLimit {
			h.Files = h.Files[:metricsLimit]
		}
		if h.Files == nil {
			h.Files = []metrics.FileHotspot{}
		}
	case "command":
		h.Commands = metrics.CommandHotspots(current)
		if metricsLimit > 0 && len(h.Commands) > metricsLimit {
			h.Commands = h.Commands[:metricsLimit]
		}
	}
	if metricsJSON {
		return writeJSON(out, h)
	}
	renderHotspots(out, h)
	return nil
}

func renderHotspots(w io.Writer, h hotspotReport) {
	fmt.Fprintf(w, "codebase %s\nrepo_id  %s\n", h.Target.Toplevel, h.Target.RepoID)
	fmt.Fprintf(w, "%d attempt(s), %d with metrics, mode %s, metrics_version %d\n", h.Attempts, h.WithMetrics, h.Mode, h.MetricsVersion)
	if h.ExcludedStale > 0 {
		fmt.Fprintf(w, "%d attempt(s) left out: stored under an older metrics_version (use --compute)\n", h.ExcludedStale)
	}
	if h.WithMetrics == 0 {
		fmt.Fprintln(w, "\nno current metrics to rank. Run with --compute to compute and persist them.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	switch h.By {
	case "file":
		fmt.Fprintln(w, "\nfiles by context tokens:")
		fmt.Fprintln(tw, "  PATH\tCONTEXT_TOKENS\tRESULT_CHARS\tATTEMPTS\tREADS\tWHOLE\tUNUSED_IN\tLINES@HEAD\tCROSS_TICKET_READERS")
		for _, f := range h.Files {
			fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n", f.Path, f.ContextTokens, f.ResultChars, f.Attempts,
				f.Reads, f.WholeReads, f.UnusedIn, intOr(f.LinesAtHead), listOr(f.CrossTicketReaders))
		}
	case "command":
		fmt.Fprintf(w, "\ncommands recurring across >= %d attempts:\n", metrics.MinCommandAttempts)
		if len(h.Commands) == 0 {
			fmt.Fprintln(w, "  none")
		}
		fmt.Fprintln(tw, "  COMMAND\tATTEMPTS\tRUNS\tIN")
		for _, c := range h.Commands {
			fmt.Fprintf(tw, "  %s\t%d\t%d\t%s\n", c.Command, c.Attempts, c.Count, strings.Join(c.Readers, ", "))
		}
	}
	tw.Flush()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- human rendering ---

func renderAttempt(w io.Writer, r metrics.Row) {
	fmt.Fprintf(w, "%s@%s  metrics  source: %s%s\n", r.Ticket, r.Attempt, r.Source, partialNote(r))
	if r.ComputedAt != nil {
		fmt.Fprintf(w, "metrics_version %d, computed %s\n", r.MetricsVersion, r.ComputedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "transcript: %s\n", r.TranscriptCmd)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	section := func(name string) { fmt.Fprintf(tw, "\n%s\t\n", name) }
	kv := func(k, v string) { fmt.Fprintf(tw, "  %s\t%s\n", k, v) }

	section("provenance")
	p := r.Provenance
	kv("base_commit", provValue(p.BaseCommit))
	kv("repo_id", provValue(p.RepoID))
	kv("model", provValue(p.Model))
	kv("draiver_version", provValue(p.DraiverVersion))
	if p.AdapterVersion != "" {
		kv("adapter_version", p.AdapterVersion)
	}

	if u := r.Usage; u != nil {
		section("tokens (from attempt.md / meter)")
		kv("input / output", fmt.Sprintf("%d / %d", u.InputTokens, u.OutputTokens))
		kv("cache read / write", fmt.Sprintf("%d / %d", u.CacheReadTokens, u.CacheCreationTokens))
		kv("normalized_work", fmt.Sprint(u.NormalizedWork))
	}

	m := r.Metrics
	if m == nil {
		tw.Flush()
		fmt.Fprintf(w, "\nno metrics stored (source: %s). Run with --compute to compute and persist them.\n", r.Source)
		return
	}
	section("outcome")
	kv("outcome", m.Outcome)
	kv("review_round_trips", fmt.Sprint(m.ReviewRoundTrips))
	kv("escalations", fmt.Sprint(m.Escalations))
	kv("human_interrupts", fmt.Sprint(m.HumanInterrupts))
	kv("context_auto_stops", fmt.Sprint(m.ContextAutoStops))
	for i, rt := range m.RoundTrips {
		kv(fmt.Sprintf("round_trip %d (review #%d)", i+1, rt.ReviewSeq),
			fmt.Sprintf("%d human decision/note event(s), %d superseded", rt.HumanEvents, rt.HumanSuperseded))
	}

	section("sessions")
	kv("sessions / segments", fmt.Sprintf("%d / %d", m.SessionCount, m.Segments))
	kv("context_floor", intOr(m.ContextFloor))
	kv("compactions", fmt.Sprint(m.Compactions))
	for i, ss := range m.Sessions {
		kv(fmt.Sprintf("session %d", i+1), fmt.Sprintf("%d segment(s), %d call(s), context start %s peak %s at-first-edit %s, ended_by %s",
			ss.Segments, ss.Calls, intOr(ss.StartContext), intOr(ss.PeakContext), intOr(ss.ContextAtFirstProductiveEdit), ss.EndedBy))
	}

	section("totals")
	kv("transcript", m.TranscriptStatus)
	kv("wall_time", durationOr(m.WallTimeSeconds))
	kv("active_time", durationOr(m.ActiveTimeSeconds))
	kv("tool_calls", fmt.Sprintf("%d  (%s)", m.ToolCalls, classBreakdown(m.CallsByClass)))
	kv("final_diff", m.FinalDiff)
	kv("diff_files / diff_lines", intOr(m.DiffFiles)+" / "+intOr(m.DiffLines))

	section("exploration & context")
	kv("prefix_basis", m.PrefixBasis)
	kv("exploration_prefix_calls", fmt.Sprint(m.ExplorationPrefixCalls))
	kv("exploration_prefix_tokens", fmt.Sprint(m.ExplorationPrefixTokens))
	kv("exploration_prefix_tokens_net", intOr(m.ExplorationPrefixTokensNet))
	kv("search_prefix_calls", fmt.Sprint(m.SearchPrefixCalls))
	kv("empty_searches", fmt.Sprint(m.EmptySearches))
	kv("repeated_searches", fmt.Sprint(m.RepeatedSearches))
	kv("files_read_distinct", fmt.Sprint(m.FilesReadDistinct))
	kv("files_read_unused", fmt.Sprint(m.FilesReadUnused))
	kv("wasted_context_ratio", floatOr(m.WastedContextRatio, "%.2f"))
	kv("rereads", fmt.Sprintf("%d  (cross-session %d, same range %d; ranged reads %d)", m.Rereads, m.RereadsCrossSession, m.RereadsSameRange, m.RangedReads))
	kv("docs_read", listOr(m.DocsRead))
	kv("first_doc_read_index", intOr(m.FirstDocReadIndex))

	section("errors & discovery")
	kv("env_discovery_errors", fmt.Sprint(m.EnvDiscoveryErrors))
	kv("test_failures", fmt.Sprint(m.TestFailures))
	kv("build_failures", fmt.Sprint(m.BuildFailures))
	kv("edit_mechanics_errors", fmt.Sprint(m.EditMechanicsErrors))
	kv("permission_denials", fmt.Sprint(m.PermissionDenials))
	kv("other_errors", fmt.Sprint(m.OtherErrors))
	kv("tries_to_first_successful_test", intOr(m.TriesToFirstSuccessfulTest))
	kv("tries_to_first_successful_build", intOr(m.TriesToFirstSuccessfulBuild))
	kv("tokens_to_first_green_test", intOr(m.TokensToFirstGreenTest))
	kv("time_to_first_green_test", durationOr(m.TimeToFirstGreenTestSeconds))
	kv("reverted_edits", intOr(m.RevertedEdits))

	section("invention")
	kv("transient_files", listOrDash(m.TransientFiles))
	kv("ticket_scoped_test_files", listOrDash(m.TicketScopedTestFiles))
	tw.Flush()
	if len(m.EscalationTexts) > 0 {
		fmt.Fprintf(w, "\nescalation texts are in --json (%d).\n", len(m.EscalationTexts))
	}
}

// codebaseColumns are the per-row columns of the human codebase table.
var codebaseColumns = []string{"exploration_prefix_calls", "search_prefix_calls", "env_discovery_errors", "files_read_unused", "tool_calls"}

func renderCodebase(w io.Writer, rep codebaseReport, sortBy *metrics.Numeric) {
	fmt.Fprintf(w, "codebase %s\nrepo_id  %s\n", rep.Target.Toplevel, rep.Target.RepoID)
	fmt.Fprintf(w, "%d attempt(s), mode %s, metrics_version %d", rep.Total, rep.Mode, rep.MetricsVersion)
	if rep.ExcludedRunning > 0 {
		fmt.Fprintf(w, ", %d running attempt(s) excluded (use --include-running)", rep.ExcludedRunning)
	}
	fmt.Fprintln(w)
	if rep.Total == 0 {
		fmt.Fprintln(w, "\nno attempts match this codebase.")
		return
	}

	for _, g := range rep.Aggregate.Groups {
		fmt.Fprintf(w, "\nmodel %s: %d attempt(s), %d with metrics", g.Model, g.Count, g.WithMetrics)
		if len(g.Outcomes) > 0 {
			fmt.Fprintf(w, "; outcomes: %s", countsLine(g.Outcomes))
		}
		fmt.Fprintln(w)
		if len(g.Metrics) == 0 {
			continue
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  METRIC\tN\tMEDIAN\tP90")
		for _, n := range metrics.NumericMetrics {
			if s, ok := g.Metrics[n.Name]; ok {
				fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\n", n.Name, s.N, num(s.Median), num(s.P90))
			}
		}
		tw.Flush()
	}

	if rep.Aggregate.ExcludedStale > 0 {
		fmt.Fprintf(w, "\n%d attempt(s) left out of the aggregates: stored under an older metrics_version (use --compute)\n", rep.Aggregate.ExcludedStale)
	}
	fmt.Fprintf(w, "\nunread docs (in the repo at HEAD, read by no selected attempt): %s\n", listOr(rep.Aggregate.UnreadDocs))
	if len(rep.Aggregate.Inventions) > 0 {
		fmt.Fprintln(w, "\ninvented files (by name pattern):")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, inv := range rep.Aggregate.Inventions {
			fmt.Fprintf(tw, "  %s\t%s\t%d attempt(s): %s\n", inv.Kind, inv.Pattern, len(inv.Attempts), strings.Join(inv.Attempts, ", "))
		}
		tw.Flush()
	}

	cols := codebaseColumns
	title := "attempts"
	if sortBy != nil {
		cols = append([]string{sortBy.Name}, without(cols, sortBy.Name)...)
		title = "attempts, worst first by " + sortBy.Name
	}
	if rep.Filters.Limit > 0 {
		title += fmt.Sprintf(" (limit %d)", rep.Filters.Limit)
	}
	fmt.Fprintf(w, "\n%s:\n", title)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  ATTEMPT\tSOURCE\tMODEL\tOUTCOME\t%s\tTRANSCRIPT\n", strings.ToUpper(strings.Join(cols, "\t")))
	for _, r := range rep.Rows {
		outcome := "-"
		if r.Metrics != nil {
			outcome = r.Metrics.Outcome
		}
		var vals []string
		for _, c := range cols {
			n, _ := metrics.LookupNumeric(c)
			vals = append(vals, numPtr(n.Get(r)))
		}
		model := r.Provenance.Model.Value
		if model == "" {
			model = "unknown"
		}
		fmt.Fprintf(tw, "  %s@%s\t%s%s\t%s\t%s\t%s\t%s\n", r.Ticket, r.Attempt, r.Source, partialNote(r), model, outcome,
			strings.Join(vals, "\t"), r.TranscriptCmd)
	}
	tw.Flush()
}

func partialNote(r metrics.Row) string {
	if r.Partial {
		return " (partial)"
	}
	return ""
}

func provValue(v metrics.Value) string {
	if v.Value == "" {
		return "unknown"
	}
	return fmt.Sprintf("%s  (%s)", v.Value, v.Source)
}

func classBreakdown(m map[string]int) string {
	var parts []string
	for _, c := range []metrics.Class{metrics.ClassRead, metrics.ClassSearch, metrics.ClassEdit, metrics.ClassBuild, metrics.ClassTest, metrics.ClassVCS, metrics.ClassSubagent, metrics.ClassOther} {
		if n := m[string(c)]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", c, n))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func countsLine(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func intOr(p *int) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprint(*p)
}

func floatOr(p *float64, format string) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf(format, *p)
}

func durationOr(p *float64) string {
	if p == nil {
		return "-"
	}
	return (time.Duration(*p * float64(time.Second))).Round(time.Second).String()
}

func listOr(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}

// listOrDash is listOr, but "-" when the list is not applicable (nil: no final diff).
func listOrDash(l []string) string {
	if l == nil {
		return "-"
	}
	return listOr(l)
}

func num(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprint(int64(f))
	}
	return fmt.Sprintf("%.2f", f)
}

func numPtr(p *float64) string {
	if p == nil {
		return "-"
	}
	return num(*p)
}

func without(l []string, x string) []string {
	var out []string
	for _, v := range l {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
