// Package metrics computes drv-054's per-attempt agent-performance metrics from an
// attempt's session transcript, its log and its final diff. It persists them in a
// derived per-attempt file and aggregates them per codebase. The metrics are a sensor
// for how accessible a codebase is to an agent: how much the agent had to explore,
// search, guess and ask before it could act. See docs/metrics.md.
package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Dawil/draiver/internal/store"
)

// Version is the metric-definitions version. Bump it whenever a definition
// changes. A stored record computed under another version reads as `stale`.
//
// It is a var, not a const, so a test can bump it to exercise staleness. Production
// code never assigns it.
var Version = 1

// Source says where a row's metric values came from.
type Source string

const (
	SourceStored   Source = "stored"   // read back from metrics.json, current version
	SourceMissing  Source = "missing"  // never computed
	SourceStale    Source = "stale"    // computed under an older metrics_version
	SourceComputed Source = "computed" // computed by this invocation
)

// Transcript holds the metric values folded from the transcript and the final
// diff. JSON field names are the documented, stable v1 schema (docs/metrics.md).
type Transcript struct {
	ToolCalls    int            `json:"tool_calls"`
	CallsByClass map[string]int `json:"calls_by_class"`

	WallTimeSeconds *float64 `json:"wall_time_s"`

	FinalDiff string `json:"final_diff"` // available | unavailable
	DiffFiles *int   `json:"diff_files"`
	DiffLines *int   `json:"diff_lines"`

	PrefixBasis             string `json:"prefix_basis"` // first-productive-edit | first-edit | no-edit
	ExplorationPrefixCalls  int    `json:"exploration_prefix_calls"`
	ExplorationPrefixTokens int    `json:"exploration_prefix_tokens"`
	SearchPrefixCalls       int    `json:"search_prefix_calls"`

	EmptySearches    int `json:"empty_searches"`
	RepeatedSearches int `json:"repeated_searches"`

	FilesReadDistinct  int      `json:"files_read_distinct"`
	FilesReadUnused    int      `json:"files_read_unused"`
	WastedContextRatio *float64 `json:"wasted_context_ratio"`
	Rereads            int      `json:"rereads"`

	DocsRead          []string `json:"docs_read"`
	FirstDocReadIndex *int     `json:"first_doc_read_index"`

	EnvDiscoveryErrors  int `json:"env_discovery_errors"`
	TestFailures        int `json:"test_failures"`
	BuildFailures       int `json:"build_failures"`
	EditMechanicsErrors int `json:"edit_mechanics_errors"`
	PermissionDenials   int `json:"permission_denials"`
	OtherErrors         int `json:"other_errors"`

	TriesToFirstSuccessfulTest  *int     `json:"tries_to_first_successful_test"`
	TriesToFirstSuccessfulBuild *int     `json:"tries_to_first_successful_build"`
	TokensToFirstGreenTest      *int     `json:"tokens_to_first_green_test"`
	TimeToFirstGreenTestSeconds *float64 `json:"time_to_first_green_test_s"`

	RevertedEdits *int `json:"reverted_edits"`

	// TranscriptInterrupts counts interrupted turns seen in the transcript. It is
	// folded into Values.HumanInterrupts. ModelObserved is the model that produced
	// most assistant messages.
	TranscriptInterrupts int    `json:"-"`
	ModelObserved        string `json:"-"`
}

// Values is one attempt's full v1 metric set: the log-derived outcome family plus
// the transcript family.
type Values struct {
	Outcome          string   `json:"outcome"`
	ReviewRoundTrips int      `json:"review_round_trips"`
	Escalations      int      `json:"escalations"`
	EscalationTexts  []string `json:"escalation_texts"`
	HumanInterrupts  int      `json:"human_interrupts"`
	TranscriptStatus string   `json:"transcript"` // present | absent
	// Transcript is embedded, so its fields flatten into Values' JSON.
	Transcript
}

// Value is a provenance value and how it was obtained: `recorded` (attempt.md),
// `observed` (read from the transcript), `inferred` (derived after the fact, e.g.
// git merge-base), `requested` (the model asked for, not what ran) or `unknown`.
type Value struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// Provenance identifies which codebase version and which agent produced a row.
type Provenance struct {
	BaseCommit     Value  `json:"base_commit"`
	RepoID         Value  `json:"repo_id"`
	Model          Value  `json:"model"`
	DraiverVersion Value  `json:"draiver_version"`
	AdapterVersion string `json:"adapter_version,omitempty"`
	Repo           string `json:"repo,omitempty"`
	Base           string `json:"base,omitempty"`
}

// Record is the persisted metrics.json.
type Record struct {
	MetricsVersion int        `json:"metrics_version"`
	ComputedAt     time.Time  `json:"computed_at"`
	Ticket         string     `json:"ticket"`
	Attempt        string     `json:"attempt"`
	Provenance     Provenance `json:"provenance"`
	Metrics        Values     `json:"metrics"`
}

// Load reads an attempt's metrics.json. ok is false when none exists.
func Load(root store.Root, ticket, att string) (Record, bool, error) {
	data, err := os.ReadFile(root.AttemptMetricsPath(ticket, att))
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("metrics: read %s/%s: %w", ticket, att, err)
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, false, fmt.Errorf("metrics: parse %s/%s metrics.json: %w", ticket, att, err)
	}
	return r, true, nil
}

// Save writes an attempt's metrics.json atomically.
func Save(root store.Root, r Record) error {
	p := root.AttemptMetricsPath(r.Ticket, r.Attempt)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".metrics-*.json")
	if err != nil {
		return fmt.Errorf("metrics: write %s/%s: %w", r.Ticket, r.Attempt, err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}
