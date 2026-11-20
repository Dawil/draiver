package metrics

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/agent"
)

// Sessions, segments, main-context fill, per-file read footprints and normalized
// commands (metrics v2, drv-054 #19). All of it is part of the same incremental fold:
// Step feeds these accumulators, Finalize reads them, nothing reads the clock.

// CompactionMinDrop and CompactionRatio define a compaction: within one session, a
// main-context request whose prompt is at least CompactionMinDrop tokens smaller than
// the previous request's AND less than CompactionRatio of it. A conversation's prompt
// only grows between requests, so a drop that large means the history was compacted.
const (
	CompactionMinDrop = 10000
	CompactionRatio   = 0.5
)

type sessAcc struct {
	id           string
	start, end   time.Time
	segments     int
	calls        int
	startCtx     int // -1 until the first main-context request
	peakCtx      int
	firstEditTo  map[string]*editSnap
	firstEditAny *editSnap
}

type fileAcc struct {
	reads, whole, chars, ctxTokens int
	// ranges[session] is the set of read ranges ("" = whole) taken in that session.
	ranges map[int]map[string]bool
}

type attrPending struct {
	path  string
	chars int
}

// session returns the current session, opening an implicit one when events arrive
// before any system frame.
func (s *State) session() *sessAcc {
	if s.cur < 0 {
		s.openSession("", time.Time{})
	}
	return s.sessions[s.cur]
}

func (s *State) openSession(id string, ts time.Time) {
	if i, ok := s.sessIdx[id]; ok {
		s.cur = i
		return
	}
	s.sessIdx[id] = len(s.sessions)
	s.cur = len(s.sessions)
	s.sessions = append(s.sessions, &sessAcc{id: id, start: ts, startCtx: -1, firstEditTo: map[string]*editSnap{}})
}

// onSystem starts a segment: a (re)start of the stream. A new SessionID is a new
// session (a fresh conversation); a known one is a resume of it.
func (s *State) onSystem(ev agent.Event) {
	s.closeSegment()
	s.segments++
	s.openSession(ev.SessionID, ev.TS)
	s.session().segments++
	s.segStart, s.segLast = ev.TS, ev.TS
	s.ctx = 0 // a new segment's first request is not comparable for compaction
	s.pendingAttr = nil
}

func (s *State) closeSegment() {
	if !s.segStart.IsZero() && s.segLast.After(s.segStart) {
		s.active += s.segLast.Sub(s.segStart).Seconds()
	}
	s.segStart, s.segLast = time.Time{}, time.Time{}
}

// touchTime records an event's timestamp against the current segment and session.
func (s *State) touchTime(ts time.Time) {
	if ts.IsZero() {
		return
	}
	if s.segStart.IsZero() {
		s.segStart = ts
	}
	if ts.After(s.segLast) {
		s.segLast = ts
	}
	ss := s.session()
	if ss.start.IsZero() || ts.Before(ss.start) {
		ss.start = ts
	}
	if ts.After(ss.end) {
		ss.end = ts
	}
}

// onMainContext folds one main-context request's prompt size: session start/peak,
// compactions, and attribution of the growth since the previous request to the read
// results that arrived in between.
func (s *State) onMainContext(ctx int) {
	ss := s.session()
	prev := s.ctx
	if ss.startCtx < 0 {
		ss.startCtx = ctx
	}
	if ctx > ss.peakCtx {
		ss.peakCtx = ctx
	}
	if prev > 0 && prev-ctx >= CompactionMinDrop && float64(ctx) < CompactionRatio*float64(prev) {
		s.compactions++
	}
	if prev > 0 && ctx > prev && len(s.pendingAttr) > 0 {
		delta, total := ctx-prev, 0
		for _, a := range s.pendingAttr {
			total += a.chars
		}
		given := 0
		for i, a := range s.pendingAttr {
			share := 0
			switch {
			case i == len(s.pendingAttr)-1:
				share = delta - given
			case total > 0:
				share = delta * a.chars / total
			default:
				share = delta / len(s.pendingAttr)
			}
			given += share
			s.files[a.path].ctxTokens += share
		}
	}
	s.pendingAttr = nil
	s.ctx = ctx
}

// onRead folds one completed read of rel: the per-file footprint and the reread split.
// main is false for a subagent's read, whose result never enters the main context.
func (s *State) onRead(rel, rng string, chars int, main bool) {
	f := s.files[rel]
	if f == nil {
		f = &fileAcc{ranges: map[int]map[string]bool{}}
		s.files[rel] = f
	}
	prior := f.reads > 0
	f.reads++
	if rng == "" {
		f.whole++
	}
	f.chars += chars
	cur := f.ranges[s.cur]
	switch {
	case !prior:
	case len(cur) == 0:
		s.rereadsCross++ // first read in this session, read in an earlier one
	case cur[rng] || cur[""]:
		s.rereadsSame++ // this slice (or the whole file) already read this session
	default:
		s.rangedReads++ // a new slice of a file already read this session
	}
	if cur == nil {
		cur = map[string]bool{}
		f.ranges[s.cur] = cur
	}
	cur[rng] = true
	if main {
		s.pendingAttr = append(s.pendingAttr, attrPending{path: rel, chars: chars})
	}
}

var (
	ticketIDRe = regexp.MustCompile(`(?i)\b[a-z][a-z]*-\d+\b`)
	digitsRe   = regexp.MustCompile(`\d+`)
)

// NormCommand normalizes a Bash command or search for cross-attempt recurrence: the
// repeated_searches collapse (whitespace, quotes, case) plus ticket ids → <ticket> and
// digit runs → N, so `go test ./x -run Drv044` and `… Drv047` are one command.
func NormCommand(cmd string) string {
	q := normQuery(cmd)
	q = ticketIDRe.ReplaceAllString(q, "<ticket>")
	return digitsRe.ReplaceAllString(q, "N")
}

func (s *State) sessionsOut(diff Diff) ([]Session, *int) {
	var out []Session
	var floor *int
	for _, ss := range s.sessions {
		o := Session{ID: ss.id, Started: ss.start, Ended: ss.end, Segments: ss.segments, Calls: ss.calls, EndedBy: "none"}
		if ss.startCtx >= 0 {
			st, pk := ss.startCtx, ss.peakCtx
			o.StartContext, o.PeakContext = &st, &pk
			if floor == nil {
				floor = &st
			}
		}
		if sn := pickPrefix(ss.firstEditTo, ss.firstEditAny, diff); sn != nil {
			c := sn.ctx
			o.ContextAtFirstProductiveEdit = &c
		}
		out = append(out, o)
	}
	return out, floor
}

// pickPrefix chooses the exploration anchor among first-edit snapshots: the first edit
// of a file in the final diff, else (no diff) the first edit of any file.
func pickPrefix(firstEditTo map[string]*editSnap, firstAny *editSnap, diff Diff) *editSnap {
	if diff.Available {
		var snap *editSnap
		for f, sn := range firstEditTo {
			if diff.Files[f] && (snap == nil || sn.index < snap.index) {
				snap = sn
			}
		}
		return snap
	}
	return firstAny
}

func (s *State) filesOut(diff Diff) []FileStat {
	out := make([]FileStat, 0, len(s.files))
	for p, f := range s.files {
		out = append(out, FileStat{
			Path: p, Reads: f.reads, WholeReads: f.whole, ResultChars: f.chars, ContextTokens: f.ctxTokens,
			Unused: !s.editFiles[p] && !(diff.Available && diff.Files[p]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (s *State) commandsOut() []CommandStat {
	out := make([]CommandStat, 0, len(s.commands))
	for c, n := range s.commands {
		out = append(out, CommandStat{Command: c, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}

// TicketIDsIn returns the ticket ids a file name carries, in the forms they appear in
// draiver's repos: `drv-046`, `drv046`, `drv_046`, `Drv046` (normalized to `drv-046`).
func TicketIDsIn(name string) []string {
	var out []string
	for _, m := range fileTicketRe.FindAllStringSubmatch(strings.ToLower(name), -1) {
		out = append(out, m[1]+"-"+strings.TrimLeft(m[2], "0"))
	}
	return out
}

var fileTicketRe = regexp.MustCompile(`(?:^|[^a-z])([a-z]{2,10})[-_]?(\d{2,5})(?:[^0-9]|$)`)

// normTicket canonicalizes a ticket id for comparison with TicketIDsIn.
func normTicket(t string) string {
	m := fileTicketRe.FindStringSubmatch(strings.ToLower(t))
	if m == nil {
		return strings.ToLower(t)
	}
	return m[1] + "-" + strings.TrimLeft(m[2], "0")
}

// IsCrossTicket reports whether a file's base name carries a ticket id other than the
// reader's own. Only ids in the reader's prefix family count (drv ~ drvweb ~ drvctl),
// so names like sha256.go or base64.go never match.
func IsCrossTicket(file, ticket string) bool {
	own := normTicket(ticket)
	ownPrefix, _, _ := strings.Cut(own, "-")
	base := file
	if i := strings.LastIndex(file, "/"); i >= 0 {
		base = file[i+1:]
	}
	for _, id := range TicketIDsIn(base) {
		p, _, _ := strings.Cut(id, "-")
		if id != own && (strings.HasPrefix(p, ownPrefix) || strings.HasPrefix(ownPrefix, p)) {
			return true
		}
	}
	return false
}

// throwawayPrefixes mark a file as ad-hoc scaffolding rather than a lasting test.
var throwawayPrefixes = []string{"zz_", "zz-", "tmp_", "tmp-", "adhoc"}

// IsTestFile reports whether a path is a test file by the common conventions:
// Go `_test.go`, `test_*.py` / `*_test.py`, JS/TS `.test.` / `.spec.`, and Gherkin
// `.feature` files.
func IsTestFile(p string) bool {
	base := strings.ToLower(path.Base(p))
	return strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".feature") ||
		(strings.HasSuffix(base, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py"))) ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

// TicketScopedTestFiles returns the test files among files whose base name carries
// the ticket's own id and starts with a throwaway prefix (zz_, tmp_, adhoc): focus
// harnesses built for one ticket, e.g. features/acceptance/zz_drv044_focus_test.go.
func TicketScopedTestFiles(files []string, ticket string) []string {
	own := normTicket(ticket)
	out := []string{}
	for _, f := range files {
		base := strings.ToLower(path.Base(f))
		if !IsTestFile(f) || !slices.Contains(TicketIDsIn(base), own) {
			continue
		}
		for _, pre := range throwawayPrefixes {
			if strings.HasPrefix(base, pre) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// NamePattern collapses a file path to a cross-attempt pattern: in the base name,
// every ticket id (any spelling TicketIDsIn knows) and the text between a throwaway
// prefix and a test suffix become `*`, so features/acceptance/zz_drv044_focus_test.go
// and …/zz_drv047_subset_test.go are both features/acceptance/zz_*_test.go.
func NamePattern(p string) string {
	dir, base := path.Split(p)
	lower := strings.ToLower(base)
	for _, pre := range throwawayPrefixes {
		if !strings.HasPrefix(lower, pre) {
			continue
		}
		for _, suf := range []string{"_test.go", ".feature", "_test.py", ".test.ts", ".test.js", ".spec.ts", ".spec.js", ".go", ".py", ".sh"} {
			if strings.HasSuffix(lower, suf) && len(lower) > len(pre)+len(suf) {
				return dir + base[:len(pre)] + "*" + base[len(base)-len(suf):]
			}
		}
	}
	return dir + fileTicketRe.ReplaceAllStringFunc(base, func(m string) string {
		// keep the delimiters fileTicketRe consumed around the id
		sub := fileTicketRe.FindStringSubmatchIndex(strings.ToLower(m))
		return m[:sub[2]] + "*" + m[sub[5]:]
	})
}
