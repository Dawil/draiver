// Package report renders a captured BDD run — standard cucumber-JSON plus its
// embedded screenshots/attachments — into a single self-contained HTML report
// (drv-018). It is deliberately *runner-agnostic*: it is driven by the cucumber
// JSON schema that every mainstream runner (cucumber-ruby/js/jvm, behave,
// godog, …) can emit, never by any one tool's native format. The renderer
// produces a standalone file with inlined CSS/JS and base64-embedded images so
// it opens offline (the standalone download of drv-019), and also supports a
// by-reference mode so the webui can serve heavy embeddings as artefacts rather
// than inlining megabytes into one page (drv-017's note on store growth).
//
// This file holds the data model and a tolerant parser; render.go holds the
// HTML renderer; discover.go locates captured runs under an attempt's artefact
// store against drv-017's per-run key contract.
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Status is a cucumber step/scenario result status. The zero value is the empty
// string, which renders and aggregates as Skipped-equivalent "unknown".
type Status string

const (
	StatusPassed    Status = "passed"
	StatusFailed    Status = "failed"
	StatusSkipped   Status = "skipped"
	StatusPending   Status = "pending"
	StatusUndefined Status = "undefined"
	StatusAmbiguous Status = "ambiguous"
)

// severity orders statuses so a scenario can roll up to its worst step. Higher
// is worse; an unrecognised status sorts just above passed so it is visible but
// never masks a real failure.
func (s Status) severity() int {
	switch s {
	case StatusFailed:
		return 6
	case StatusUndefined:
		return 5
	case StatusAmbiguous:
		return 4
	case StatusPending:
		return 3
	case StatusSkipped:
		return 2
	case StatusPassed:
		return 1
	case "":
		return 0
	default:
		return 1 // unknown but present — treat like passed for rollup, still shown
	}
}

// Report is a parsed cucumber-JSON document: an ordered list of features.
type Report struct {
	Features []Feature
}

// Feature is one .feature file's worth of results.
type Feature struct {
	URI         string    `json:"uri"`
	ID          string    `json:"id"`
	Keyword     string    `json:"keyword"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Line        int       `json:"line"`
	Tags        []Tag     `json:"tags"`
	Elements    []Element `json:"elements"`
}

// Element is a scenario, scenario outline example, or background within a feature.
type Element struct {
	ID          string `json:"id"`
	Keyword     string `json:"keyword"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Line        int    `json:"line"`
	Type        string `json:"type"` // "scenario" | "background" | ...
	Tags        []Tag  `json:"tags"`
	Before      []Hook `json:"before"`
	Steps       []Step `json:"steps"`
	After       []Hook `json:"after"`
}

// Tag is a gherkin tag (e.g. @smoke).
type Tag struct {
	Name string `json:"name"`
	Line int    `json:"line"`
}

// Step is one gherkin step and its result, with any attachments.
type Step struct {
	Keyword    string      `json:"keyword"`
	Name       string      `json:"name"`
	Line       int         `json:"line"`
	Hidden     bool        `json:"hidden"`
	Result     Result      `json:"result"`
	Match      Match       `json:"match"`
	Rows       []Row       `json:"rows"`
	DocString  *DocString  `json:"doc_string"`
	Output     []string    `json:"output"`
	Embeddings []Embedding `json:"embeddings"`
}

// Hook is a before/after hook attached to a scenario. It carries no gherkin text
// but can still fail and can carry embeddings (e.g. a screenshot-on-failure
// after-hook), so it renders like a step.
type Hook struct {
	Match      Match       `json:"match"`
	Result     Result      `json:"result"`
	Output     []string    `json:"output"`
	Embeddings []Embedding `json:"embeddings"`
}

// Result is a step/hook outcome: status, wall-clock duration (nanoseconds, per
// the cucumber schema) and an optional error message for failures.
type Result struct {
	Status       Status `json:"status"`
	Duration     int64  `json:"duration"` // nanoseconds
	ErrorMessage string `json:"error_message"`
}

// Match points at the glue code location that ran a step.
type Match struct {
	Location string `json:"location"`
}

// Row is one row of a step's data table.
type Row struct {
	Cells []string `json:"cells"`
}

// DocString is a step's attached doc string (triple-quoted block).
type DocString struct {
	ContentType string `json:"content_type"`
	Value       string `json:"value"`
	Line        int    `json:"line"`
}

// Embedding is a base64 attachment on a step or hook — a screenshot, a page
// source dump, a log. Cucumber dialects disagree on the field names: most use
// {mime_type, data}, some older/JVM ones nest {media:{type}, data}. UnmarshalJSON
// accepts both so the renderer stays runner-agnostic.
type Embedding struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64, no data: prefix
	Name     string `json:"name"`
}

// UnmarshalJSON tolerates both the {mime_type,...} and {media:{type},...}
// spellings of an embedding.
func (e *Embedding) UnmarshalJSON(b []byte) error {
	var raw struct {
		MimeType string `json:"mime_type"`
		Data     string `json:"data"`
		Name     string `json:"name"`
		Media    struct {
			Type string `json:"type"`
		} `json:"media"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	e.Data = raw.Data
	e.Name = raw.Name
	e.MimeType = raw.MimeType
	if e.MimeType == "" {
		e.MimeType = raw.Media.Type
	}
	if e.MimeType == "" {
		e.MimeType = "application/octet-stream"
	}
	return nil
}

// Parse decodes a cucumber-JSON document (the top-level array of features). It is
// lenient: unknown fields are ignored and an empty document parses to an empty
// report rather than an error, so a run that produced no features still renders.
func Parse(data []byte) (*Report, error) {
	if len(trimSpace(data)) == 0 {
		return &Report{}, nil
	}
	var features []Feature
	if err := json.Unmarshal(data, &features); err != nil {
		return nil, fmt.Errorf("report: parse cucumber-JSON: %w", err)
	}
	return &Report{Features: features}, nil
}

// trimSpace reports the content with leading/trailing JSON whitespace removed,
// without allocating a new string for the common non-empty case.
func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && isSpace(b[i]) {
		i++
	}
	for j > i && isSpace(b[j-1]) {
		j--
	}
	return b[i:j]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// --- derived rollups ---------------------------------------------------------

// Status rolls a scenario up to the worst status among its hooks and steps.
func (el Element) Status() Status {
	worst := Status("")
	consider := func(r Result) {
		if r.Status.severity() > worst.severity() {
			worst = r.Status
		}
	}
	for _, h := range el.Before {
		consider(h.Result)
	}
	for _, s := range el.Steps {
		consider(s.Result)
	}
	for _, h := range el.After {
		consider(h.Result)
	}
	if worst == "" {
		return StatusSkipped
	}
	return worst
}

// Duration totals the scenario's step and hook durations.
func (el Element) Duration() time.Duration {
	var ns int64
	for _, h := range el.Before {
		ns += h.Result.Duration
	}
	for _, s := range el.Steps {
		ns += s.Result.Duration
	}
	for _, h := range el.After {
		ns += h.Result.Duration
	}
	return time.Duration(ns)
}

// IsScenario reports whether the element is a real scenario (not a background),
// so summary counts don't double-count shared backgrounds.
func (el Element) IsScenario() bool {
	return el.Type == "" || el.Type == "scenario" || el.Type == "scenario_outline"
}

// Summary is the aggregate shown at the top of a report.
type Summary struct {
	Features          int
	Scenarios         int
	ScenariosByStatus map[Status]int
	Steps             int
	StepsByStatus     map[Status]int
	Duration          time.Duration
}

// Passed reports whether every counted scenario passed — the one-glance verdict.
func (s Summary) Passed() bool {
	return s.Scenarios > 0 && s.ScenariosByStatus[StatusPassed] == s.Scenarios
}

// Summarize computes the aggregate rollup across all features.
func (r *Report) Summarize() Summary {
	sum := Summary{
		ScenariosByStatus: map[Status]int{},
		StepsByStatus:     map[Status]int{},
	}
	sum.Features = len(r.Features)
	for _, f := range r.Features {
		for _, el := range f.Elements {
			tally := func(res Result) {
				sum.Steps++
				sum.StepsByStatus[res.Status]++
				sum.Duration += time.Duration(res.Duration)
			}
			for _, h := range el.Before {
				tally(h.Result)
			}
			for _, s := range el.Steps {
				tally(s.Result)
			}
			for _, h := range el.After {
				tally(h.Result)
			}
			if el.IsScenario() {
				sum.Scenarios++
				sum.ScenariosByStatus[el.Status()]++
			}
		}
	}
	return sum
}

// orderedStatuses returns the known statuses present in a count map, worst-first,
// so the summary bar reads failed→…→passed consistently.
func orderedStatuses(counts map[Status]int) []Status {
	var present []Status
	for st, n := range counts {
		if n > 0 {
			present = append(present, st)
		}
	}
	sort.Slice(present, func(i, j int) bool {
		return present[i].severity() > present[j].severity()
	})
	return present
}
