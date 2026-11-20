package metrics

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
)

// Mode selects whether the verb computes. Stored never computes. Compute fills
// missing or stale records. Recompute recomputes every selected attempt.
type Mode string

const (
	ModeStored    Mode = "stored"
	ModeCompute   Mode = "compute"
	ModeRecompute Mode = "recompute"
)

// Normalizer decodes one recorded transcript line into normalized events. It is the
// adapter seam: the fold only ever sees agent.Events.
type Normalizer func(line []byte) []agent.Event

// Options configures Build.
type Options struct {
	Mode           Mode
	IncludeRunning bool
	Normalize      Normalizer
	Now            func() time.Time
}

// Usage is the attempt's token figures. They are reused from attempt.md's `metrics:`
// block (folded on retire) and the session meter, never recomputed. There is no cost:
// the meter's dollar figure was unreliable (null, or off by orders of magnitude) and
// no verified, pinned per-model price table exists to derive one (drv-054 #19).
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	NormalizedWork      int `json:"normalized_work"`
}

// Row is one attempt in the verb's output.
type Row struct {
	Ticket  string `json:"ticket"`
	Attempt string `json:"attempt"`
	// Source says where Metrics came from: stored | missing | stale | computed.
	Source Source `json:"source"`
	// Partial marks a running attempt's numbers. They are never persisted.
	Partial       bool      `json:"partial"`
	Started       time.Time `json:"started"`
	TranscriptCmd string    `json:"transcript_cmd"`

	MetricsVersion int        `json:"metrics_version,omitempty"`
	ComputedAt     *time.Time `json:"computed_at,omitempty"`
	Provenance     Provenance `json:"provenance"`
	Usage          *Usage     `json:"usage"`
	// Metrics is nil when Source is missing.
	Metrics *Values `json:"metrics"`

	running bool
}

// Running reports whether the attempt is still being worked.
func (r Row) Running() bool { return r.running }

// TranscriptCommand is the command that shows an attempt's transcript, the pointer
// every row carries so a reader can drill from a number to what happened.
func TranscriptCommand(ticket, att string) string {
	return fmt.Sprintf("draiver ctl logs %s --attempt %s", ticket, att)
}

// Build produces one attempt's row under opts. It never computes in ModeStored,
// and it never persists a running attempt's partial numbers.
func Build(root store.Root, ticket, att string, opts Options) (Row, error) {
	a, err := project.LoadAttempt(root, ticket, att)
	if err != nil {
		return Row{}, err
	}
	meta, err := attempt.LoadMeta(root, ticket, att)
	if err != nil {
		return Row{}, err
	}
	lf := FromLog(a)
	row := Row{
		Ticket: ticket, Attempt: att, Started: meta.Started,
		TranscriptCmd: TranscriptCommand(ticket, att),
		Partial:       lf.Running,
		running:       lf.Running,
		Provenance:    recordedProvenance(meta),
		Usage:         usageOf(root, ticket, att, meta),
	}

	if lf.Running && !opts.IncludeRunning {
		// The caller excludes it. Do no work on numbers nobody will see.
		row.Source = SourceMissing
		return row, nil
	}

	rec, ok, err := Load(root, ticket, att)
	if err != nil {
		return Row{}, err
	}
	switch {
	case !ok:
		row.Source = SourceMissing
	case rec.MetricsVersion != Version:
		row.Source = SourceStale
	default:
		row.Source = SourceStored
	}

	compute := opts.Mode == ModeRecompute ||
		(opts.Mode == ModeCompute && (row.Source == SourceMissing || row.Source == SourceStale || lf.Running))
	if compute {
		norm := opts.Normalize
		if norm == nil {
			return Row{}, errors.New("metrics: no transcript normalizer configured")
		}
		now := time.Now
		if opts.Now != nil {
			now = opts.Now
		}
		rec, err = computeRecord(root, a, meta, lf, norm, now().UTC())
		if err != nil {
			return Row{}, err
		}
		ok = true
		row.Source = SourceComputed
		if !lf.Running {
			if err := Save(root, rec); err != nil {
				return Row{}, err
			}
		}
	}
	if ok {
		at := rec.ComputedAt
		row.MetricsVersion = rec.MetricsVersion
		row.ComputedAt = &at
		row.Provenance = rec.Provenance
		v := rec.Metrics
		row.Metrics = &v
	}
	return row, nil
}

// recordedProvenance reports provenance from attempt.md alone, with no inference:
// what a --stored read may show for an attempt with no metrics record.
func recordedProvenance(m attempt.Meta) Provenance {
	p := Provenance{
		BaseCommit:     valueOr(m.BaseCommit, "recorded"),
		RepoID:         valueOr(m.RepoID, "recorded"),
		DraiverVersion: valueOr(m.DraiverVersion, "recorded"),
		AdapterVersion: m.AdapterVersion,
		Repo:           m.Repo,
		Base:           m.Base,
	}
	switch {
	case m.ModelUsed != "":
		p.Model = Value{m.ModelUsed, "recorded"}
	case m.Model != "":
		p.Model = Value{m.Model, "requested"}
	default:
		p.Model = Value{"", "unknown"}
	}
	return p
}

func valueOr(v, source string) Value {
	if v == "" {
		return Value{"", "unknown"}
	}
	return Value{v, source}
}

// computeRecord folds the transcript and joins it with the log facts, the final diff
// and provenance (inferring missing provenance where it can, clearly labelled).
func computeRecord(root store.Root, a project.Attempt, meta attempt.Meta, lf LogFacts, norm Normalizer, now time.Time) (Record, error) {
	prov := recordedProvenance(meta)
	head := AttemptBranch(a.Ticket, a.ID)
	if prov.BaseCommit.Source == "unknown" && meta.Repo != "" {
		if sha, h, err := InferBaseCommit(meta.Repo, a.Ticket, a.ID, meta.Base); err == nil && sha != "" {
			prov.BaseCommit = Value{sha, "inferred"}
			head = h
		}
	}
	if prov.RepoID.Source == "unknown" && meta.Repo != "" {
		if id, err := RepoID(meta.Repo); err == nil && id != "" {
			prov.RepoID = Value{id, "inferred"}
		}
	}

	st := NewState()
	present, err := foldStream(root.SessionStreamPath(a.Ticket, a.ID), norm, st)
	if err != nil {
		return Record{}, err
	}

	diff := Diff{}
	if prov.BaseCommit.Value != "" && meta.Repo != "" {
		if d, err := FinalDiff(meta.Repo, prov.BaseCommit.Value, head); err == nil {
			diff = d
		}
	}
	tr := st.Finalize(diff)
	if prov.Model.Source != "recorded" && tr.ModelObserved != "" {
		prov.Model = Value{tr.ModelObserved, "observed"}
	}

	v := Values{
		Outcome:          lf.Outcome,
		ReviewRoundTrips: lf.ReviewRoundTrips,
		Escalations:      len(lf.Escalations),
		EscalationTexts:  append([]string{}, lf.Escalations...),
		HumanInterrupts:  lf.Messages + tr.TranscriptInterrupts,
		TranscriptStatus: "absent",
		Transcript:       tr,
	}
	if present {
		v.TranscriptStatus = "present"
	}
	return Record{
		MetricsVersion: Version, ComputedAt: now,
		Ticket: a.Ticket, Attempt: a.ID,
		Provenance: prov, Metrics: v,
	}, nil
}

// foldStream streams a recorded transcript through the fold one line at a time. A
// missing transcript folds nothing and reports present=false.
func foldStream(p string, norm Normalizer, st *State) (present bool, err error) {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("metrics: open transcript: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		for _, ev := range norm(sc.Bytes()) {
			st.Step(ev)
		}
	}
	if err := sc.Err(); err != nil {
		return true, fmt.Errorf("metrics: read transcript: %w", err)
	}
	return true, nil
}

// usageOf reuses the existing token figures (attempt.md `metrics:`) and the
// meter's cost. It returns nil when neither exists.
func usageOf(root store.Root, ticket, att string, meta attempt.Meta) *Usage {
	var u *Usage
	if m := meta.Metrics; m != nil {
		u = &Usage{
			InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
			CacheReadTokens: m.CacheReadTokens, CacheCreationTokens: m.CacheCreationTokens,
			NormalizedWork: m.NormalizedWork,
		}
	}
	data, err := os.ReadFile(root.SessionMeterPath(ticket, att))
	if err != nil {
		return u
	}
	var meter session.Meter
	if json.Unmarshal(data, &meter) != nil {
		return u
	}
	if u == nil {
		t := meter.Totals.Metrics()
		u = &Usage{
			InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
			CacheReadTokens: t.CacheReadTokens, CacheCreationTokens: t.CacheCreationTokens,
			NormalizedWork: t.NormalizedWork,
		}
	}
	return u
}
