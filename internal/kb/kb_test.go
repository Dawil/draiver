package kb

import (
	"reflect"
	"testing"

	"github.com/Dawil/draiver/internal/event"
)

// ev builds a seq-stamped event for the fold (the fold reads only seq/type/refs/
// supersedes/body, so the rest is left zero).
func ev(seq int, typ, body string, refs, supersedes []int) event.Event {
	return event.Event{Seq: seq, Type: typ, Body: body, Refs: refs, Supersedes: supersedes}
}

func liveSeqs(v View) []int {
	var out []int
	for _, f := range v.Live() {
		out = append(out, f.Seq)
	}
	return out
}

func TestFoldLivenessViaSupersedes(t *testing.T) {
	v := Fold([]event.Event{
		ev(1, "created", "start", nil, nil),
		ev(2, "gotcha", "keys only work in test mode", nil, nil),
		ev(3, "gotcha", "actually live keys 401 until verified", nil, []int{2}), // retires #2
	})
	if got, want := liveSeqs(v), []int{1, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("live seqs = %v, want %v", got, want)
	}
	f2, _ := v.Get(2)
	if f2.Live || f2.SupersededBy != 3 {
		t.Errorf("#2 should be superseded by #3, got live=%v by=%d", f2.Live, f2.SupersededBy)
	}
	f3, _ := v.Get(3)
	if !f3.Live || f3.SupersededBy != 0 {
		t.Errorf("#3 should be live, got live=%v by=%d", f3.Live, f3.SupersededBy)
	}
}

// The resolution→escalation special case is subsumed: a resolution retires the
// escalation it refs, with no explicit Supersedes field and no gateway change.
func TestFoldResolutionBridgeRetiresEscalation(t *testing.T) {
	v := Fold([]event.Event{
		ev(1, "escalation", "which provider?", nil, nil),
		ev(2, "resolution", "use Auth0", []int{1}, nil), // bridges: retires #1
		ev(3, "escalation", "prod secret?", nil, nil),   // open
	})
	if got, want := liveSeqs(v), []int{2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("live seqs = %v, want %v", got, want)
	}
	openEsc := v.LiveEscalations()
	if len(openEsc) != 1 || openEsc[0].Seq != 3 {
		t.Errorf("open escalations = %+v, want only #3", openEsc)
	}
	f1, _ := v.Get(1)
	if f1.Live || f1.SupersededBy != 2 {
		t.Errorf("#1 should be retired by #2, got live=%v by=%d", f1.Live, f1.SupersededBy)
	}
}

// Backward compat: an attempt with no supersedes anywhere → every event live.
func TestFoldNoSupersedesEverythingLive(t *testing.T) {
	v := Fold([]event.Event{
		ev(1, "created", "start", nil, nil),
		ev(2, "decision", "chose cobra", nil, nil),
		ev(3, "note", "context", nil, nil),
	})
	if got, want := liveSeqs(v), []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("live seqs = %v, want all three", got)
	}
}

func TestChainOldestToNewest(t *testing.T) {
	v := Fold([]event.Event{
		ev(1, "decision", "seemed like X", nil, nil),
		ev(2, "decision", "actually Y", nil, []int{1}),    // supersedes #1
		ev(3, "decision", "no, finally Z", nil, []int{2}), // supersedes #2
		ev(4, "note", "unrelated", nil, nil),
	})
	chain := v.Chain(3)
	var seqs []int
	for _, f := range chain {
		seqs = append(seqs, f.Seq)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(seqs, want) {
		t.Errorf("chain(3) = %v, want %v (oldest→newest)", seqs, want)
	}
	// A fact that supersedes nothing yields just itself.
	if c := v.Chain(4); len(c) != 1 || c[0].Seq != 4 {
		t.Errorf("chain(4) = %+v, want just #4", c)
	}
	// Unknown seq yields nothing.
	if c := v.Chain(99); c != nil {
		t.Errorf("chain(99) = %+v, want nil", c)
	}
}

func TestHeadline(t *testing.T) {
	cases := []struct{ in, want string }{
		{"# Heading\n\nbody", "Heading"},
		{"\n\n  plain first line  \nsecond", "plain first line"},
		{"- a bullet point", "a bullet point"},
		{"> a quote", "a quote"},
		{"", "(no body)"},
		{"   \n\t\n", "(no body)"},
	}
	for _, c := range cases {
		if got := Headline(c.in); got != c.want {
			t.Errorf("Headline(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Length cap with ellipsis.
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	h := Headline(long)
	if []rune(h)[len([]rune(h))-1] != '…' {
		t.Errorf("long headline not truncated with ellipsis: %q", h)
	}
	if len([]rune(h)) > maxHeadline+1 {
		t.Errorf("headline length %d exceeds cap %d (+ellipsis)", len([]rune(h)), maxHeadline)
	}
}
