package project

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
)

func TestParseSupervision(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Supervision
		wantErr bool
	}{
		{"passthrough", SupervisionPassthrough, false},
		{"pre-digest", SupervisionPreDigest, false},
		{" pre-digest ", SupervisionPreDigest, false}, // trimmed
		{"auto-execute", "", true},                    // deferred, rejected with a pointed message
		{"", "", true},
		{"nonsense", "", true},
	} {
		got, err := ParseSupervision(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseSupervision(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSupervision(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseSupervision(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseSupervisionAutoExecuteNamed checks the deferred mode's error names it, so
// an operator reaching for auto-execute is told it is not built yet rather than
// getting an opaque "unknown".
func TestParseSupervisionAutoExecuteNamed(t *testing.T) {
	_, err := ParseSupervision("auto-execute")
	if err == nil {
		t.Fatal("auto-execute parsed; want a deferred-mode error")
	}
	if got := err.Error(); !strings.Contains(got, "deferred") || !strings.Contains(got, "auto-execute") {
		t.Errorf("auto-execute error %q should name the mode and say it is deferred", got)
	}
}

func sup(mode string) event.Event { return event.Event{Type: "supervision", Outcome: mode} }

func TestDeriveSupervision(t *testing.T) {
	for name, tc := range map[string]struct {
		events []event.Event
		want   Supervision
	}{
		"none is default/unset": {nil, SupervisionDefault},
		"single pre-digest":     {[]event.Event{sup("pre-digest")}, SupervisionPreDigest},
		"single passthrough":    {[]event.Event{sup("passthrough")}, SupervisionPassthrough},
		"last event wins": {[]event.Event{
			sup("pre-digest"), sup("passthrough"),
		}, SupervisionPassthrough},
		"flip back to pre-digest": {[]event.Event{
			sup("passthrough"), sup("pre-digest"),
		}, SupervisionPreDigest},
		"non-supervision events ignored": {[]event.Event{
			{Type: "enable"}, sup("pre-digest"), {Type: "note"},
		}, SupervisionPreDigest},
		"unparseable outcome skipped defensively": {[]event.Event{
			sup("pre-digest"), sup("bogus"),
		}, SupervisionPreDigest},
	} {
		t.Run(name, func(t *testing.T) {
			if got := DeriveSupervision(tc.events); got != tc.want {
				t.Errorf("DeriveSupervision = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEffective(t *testing.T) {
	for name, tc := range map[string]struct {
		override, def Supervision
		want          Supervision
	}{
		"override wins over default":     {SupervisionPreDigest, SupervisionPassthrough, SupervisionPreDigest},
		"override wins the other way":    {SupervisionPassthrough, SupervisionPreDigest, SupervisionPassthrough},
		"unset falls back to default":    {SupervisionDefault, SupervisionPreDigest, SupervisionPreDigest},
		"unset+unset floors passthrough": {SupervisionDefault, SupervisionDefault, SupervisionPassthrough},
		"unknown default floors too":     {SupervisionDefault, Supervision("weird"), SupervisionPassthrough},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Effective(tc.override, tc.def); got != tc.want {
				t.Errorf("Effective(%q,%q) = %q, want %q", tc.override, tc.def, got, tc.want)
			}
		})
	}
}
