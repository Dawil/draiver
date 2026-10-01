// Package cucumber parses and summarises standard cucumber-JSON — the runner-
// agnostic interchange a BDD/acceptance rung emits (drv-016). draiver consumes the
// JSON, not the tool: any BDD runner (godog, cucumber-js, behave, …) that writes
// standard cucumber-JSON is understood here, so the executor never hardcodes a
// runner. It is deliberately a data-model package — parse, fold a summary, answer
// pass/fail — leaving durable artefact storage (drv-017) and HTML rendering
// (drv-018) to consume this model downstream.
//
// The schema is the long-standing cucumber-JSON shape: a top-level array of
// features, each with elements (scenarios/backgrounds), each with steps, each step
// carrying a result status and optional embeddings (screenshots/attachments, base64
// in `data`). Parsing is lenient — unknown fields are ignored, not rejected — so a
// runner that emits extra keys still loads; that tolerance is the whole point of
// consuming a shared format rather than a specific tool's output.
package cucumber

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Report is a parsed cucumber-JSON document: the top-level array of features.
type Report []Feature

// Feature is one `.feature` file's worth of results.
type Feature struct {
	URI         string    `json:"uri"`
	ID          string    `json:"id"`
	Keyword     string    `json:"keyword"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Line        int       `json:"line,omitempty"`
	Tags        []Tag     `json:"tags,omitempty"`
	Elements    []Element `json:"elements,omitempty"`
}

// Element is one scenario (or background) within a feature. Type is "scenario" or
// "background"; backgrounds are not counted as scenarios in a Summary.
type Element struct {
	ID          string `json:"id,omitempty"`
	Keyword     string `json:"keyword"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Line        int    `json:"line,omitempty"`
	Type        string `json:"type"`
	Tags        []Tag  `json:"tags,omitempty"`
	Steps       []Step `json:"steps,omitempty"`
}

// Step is one Given/When/Then step, its result, and any embeddings it attached.
type Step struct {
	Keyword    string      `json:"keyword"`
	Name       string      `json:"name"`
	Line       int         `json:"line,omitempty"`
	Match      *Match      `json:"match,omitempty"`
	Result     Result      `json:"result"`
	Embeddings []Embedding `json:"embeddings,omitempty"`
}

// Match points at the step definition that backed a step (its code location).
type Match struct {
	Location string `json:"location,omitempty"`
}

// Result is a step's verdict. Status is the cucumber status vocabulary: "passed",
// "failed", "skipped", "undefined", "pending", or "ambiguous". Duration is in
// nanoseconds (cucumber-JSON's unit). ErrorMessage is set on a failed step.
type Result struct {
	Status       string `json:"status"`
	Duration     int64  `json:"duration,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// Embedding is an attachment a step captured — a screenshot, a log, a page dump —
// with its MIME type and base64-encoded Data. These are what drv-017 persists and
// drv-018 renders inline; here they are only counted.
type Embedding struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
	Name     string `json:"name,omitempty"`
}

// Tag is a `@tag` on a feature or scenario.
type Tag struct {
	Name string `json:"name"`
	Line int    `json:"line,omitempty"`
}

// Parse decodes standard cucumber-JSON bytes into a Report. It is lenient about
// unknown fields (runners emit many we don't model) but strict about shape: the
// document must be the cucumber top-level array, so an object or other JSON is
// rejected — that is what distinguishes a real cucumber-JSON report from a file a
// misconfigured runner happened to write. An empty array (`[]`) is valid: a run
// that exercised no features.
func Parse(data []byte) (Report, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("parse cucumber-JSON: empty input")
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse cucumber-JSON: %w", err)
	}
	return r, nil
}

// Passed reports whether every step in the report is green — i.e. no step is
// failed/undefined/pending/ambiguous. Passed and skipped steps (and the empty
// status some runners emit for not-run steps) do not fail the report. An empty
// report is vacuously green; the run's own exit code remains the authoritative
// verdict — Passed is a cross-check, not a substitute.
func (r Report) Passed() bool {
	for _, f := range r {
		for _, el := range f.Elements {
			for _, st := range el.Steps {
				if !stepGreen(st.Result.Status) {
					return false
				}
			}
		}
	}
	return true
}

func stepGreen(status string) bool {
	switch status {
	case "passed", "skipped", "":
		return true
	default:
		return false
	}
}

// Summary is a folded count over a Report: features, scenarios (excluding
// backgrounds), steps, the per-status step tally, scenario pass/fail, and the total
// number of embeddings captured.
type Summary struct {
	Features        int
	Scenarios       int
	ScenariosPassed int
	ScenariosFailed int
	Steps           int
	StepStatus      map[string]int
	Embeddings      int
}

// Summary folds the report into counts. A scenario is "passed" when all of its own
// steps are green (see stepGreen); backgrounds are not scenarios but their steps are
// counted toward Steps/StepStatus/Embeddings.
func (r Report) Summary() Summary {
	s := Summary{StepStatus: map[string]int{}}
	for _, f := range r {
		s.Features++
		for _, el := range f.Elements {
			isScenario := el.Type != "background"
			ok := true
			for _, st := range el.Steps {
				s.Steps++
				status := st.Result.Status
				if status == "" {
					status = "unknown"
				}
				s.StepStatus[status]++
				s.Embeddings += len(st.Embeddings)
				if !stepGreen(st.Result.Status) {
					ok = false
				}
			}
			if isScenario {
				s.Scenarios++
				if ok {
					s.ScenariosPassed++
				} else {
					s.ScenariosFailed++
				}
			}
		}
	}
	return s
}

// String renders a one-line, deterministic summary for `draiver test` output, e.g.
//
//	2 features, 3 scenarios (2 passed, 1 failed), 15 steps [passed 12, failed 1, skipped 2], 1 embedding
//
// The per-status breakdown is sorted by status name so the line is stable across
// runs. The embeddings clause is omitted when there are none.
func (s Summary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s, %s", plural(s.Features, "feature"), plural(s.Scenarios, "scenario"))
	if s.Scenarios > 0 {
		fmt.Fprintf(&b, " (%d passed, %d failed)", s.ScenariosPassed, s.ScenariosFailed)
	}
	fmt.Fprintf(&b, ", %s", plural(s.Steps, "step"))
	if len(s.StepStatus) > 0 {
		keys := make([]string, 0, len(s.StepStatus))
		for k := range s.StepStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, s.StepStatus[k]))
		}
		fmt.Fprintf(&b, " [%s]", strings.Join(parts, ", "))
	}
	if s.Embeddings > 0 {
		fmt.Fprintf(&b, ", %s", plural(s.Embeddings, "embedding"))
	}
	return b.String()
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
