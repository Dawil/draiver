package metrics

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/agent"
)

// The transcript metrics are an incremental fold over normalized agent.Events: a
// State, a Step applied to each event in stream order, and a Finalize that joins the
// folded state with the attempt's final diff. Nothing here reads the agent's wire
// format or holds the transcript in memory, so a later live consumer (`--follow`,
// an exporter) only has to call Step as events arrive.

// State is the running fold. The zero value is not ready; use NewState.
type State struct {
	cwd string

	firstTS, lastTS time.Time

	toolCalls int
	byClass   map[Class]int
	// running counts used to snapshot the exploration prefix at each edit
	readish  int // read + subagent calls so far
	searches int // search calls so far

	pending map[string]pendingCall // tool-call id → call awaiting its result

	seenQueries      map[string]bool
	emptySearches    int
	repeatedSearches int

	readFiles  map[string]int // repo-relative path → read count
	readOrder  []string
	editFiles  map[string]bool
	docsRead   map[string]bool
	firstDocAt int // tool-call index of the first doc read, -1 if none

	// firstEditAny is the snapshot at the first edit of any file; firstEditTo
	// holds the snapshot at the first edit of each file.
	firstEditAny *editSnap
	firstEditTo  map[string]*editSnap
	awaitingCtx  []*editSnap // snapshots waiting for their request's context size

	errs map[ErrKind]int

	testTries, buildTries int
	firstGreenTest        *greenSnap
	firstGreenBuild       *greenSnap

	seenMsg map[string]bool
	work    int // cumulative tokens (input + cache read/write + output), deduped by message
	ctx     int // latest per-request context size
	models  map[string]int

	interrupts int
}

type pendingCall struct {
	index int
	class Class
	call  Call
	bash  bool
	snap  *editSnap // set when the call edits files
}

// editSnap is the exploration state captured when a file is first edited.
type editSnap struct {
	index    int
	readish  int
	searches int
	ctx      int
	msgID    string
}

type greenSnap struct {
	tries int
	work  int
	at    time.Time
}

// NewState returns an empty fold.
func NewState() *State {
	return &State{
		byClass:     map[Class]int{},
		pending:     map[string]pendingCall{},
		seenQueries: map[string]bool{},
		readFiles:   map[string]int{},
		editFiles:   map[string]bool{},
		docsRead:    map[string]bool{},
		firstDocAt:  -1,
		firstEditTo: map[string]*editSnap{},
		errs:        map[ErrKind]int{},
		seenMsg:     map[string]bool{},
		models:      map[string]int{},
	}
}

// Step folds one event.
func (s *State) Step(ev agent.Event) {
	if !ev.TS.IsZero() {
		if s.firstTS.IsZero() || ev.TS.Before(s.firstTS) {
			s.firstTS = ev.TS
		}
		if ev.TS.After(s.lastTS) {
			s.lastTS = ev.TS
		}
	}
	switch ev.Kind {
	case agent.EventSystem:
		if ev.Cwd != "" && s.cwd == "" {
			s.cwd = filepath.Clean(ev.Cwd)
		}
		if ev.Model != "" && len(s.models) == 0 {
			s.models[ev.Model] += 0 // known, but count only real assistant messages
		}
	case agent.EventAssistant:
		s.countModel(ev)
	case agent.EventToolCall:
		s.countModel(ev)
		if ev.Tool != nil {
			s.onCall(ev)
		}
	case agent.EventToolResult:
		if ev.Tool != nil {
			s.onResult(ev)
		}
	case agent.EventUsage:
		s.onUsage(ev)
	case agent.EventTurnEnd:
		if strings.Contains(strings.ToLower(ev.Turn), "interrupt") {
			s.interrupts++
		}
	}
}

func (s *State) countModel(ev agent.Event) {
	if ev.Model == "" {
		return
	}
	key := "model:" + ev.MessageID
	if ev.MessageID != "" && s.seenMsg[key] {
		return
	}
	s.seenMsg[key] = true
	s.models[ev.Model]++
}

func (s *State) onUsage(ev agent.Event) {
	u := ev.Usage
	if u == nil || u.ContextTokens == 0 {
		return // the cumulative turn-end frame is not a per-request figure
	}
	key := "usage:" + ev.MessageID
	if ev.MessageID != "" && s.seenMsg[key] {
		return // the same message's usage repeated on a sibling content-block line
	}
	s.seenMsg[key] = true
	s.work += u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens + u.OutputTokens
	s.ctx = u.ContextTokens
	// Snapshots taken at an edit call learn the size of the request that produced
	// that call from its message's usage.
	keep := s.awaitingCtx[:0]
	for _, sn := range s.awaitingCtx {
		if sn.msgID == "" || sn.msgID == ev.MessageID {
			sn.ctx = u.ContextTokens
		} else {
			keep = append(keep, sn)
		}
	}
	s.awaitingCtx = keep
}

func (s *State) onCall(ev agent.Event) {
	c := Classify(ev.Tool.Name, ev.Tool.Input)
	idx := s.toolCalls
	s.toolCalls++
	s.byClass[c.Class]++
	pc := pendingCall{index: idx, class: c.Class, call: c, bash: ev.Tool.Name == "Bash"}
	// An edit call snapshots the exploration state now, so its prefix covers the
	// calls strictly before it. The snapshot is committed only once the result shows
	// the edit happened.
	if len(c.Edits) > 0 {
		pc.snap = &editSnap{index: idx, readish: s.readish, searches: s.searches, ctx: s.ctx, msgID: ev.MessageID}
		s.awaitingCtx = append(s.awaitingCtx, pc.snap)
	}
	s.pending[ev.Tool.ID] = pc

	switch c.Class {
	case ClassRead, ClassSubagent:
		s.readish++
	case ClassSearch:
		s.searches++
		if c.Query != "" {
			if s.seenQueries[c.Query] {
				s.repeatedSearches++
			}
			s.seenQueries[c.Query] = true
		}
	}
}

func (s *State) onResult(ev agent.Event) {
	pc, ok := s.pending[ev.Tool.ID]
	if !ok {
		return
	}
	delete(s.pending, ev.Tool.ID)
	if strings.Contains(ev.Tool.Result, "[Request interrupted by user") {
		s.interrupts++
	}
	empty := pc.class == ClassSearch && IsEmptySearch(ev.Tool.Result)
	if empty {
		s.emptySearches++
	}
	// A search that found nothing (grep exits 1) is not an error.
	failed := ev.Tool.IsError && !empty
	kind := ErrNone
	if failed {
		kind = ClassifyError(pc.class, ev.Tool.Result)
		s.errs[kind]++
	}
	// Reads and edits count only when the call took effect. A failed file tool (an
	// Edit whose old_string did not match, a Read of a missing file) touched nothing.
	// A failed Bash call may still have run its earlier segments, so it counts unless
	// it never ran (permission denial) or could not find its command or file.
	touched := !failed || (pc.bash && kind != ErrPermission && kind != ErrEnvDiscovery)
	if touched {
		s.commitTouches(pc)
	}
	switch pc.class {
	case ClassTest:
		s.testTries++
		if !failed && s.firstGreenTest == nil {
			s.firstGreenTest = &greenSnap{tries: s.testTries, work: s.work, at: ev.TS}
		}
	case ClassBuild:
		s.buildTries++
		if !failed && s.firstGreenBuild == nil {
			s.firstGreenBuild = &greenSnap{tries: s.buildTries, work: s.work, at: ev.TS}
		}
	}
}

// commitTouches records the files a completed call read and edited.
func (s *State) commitTouches(pc pendingCall) {
	for _, p := range pc.call.Reads {
		rel, ok := s.rel(p)
		if !ok {
			continue
		}
		if s.readFiles[rel] == 0 {
			s.readOrder = append(s.readOrder, rel)
		}
		s.readFiles[rel]++
		if IsDoc(rel) {
			s.docsRead[rel] = true
			if s.firstDocAt < 0 || pc.index < s.firstDocAt {
				s.firstDocAt = pc.index
			}
		}
	}
	for _, p := range pc.call.Edits {
		rel, ok := s.rel(p)
		if !ok {
			continue
		}
		s.editFiles[rel] = true
		if s.firstEditAny == nil || pc.snap.index < s.firstEditAny.index {
			s.firstEditAny = pc.snap
		}
		if s.firstEditTo[rel] == nil || pc.snap.index < s.firstEditTo[rel].index {
			s.firstEditTo[rel] = pc.snap
		}
	}
}

// rel turns a path as the agent wrote it into a repo-relative one. Paths under the
// session's working directory are made relative. Relative paths are taken as
// already repo-relative. Absolute paths elsewhere (/tmp, ~/.claude, the data root)
// are not codebase files and are dropped (ok=false).
func (s *State) rel(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, "*?[{$`") {
		return "", false // a glob or an expansion names no single file
	}
	if strings.HasPrefix(p, "~") {
		return "", false
	}
	if filepath.IsAbs(p) {
		if s.cwd == "" {
			return "", false
		}
		r, err := filepath.Rel(s.cwd, p)
		if err != nil || r == ".." || strings.HasPrefix(r, "../") {
			return "", false
		}
		return filepath.ToSlash(r), true
	}
	return path.Clean(filepath.ToSlash(p)), true
}

// IsDoc reports whether a repo-relative path is agent-facing documentation:
// AGENTS.md, CLAUDE.md, a README* anywhere, or anything under a docs/ directory.
func IsDoc(rel string) bool {
	base := path.Base(rel)
	switch {
	case strings.EqualFold(base, "AGENTS.md"), strings.EqualFold(base, "CLAUDE.md"):
		return true
	case strings.HasPrefix(strings.ToLower(base), "readme"):
		return true
	case strings.HasPrefix(rel, "docs/") || strings.Contains(rel, "/docs/"):
		return true
	}
	return false
}

// Diff is the attempt's final diff: the files changed on the attempt branch
// relative to base_commit, and the changed line count. Available is false when it
// could not be determined (no base, branch gone, repo missing).
type Diff struct {
	Available bool
	Files     map[string]bool
	Lines     int
}

// Finalize joins the folded state with the final diff into the transcript metric
// values. It does not mutate the state, so a live consumer can finalize repeatedly.
func (s *State) Finalize(diff Diff) Transcript {
	t := Transcript{
		ToolCalls:            s.toolCalls,
		CallsByClass:         map[string]int{},
		EmptySearches:        s.emptySearches,
		RepeatedSearches:     s.repeatedSearches,
		FilesReadDistinct:    len(s.readFiles),
		Rereads:              0,
		DocsRead:             sortedKeys(s.docsRead),
		EnvDiscoveryErrors:   s.errs[ErrEnvDiscovery],
		TestFailures:         s.errs[ErrTestFailure],
		BuildFailures:        s.errs[ErrBuildFailure],
		EditMechanicsErrors:  s.errs[ErrEditMechanics],
		PermissionDenials:    s.errs[ErrPermission],
		OtherErrors:          s.errs[ErrOther],
		TranscriptInterrupts: s.interrupts,
		ModelObserved:        s.modelObserved(),
		FinalDiff:            "unavailable",
	}
	for c, n := range s.byClass {
		t.CallsByClass[string(c)] = n
	}
	for _, n := range s.readFiles {
		if n > 1 {
			t.Rereads += n - 1
		}
	}
	if s.firstDocAt >= 0 {
		i := s.firstDocAt
		t.FirstDocReadIndex = &i
	}
	if !s.firstTS.IsZero() && !s.lastTS.IsZero() {
		w := s.lastTS.Sub(s.firstTS).Seconds()
		t.WallTimeSeconds = &w
	}
	if diff.Available {
		t.FinalDiff = "available"
		n, l := len(diff.Files), diff.Lines
		t.DiffFiles, t.DiffLines = &n, &l
	}

	// The exploration prefix ends at the first productive edit: the first edit of a
	// file that is in the final diff. Without a usable diff it falls back to the first
	// edit of any file, flagged. With no edit at all the prefix is the whole transcript.
	var snap *editSnap
	if diff.Available {
		for f, sn := range s.firstEditTo {
			if diff.Files[f] && (snap == nil || sn.index < snap.index) {
				snap = sn
			}
		}
		if snap != nil {
			t.PrefixBasis = "first-productive-edit"
		}
	}
	if snap == nil && s.firstEditAny != nil {
		snap = s.firstEditAny
		t.PrefixBasis = "first-edit"
	}
	if snap != nil {
		t.ExplorationPrefixCalls = snap.readish
		t.SearchPrefixCalls = snap.searches
		t.ExplorationPrefixTokens = snap.ctx
	} else {
		t.PrefixBasis = "no-edit"
		t.ExplorationPrefixCalls = s.byClass[ClassRead] + s.byClass[ClassSubagent]
		t.SearchPrefixCalls = s.byClass[ClassSearch]
		t.ExplorationPrefixTokens = s.ctx
	}

	// Unused reads: read but neither edited nor in the final diff.
	unused := 0
	for f := range s.readFiles {
		if s.editFiles[f] || (diff.Available && diff.Files[f]) {
			continue
		}
		unused++
	}
	t.FilesReadUnused = unused
	if len(s.readFiles) > 0 {
		r := float64(unused) / float64(len(s.readFiles))
		t.WastedContextRatio = &r
	}

	if diff.Available {
		reverted := 0
		for f := range s.editFiles {
			if !diff.Files[f] {
				reverted++
			}
		}
		t.RevertedEdits = &reverted
	}

	if g := s.firstGreenTest; g != nil {
		tries, work := g.tries, g.work
		t.TriesToFirstSuccessfulTest = &tries
		t.TokensToFirstGreenTest = &work
		if !g.at.IsZero() && !s.firstTS.IsZero() {
			d := g.at.Sub(s.firstTS).Seconds()
			t.TimeToFirstGreenTestSeconds = &d
		}
	}
	if g := s.firstGreenBuild; g != nil {
		tries := g.tries
		t.TriesToFirstSuccessfulBuild = &tries
	}
	return t
}

// modelObserved is the model that produced most assistant messages.
func (s *State) modelObserved() string {
	best, n := "", -1
	for m, c := range s.models {
		if c > n || (c == n && m < best) {
			best, n = m, c
		}
	}
	return best
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
