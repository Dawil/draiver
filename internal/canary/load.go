package canary

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
)

// Observe reads the store and builds one Observation per attempt that targets a
// repo. It is strictly read-only: it reads meter.json and stream.jsonl through the
// path builders rather than session.Open (which would create session dirs), so a
// canary run never mutates the data root. Attempts without a repo, or without any
// metered session, are skipped — there is nothing to judge.
func Observe(root store.Root) ([]Observation, error) {
	attempts, err := project.LoadAll(root)
	if err != nil {
		return nil, err
	}
	var out []Observation
	for _, a := range attempts {
		if a.Repo == "" {
			continue
		}
		tot, hasTot := loadTotals(root, a)
		first, hasFirst := loadFirstTurn(root, a.Ticket, a.ID)
		if !hasTot && !hasFirst {
			continue // never ran / no telemetry
		}
		out = append(out, Observation{
			Repo:           a.Repo,
			Ticket:         a.Ticket,
			Attempt:        a.ID,
			StartedAt:      startedAt(a),
			FirstTurn:      first,
			HasFirstTurn:   hasFirst,
			Totals:         tot,
			AdapterVersion: adapterVersion(root, a.Ticket, a.ID),
		})
	}
	return out, nil
}

// startedAt is the attempt's first (created) event time; the log is seq-ordered.
func startedAt(a project.Attempt) time.Time {
	if len(a.Events) > 0 {
		return a.Events[0].TS
	}
	return time.Time{}
}

// loadTotals prefers the freshest source: the live meter.json totals, then the
// folded attempt.md metrics, then the older usage-only meter schema (pre-drvctl-031
// totals). Returns false only when none of them carry any token count.
func loadTotals(root store.Root, a project.Attempt) (agent.Totals, bool) {
	if m, ok := readMeter(root, a.Ticket, a.ID); ok {
		if t := m.Totals; t.NormalizedWork() > 0 {
			return t, true
		}
		// Older meters predate the totals block but still carry a usage snapshot.
		if u := m.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheCreationTokens > 0 {
			return agent.Totals{
				InputTokens:         u.InputTokens,
				OutputTokens:        u.OutputTokens,
				CacheReadTokens:     u.CacheReadTokens,
				CacheCreationTokens: u.CacheCreationTokens,
			}, true
		}
	}
	if m := a.Metrics; m != nil {
		return agent.Totals{
			InputTokens:         m.InputTokens,
			OutputTokens:        m.OutputTokens,
			CacheReadTokens:     m.CacheReadTokens,
			CacheCreationTokens: m.CacheCreationTokens,
		}, true
	}
	return agent.Totals{}, false
}

func readMeter(root store.Root, ticket, attempt string) (session.Meter, bool) {
	data, err := os.ReadFile(root.SessionMeterPath(ticket, attempt))
	if err != nil {
		return session.Meter{}, false
	}
	var m session.Meter
	if err := json.Unmarshal(data, &m); err != nil {
		return session.Meter{}, false
	}
	return m, true
}

// wireUsage mirrors the Claude-Code stream-json usage frame (same field names as
// internal/agent/claudecode/parse.go, kept local so the canary need not export
// them from the adapter).
type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type streamLine struct {
	Type    string `json:"type"`
	Message *struct {
		Usage *wireUsage `json:"usage"`
	} `json:"message"`
	Usage *wireUsage `json:"usage"`
}

// loadFirstTurn returns the first per-request usage frame from stream.jsonl — the
// turn-1 evidence the canary judges. A per-request frame is one that carries
// prompt tokens (input + cache_read + cache_creation > 0), which skips any leading
// system/init lines. Returns false when the stream is absent, empty, or frameless.
func loadFirstTurn(root store.Root, ticket, attempt string) (agent.Usage, bool) {
	f, err := os.Open(root.SessionStreamPath(ticket, attempt))
	if err != nil {
		return agent.Usage{}, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tolerate long stream-json lines
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var line streamLine
		if err := json.Unmarshal(b, &line); err != nil {
			continue
		}
		u := line.Usage
		if line.Message != nil && line.Message.Usage != nil {
			u = line.Message.Usage
		}
		if u == nil {
			continue
		}
		if u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens == 0 {
			continue
		}
		return agent.Usage{
			InputTokens:         u.InputTokens,
			OutputTokens:        u.OutputTokens,
			CacheReadTokens:     u.CacheReadInputTokens,
			CacheCreationTokens: u.CacheCreationInputTokens,
			ContextTokens:       u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		}, true
	}
	return agent.Usage{}, false
}

// adapterVersion reads the pinned adapter version recorded for the attempt. The
// drvctl-033 version event is not yet implemented, so today session.Identity
// carries no version and this returns "" — which the analysis reports as
// "unattributable" rather than treating as a match. This is the seam that lights
// up once drvctl-033 records the version.
func adapterVersion(root store.Root, ticket, attempt string) string {
	return ""
}
