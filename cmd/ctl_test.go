package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/handbook"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestResolveCtlTarget(t *testing.T) {
	dir := newTicket(t) // ticket PROJ-1, attempt 0001
	dataFlag, actorFlag, attemptFlag = "", "", ""
	t.Setenv("DRAIVER_ATTEMPT", "")
	root := store.Root{Dir: dir}

	t.Run("latest attempt", func(t *testing.T) {
		ticket, att, err := resolveCtlTarget(root, "PROJ-1")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if ticket != "PROJ-1" || att != "0001" {
			t.Fatalf("got %s/%s, want PROJ-1/0001", ticket, att)
		}
	})

	t.Run("explicit @attempt suffix", func(t *testing.T) {
		ticket, att, err := resolveCtlTarget(root, "PROJ-1@0001")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if ticket != "PROJ-1" || att != "0001" {
			t.Fatalf("got %s/%s, want PROJ-1/0001", ticket, att)
		}
	})

	t.Run("unknown ticket", func(t *testing.T) {
		if _, _, err := resolveCtlTarget(root, "NOPE-9"); err == nil {
			t.Fatal("expected an error for an unknown ticket")
		}
	})

	t.Run("unknown attempt", func(t *testing.T) {
		if _, _, err := resolveCtlTarget(root, "PROJ-1@9999"); err == nil {
			t.Fatal("expected an error for an unknown attempt")
		}
	})
}

// TestCtlEnableDisable drives the enable/disable verbs end to end: each appends a
// log event that flips the derived Enabled bit, the default is disabled, and the
// bit is a separate axis that leaves the control state on Running.
func TestCtlEnableDisable(t *testing.T) {
	dir := newTicket(t) // ticket PROJ-1, attempt 0001

	// Default: disabled.
	if a, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001"); err != nil || a.Enabled {
		t.Fatalf("fresh attempt should be disabled by default (enabled=%v err=%v)", a.Enabled, err)
	}

	// enable → Enabled, still Running.
	out, code := run(t, "--data", dir, "--actor", "human:dave", "ctl", "enable", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("ctl enable exited %d: %s", code, out)
	}
	a, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Enabled {
		t.Fatal("attempt should be enabled after ctl enable")
	}
	if a.State != project.Running {
		t.Fatalf("enable must not change control state: got %q", a.State)
	}

	// disable → back to disabled.
	if out, code := run(t, "--data", dir, "--actor", "human:dave", "ctl", "disable", "PROJ-1@0001"); code != 0 {
		t.Fatalf("ctl disable exited %d: %s", code, out)
	}
	if a, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001"); err != nil || a.Enabled {
		t.Fatalf("attempt should be disabled after ctl disable (enabled=%v err=%v)", a.Enabled, err)
	}

	// The two events are in the hash chain like every other.
	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Type)
	}
	if strings.Join(kinds, ",") != "created,enable,disable" {
		t.Fatalf("unexpected log types: %v", kinds)
	}

	// An unknown target is an error, not a silent no-op.
	if _, code := run(t, "--data", dir, "ctl", "enable", "NOPE-9"); code == 0 {
		t.Fatal("expected nonzero exit enabling an unknown ticket")
	}
}

// TestCtlStatusHidesDone drives the status filter end to end: the default list
// view omits terminal Done attempts, --all restores them, and an explicit target
// prints its Done attempt regardless — plus the empty-view hint counts what it
// hid.
func TestCtlStatusHidesDone(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001, Running

	// A second ticket driven to Done via a `done` lifecycle event.
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "PROJ-2", "--title", "Two", "--repo", dir); code != 0 {
		t.Fatalf("new PROJ-2 exited %d", code)
	}
	root := store.Root{Dir: dir}
	if _, err := ticketlog.Append(root, "PROJ-2", "0001", event.Event{Type: "done", Actor: "human:test", Body: "closed"}); err != nil {
		t.Fatalf("append done: %v", err)
	}
	if a, err := project.LoadAttempt(root, "PROJ-2", "0001"); err != nil || a.State != project.Done {
		t.Fatalf("PROJ-2 should be Done (state=%q err=%v)", a.State, err)
	}

	// Default list view: Running shows, Done is hidden.
	out, code := run(t, "--data", dir, "ctl", "status")
	if code != 0 {
		t.Fatalf("ctl status exited %d: %s", code, out)
	}
	if !strings.Contains(out, "PROJ-1/0001") {
		t.Errorf("default view should list the Running attempt:\n%s", out)
	}
	if strings.Contains(out, "PROJ-2/0001") {
		t.Errorf("default view should hide the Done attempt:\n%s", out)
	}

	// --all restores the Done attempt.
	for _, flag := range []string{"--all", "-a"} {
		out, code := run(t, "--data", dir, "ctl", "status", flag)
		if code != 0 {
			t.Fatalf("ctl status %s exited %d: %s", flag, code, out)
		}
		if !strings.Contains(out, "PROJ-2/0001") {
			t.Errorf("%s should include the Done attempt:\n%s", flag, out)
		}
	}

	// An explicit Done target prints regardless of the filter.
	out, code = run(t, "--data", dir, "ctl", "status", "PROJ-2@0001")
	if code != 0 {
		t.Fatalf("ctl status PROJ-2@0001 exited %d: %s", code, out)
	}
	if !strings.Contains(out, "PROJ-2/0001") {
		t.Errorf("an explicitly named Done attempt should print:\n%s", out)
	}
}

// TestCtlStatusEmptyHint shows the one-line hint (with the hidden count) when the
// default view filters everything away.
func TestCtlStatusEmptyHint(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001, Running
	root := store.Root{Dir: dir}
	if _, err := ticketlog.Append(root, "PROJ-1", "0001", event.Event{Type: "done", Actor: "human:test", Body: "closed"}); err != nil {
		t.Fatalf("append done: %v", err)
	}

	out, code := run(t, "--data", dir, "ctl", "status")
	if code != 0 {
		t.Fatalf("ctl status exited %d: %s", code, out)
	}
	if !strings.Contains(out, "no active attempts — 1 Done hidden; --all to show") {
		t.Errorf("expected the empty-view hint with a hidden count:\n%s", out)
	}
}

func TestContextGauge(t *testing.T) {
	cases := []struct {
		ctx, cap int
		want     string
	}{
		{0, 200_000, "ctx=-"},
		{24_000, 200_000, "ctx=12% (24,000/200,000)"},
		{1_000, 0, "ctx=1,000 tok"},
		{1_000_000, 1_000_000, "ctx=100% (1,000,000/1,000,000)"},
	}
	for _, c := range cases {
		if got := contextGauge(c.ctx, c.cap); got != c.want {
			t.Errorf("contextGauge(%d,%d) = %q, want %q", c.ctx, c.cap, got, c.want)
		}
	}
}

func TestCommas(t *testing.T) {
	cases := map[int]string{0: "0", 42: "42", 1000: "1,000", 1234567: "1,234,567", -1000: "-1,000"}
	for n, want := range cases {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestCtlLogsRenderAndJSON drives `ctl logs` end to end in both modes: the
// default renders a human-readable line, while --json reproduces the raw
// stream.jsonl verbatim for `| jq` pipelines.
func TestCtlLogsRenderAndJSON(t *testing.T) {
	dir := newTicket(t) // ticket PROJ-1, attempt 0001
	root := store.Root{Dir: dir}
	streamPath := root.SessionStreamPath("PROJ-1", "0001")
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
		t.Fatal(err)
	}
	rawLine := `{"type":"assistant","session_id":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello from the session."}]}}`
	if err := os.WriteFile(streamPath, []byte(rawLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Default: human-readable, prose not raw JSON, session id dropped.
	out, code := run(t, "--data", dir, "ctl", "logs", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("ctl logs exited %d: %s", code, out)
	}
	if !strings.Contains(out, "Hello from the session.") {
		t.Errorf("rendered output missing assistant prose:\n%s", out)
	}
	if strings.Contains(out, `"session_id"`) || strings.Contains(out, `"type":"assistant"`) {
		t.Errorf("rendered output leaked raw stream-json:\n%s", out)
	}

	// --json: byte-for-byte passthrough of the recorded line.
	out, code = run(t, "--data", dir, "ctl", "logs", "PROJ-1@0001", "--json")
	if code != 0 {
		t.Fatalf("ctl logs --json exited %d: %s", code, out)
	}
	if !strings.Contains(out, rawLine) {
		t.Errorf("--json did not reproduce the raw stream line:\n%s", out)
	}
}

// TestResolvePromptCache covers the prompt-cache resolution: the append defaults
// to draiver's shipped protocol (drvctl-034) with the exclude toggle still off and
// unprobed, a config file overrides the append verbatim, an empty override file
// disables the append, a missing override file is a hard error, and the exclude
// toggle fails loud when the agent lacks flag support but passes when it has it.
func TestResolvePromptCache(t *testing.T) {
	yes := func() (bool, error) { return true, nil }
	no := func() (bool, error) { return false, nil }
	probed := false
	spy := func() (bool, error) { probed = true; return true, nil }

	// Default: exclude off and unprobed, append defaults to the shipped protocol.
	exclude, appendPrompt, err := resolvePromptCache(config.Config{}, spy)
	if err != nil || exclude {
		t.Fatalf("default: got exclude=%v err=%v, want false/nil", exclude, err)
	}
	if appendPrompt != handbook.Content() {
		t.Errorf("default append = %q, want the shipped handbook protocol", appendPrompt)
	}
	if probed {
		t.Errorf("default (toggle off) must not probe agent support")
	}

	// A config file overrides the shipped protocol, read into the prefix verbatim.
	pf := filepath.Join(t.TempDir(), "protocol.txt")
	if err := os.WriteFile(pf, []byte("PROTOCOL ABOVE THE WALL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, appendPrompt, err = resolvePromptCache(config.Config{AppendSystemPromptFile: pf}, no)
	if err != nil {
		t.Fatalf("append file: unexpected err %v", err)
	}
	if appendPrompt != "PROTOCOL ABOVE THE WALL\n" {
		t.Errorf("append prompt = %q, want the file contents verbatim", appendPrompt)
	}

	// An empty override file disables the append (opt out of the shipped default).
	empty := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, appendPrompt, err = resolvePromptCache(config.Config{AppendSystemPromptFile: empty}, no); err != nil || appendPrompt != "" {
		t.Errorf("empty override: got append=%q err=%v, want \"\"/nil", appendPrompt, err)
	}

	// Named-but-missing append file is a hard error, not a silently-empty prefix.
	if _, _, err := resolvePromptCache(config.Config{AppendSystemPromptFile: filepath.Join(t.TempDir(), "nope.txt")}, no); err == nil {
		t.Errorf("missing append file: want error, got nil")
	}

	// Toggle on + agent supports the flag → exclude true.
	exclude, _, err = resolvePromptCache(config.Config{ExcludeDynamicSystemPromptSections: true}, yes)
	if err != nil || !exclude {
		t.Errorf("toggle on + supported: got exclude=%v err=%v, want true/nil", exclude, err)
	}

	// Toggle on + agent lacks the flag → fail loud.
	if _, _, err := resolvePromptCache(config.Config{ExcludeDynamicSystemPromptSections: true}, no); err == nil {
		t.Errorf("toggle on + unsupported: want a loud error, got nil")
	}

	// A probe that itself errors (e.g. the binary could not be run) surfaces as an
	// error too, so "couldn't check" is never mistaken for "supported".
	boom := func() (bool, error) { return false, context.DeadlineExceeded }
	if _, _, err := resolvePromptCache(config.Config{ExcludeDynamicSystemPromptSections: true}, boom); err == nil {
		t.Errorf("probe error: want error, got nil")
	}
}
