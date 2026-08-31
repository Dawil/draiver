package streamlog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/agent/claudecode"
	"github.com/Dawil/draiver/internal/reconcile"
)

// The role vocabulary event kinds map onto — a small, stable set that replaces
// the old glyph markers (`>`, `!`, `--`, `?`). Each renders in a distinct colour
// on a TTY so a reader can tell who is speaking without decoding a marker.
const (
	roleAssistant = "assistant"
	roleTool      = "tool"
	roleSystem    = "system"
	rolePerm      = "perm"
	roleError     = "error"
)

// Prefix column widths. The datetime is a fixed 19-column stamp
// (`2006-01-02 15:04:05`); the tokens column is right-aligned so it stays put as
// the number grows; the role column is left-aligned so the content start-column
// is stable. prefixWidth is the total visible width of the `[<prefix>]: ` string,
// used to leave the markdown renderer room to wrap inside the terminal.
const (
	tsLayout   = "2006-01-02 15:04:05"
	tokenWidth = 9
	roleWidth  = 9
	// "[" + 19 (datetime) + "  " + tokenWidth + "  " + roleWidth + "]: "
	prefixWidth = 1 + 19 + 2 + tokenWidth + 2 + roleWidth + 3
)

// ANSI attributes used on a TTY only. roleColor colours the role token in the
// prefix (not the datetime, tokens or brackets — drvctl-025 #17); ansiItalic
// marks tool output so it reads as visibly distinct from prose (drvctl-025 #8).
const (
	ansiReset  = "\033[0m"
	ansiItalic = "\033[3m"
)

func roleColor(role string) string {
	switch role {
	case roleAssistant:
		return "\033[36m" // cyan
	case roleTool:
		return "\033[33m" // yellow
	case roleSystem:
		return "\033[90m" // grey
	case rolePerm:
		return "\033[35m" // magenta
	case roleError:
		return "\033[31m" // red
	}
	return ""
}

// streamRenderer turns normalized events into the human-readable session render:
// a fixed-width, greppable `[<time>  <tokens>  <role>]: ` prefix on every line,
// followed by the event body — with assistant prose rendered from Markdown to
// styled ANSI (glamour over the goldmark parser this repo already carries).
//
// It is stateful, so one value is built per stream and reused for every line: it
// carries the last-known cumulative context-token count forward across the many
// events that report no usage, so the tokens column is populated on every line
// rather than only on usage frames. It is the single renderer shared by the live
// `ctl logs -f` follow and disk replay alike; transport fields (event uuids, the
// session id, envelope wrappers) are dropped.
//
// The timestamp is render-time wallclock captured as each line is emitted, NOT a
// recorded event time — the normalized event and the stream-json envelope carry
// none, and the on-disk stream.jsonl format and `--json` passthrough must stay
// byte-for-byte unchanged (drvctl-025 #3). It is truthful for the live follow and
// honest-but-approximate for historical replay.
type streamRenderer struct {
	out       io.Writer
	styled    bool // out is a TTY: colours + italics + ANSI markdown on
	md        *glamour.TermRenderer
	ctxTokens int // last-known cumulative context tokens; -1 until first usage
	now       func() time.Time
}

// newStreamRenderer builds a renderer bound to out, detecting whether out is a
// terminal. On a TTY it styles: role-coloured prefixes, italic tool output, and
// glamour's dark ANSI markdown wrapped to the terminal width minus the prefix.
// When out is not a terminal (a pipe, a file, the test buffer) it renders plain —
// glamour's notty style, which shows emphasis as literal `**bold**` markers
// rather than leaking ANSI escapes into a `| jq`/file consumer (drvctl-025 #5).
func newStreamRenderer(out io.Writer) *streamRenderer {
	r := &streamRenderer{out: out, ctxTokens: -1, now: time.Now}
	width := 100
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		r.styled = true
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > prefixWidth+20 {
			width = w
		}
	}
	wrap := width - prefixWidth
	if wrap < 20 {
		wrap = 20
	}
	base := styles.NoTTYStyleConfig
	profile := termenv.Ascii
	if r.styled {
		base = styles.DarkStyleConfig
		profile = termenv.ANSI256
	}
	md, err := glamour.NewTermRenderer(
		glamour.WithStyles(compactStyle(base)),
		glamour.WithColorProfile(profile),
		glamour.WithWordWrap(wrap),
	)
	if err == nil {
		r.md = md
	}
	return r
}

// compactStyle strips glamour's document framing — the 2-space margin and the
// leading/trailing blank lines it wraps every block in — so the rendered body
// sits flush against our prefix column with no gutter (drvctl-025 #5). It copies
// the base config by value; only the Document fields are replaced.
func compactStyle(base ansi.StyleConfig) ansi.StyleConfig {
	s := base
	zero := uint(0)
	s.Document.Margin = &zero
	s.Document.BlockPrefix = ""
	s.Document.BlockSuffix = ""
	return s
}

// renderEvent writes one normalized event. Assistant prose is rendered as styled
// Markdown; tool calls, tool errors, permission asks, turn boundaries and errors
// map to their role. Usage-only frames print nothing (a bare ctx/tok/$ line is
// noise, per drvctl-025 #8) but still advance the token tally the prefix carries.
func (r *streamRenderer) renderEvent(ev agent.Event) {
	// Fold any usage this event carries into the running tally first, so the
	// tokens column reflects it even on the line that reported it. Only a
	// positive count updates the tally: a result frame reports cost-only usage
	// with ContextTokens==0 (see claudecode.Normalize), which must not clobber
	// the last real context-window snapshot the column carries forward.
	if ev.Usage != nil && ev.Usage.ContextTokens > 0 {
		r.ctxTokens = ev.Usage.ContextTokens
	}
	switch ev.Kind {
	case agent.EventSystem:
		// The session id is a transport handle, not something a human reading the
		// stream needs; note only that the session came online.
		r.emit(roleSystem, "session online")
	case agent.EventAssistant:
		if ev.Thinking {
			return
		}
		if s := strings.TrimSpace(ev.Text); s != "" {
			r.emit(roleAssistant, s)
		}
	case agent.EventToolCall:
		if ev.Tool != nil {
			if s := toolSummary(ev.Tool.Input); s != "" {
				r.emit(roleTool, ev.Tool.Name+": "+s)
			} else {
				r.emit(roleTool, ev.Tool.Name)
			}
		}
	case agent.EventToolResult:
		if ev.Tool != nil && ev.Tool.IsError {
			r.emit(roleTool, ev.Tool.Name+" failed")
		}
	case agent.EventPermission:
		if ev.Permission != nil {
			r.emit(rolePerm, "permission: "+ev.Permission.Tool)
		}
	case agent.EventUsage:
		// The ctx/tok/$ figures ride along on the next real line's prefix; a
		// dedicated line for them is dropped (drvctl-025 #8).
		return
	case agent.EventTurnEnd:
		r.emit(roleSystem, "turn end ("+ev.Turn+")")
	case agent.EventError:
		r.emit(roleError, ev.Err)
	}
}

// emit writes body under role at render-time wallclock, one physical line at a
// time, each carrying the full `[<prefix>]: ` so every line stays independently
// greppable (drvctl-025 #8).
func (r *streamRenderer) emit(role, body string) {
	r.emitAt(role, r.now().Format(tsLayout), body)
}

// emitAt is emit with an explicit timestamp column, for a source that carries its
// own recorded time (the ctl.jsonl health log) rather than the stream's render-time
// approximation.
func (r *streamRenderer) emitAt(role, ts, body string) {
	prefix := r.prefixAt(role, ts)
	for _, line := range r.bodyLines(role, body) {
		fmt.Fprintln(r.out, prefix+line)
	}
}

// prefix renders the `[<datetime>  <tokens>  <role>]: ` column for one line. The
// bracket-and-colon are wrapped in `[` … `]: ` so a reader (or grep) can split
// prefix from content on a fixed delimiter; on a TTY only the role token is
// tinted by its colour — the datetime, tokens, brackets and delimiter stay
// uncoloured (drvctl-025 #17).
func (r *streamRenderer) prefix(role string) string {
	return r.prefixAt(role, r.now().Format(tsLayout))
}

// prefixAt renders the prefix column with an explicit datetime stamp, so a source
// carrying its own recorded time (ctl.jsonl health) sits in the same fixed-width
// column as the render-time-stamped agent stream.
func (r *streamRenderer) prefixAt(role, ts string) string {
	tok := "-"
	if r.ctxTokens >= 0 {
		tok = commas(r.ctxTokens)
	}
	// Pad the role to a fixed column by hand: on a TTY the colour escapes wrap
	// only the role word, so %-*s (which counts the escape bytes) would misalign
	// the closing bracket. Left-align the visible word, then trailing spaces.
	roleField := role
	if r.styled {
		roleField = roleColor(role) + role + ansiReset
	}
	if pad := roleWidth - len(role); pad > 0 {
		roleField += strings.Repeat(" ", pad)
	}
	return fmt.Sprintf("[%s  %*s  %s]: ", ts, tokenWidth, tok, roleField)
}

// bodyLines turns an event body into the physical lines to print under the
// prefix. Assistant prose is rendered from Markdown (styled ANSI on a TTY, plain
// otherwise), split into lines with glamour's padding and blank framing stripped;
// tool output is italicised on a TTY so it reads as visibly distinct; everything
// else is a single plain line.
func (r *streamRenderer) bodyLines(role, body string) []string {
	if role == roleAssistant && r.md != nil {
		if rendered, err := r.md.Render(body); err == nil {
			var lines []string
			for _, ln := range strings.Split(rendered, "\n") {
				if ln = trimTrailingBlank(ln); ln != "" {
					lines = append(lines, ln)
				}
			}
			if len(lines) > 0 {
				return lines
			}
		}
	}
	if role == roleTool && r.styled {
		return []string{ansiItalic + body + ansiReset}
	}
	return []string{body}
}

// trimTrailingBlank strips trailing whitespace from a rendered line, seeing
// through the per-cell colour escapes glamour uses to right-pad every line to the
// wrap width — a plain strings.TrimRight can't, because such a line ends in an
// ANSI reset, not a space (drvctl-025 #5). It skips CSI escape sequences while
// tracking the last visible non-space byte, drops everything past it, and (if any
// colour survived) re-appends a reset so no styling bleeds past the content. A
// line that is blank once its padding and escapes are removed returns "".
func trimTrailingBlank(s string) string {
	lastVisible := -1
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // ESC: skip a CSI sequence (ESC [ … final-byte 0x40–0x7e)
			j := i + 1
			if j < len(s) && s[j] == '[' {
				for j++; j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e); j++ {
				}
				if j < len(s) {
					j++ // include the final byte
				}
			}
			i = j
			continue
		}
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\r' {
			lastVisible = i
		}
		i++
	}
	if lastVisible < 0 {
		return ""
	}
	trimmed := s[:lastVisible+1]
	if strings.Contains(trimmed, "\x1b[") {
		trimmed += ansiReset
	}
	return trimmed
}

// toolSummary pulls a short, human-meaningful snippet out of a tool call's raw
// input — the primary field most tools key on (a command, a path, a pattern) —
// so a rendered `> tool` line shows what the call is doing, not just its name. It
// stays a single short line; input it cannot read as one of those fields yields
// "" and the caller prints the bare tool name.
func toolSummary(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return truncateOneLine(v, 72)
		}
	}
	return ""
}

// truncateOneLine collapses a value to a single short line for a one-liner
// render: it keeps only the first line and caps the length (rune-safe), marking
// with an ellipsis whenever it dropped anything.
func truncateOneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	truncated := false
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
		truncated = true
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
		truncated = true
	}
	if truncated {
		s += "…"
	}
	return s
}

// commas groups an integer into thousands for a readable gauge (12345 -> 12,345).
func commas(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// TailStream prints an attempt's recorded logs. In raw mode (--json) it emits the
// stream.jsonl lines verbatim, byte-for-byte, so `| jq` pipelines keep working —
// the machine form is unchanged, and ctl.jsonl health is *not* folded in (it is a
// human-render concern only). In render mode — the human-readable default — it
// interleaves two sources through one stateful renderer: the agent stream
// (stream.jsonl, normalized back into the live view's event vocabulary with
// transport noise dropped) and draiverctld's own operational health transitions
// (ctl.jsonl, drvctl-027). Without follow it prints what is on disk and returns;
// with follow it keeps emitting appended lines from both files until ctx is
// cancelled (Ctrl-C), waiting for either to appear if the session has not started.
//
// tail is the follow-mode backlog window over stream.jsonl: a follow is a "show me
// what's happening now" request, so rather than replay the whole recorded history
// on start it emits only the last `tail` stream records and then follows live.
// tail == 0 shows no backlog (only lines appended after the follow starts); tail
// < 0 replays the full history (today's behaviour, the escape hatch). The window
// is meaningful only under follow — without it the full history always prints (cat
// semantics) — and it applies to the stream.jsonl backlog only: the interleaved
// ctl.jsonl health lines are sparse and kept in full (drvctl-030).
func TailStream(ctx context.Context, out io.Writer, streamPath, ctlPath string, follow, render bool, tail int) error {
	// The backlog window is a follow-mode concern; a plain `logs` (cat) always
	// prints the whole file, and health is never windowed.
	streamTail := -1
	if follow {
		streamTail = tail
	}
	if !render {
		return tailRaw(ctx, out, streamPath, follow, streamTail)
	}

	// One renderer for both sources: it carries the running token tally forward, and
	// a shared prefix column keeps the two visually aligned as they interleave.
	sr := newStreamRenderer(out)
	stream := &lineReader{path: streamPath, tail: streamTail}
	ctl := &lineReader{path: ctlPath, tail: -1}
	defer stream.close()
	defer ctl.close()

	printed := false
	for {
		n := stream.drain(func(line string) { sr.renderLine(line); printed = true })
		n += ctl.drain(func(line string) { sr.renderHealthLine(line); printed = true })
		if !follow {
			// Flush any partial trailing line held for a newline that will not come.
			if stream.flush(func(line string) { sr.renderLine(line); printed = true }) {
				printed = true
			}
			if ctl.flush(func(line string) { sr.renderHealthLine(line); printed = true }) {
				printed = true
			}
			if !printed {
				fmt.Fprintln(out, "(no session stream yet)")
			}
			return nil
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
}

// tailRaw is the --json path: the stream.jsonl bytes, verbatim. It preserves the
// exact pre-drvctl-027 behaviour — a `| jq` / replay consumer sees the on-disk
// journal untouched, and health never enters this stream. In follow mode it honours
// the same backlog window as the render path (tail records, seeked to a record
// boundary so the bytes stay byte-for-byte); non-follow always emits the full file
// (tail < 0), which is where replay-from-start is served.
func tailRaw(ctx context.Context, out io.Writer, path string, follow bool, tail int) error {
	f, err := openStream(ctx, path, follow)
	if err != nil {
		return err
	}
	if f == nil {
		if !follow {
			fmt.Fprintln(out, "(no session stream yet)")
		}
		return nil
	}
	defer f.Close()
	if off, err := tailOffset(f, tail); err == nil {
		_, _ = f.Seek(off, io.SeekStart)
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			_, _ = io.WriteString(out, line)
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return err
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// tailOffset returns the byte offset in f from which reading yields the last n
// complete (newline-terminated) lines — the seek target for a follow's backlog
// window. n < 0 means the whole file (offset 0); n == 0 means only content
// appended after the current end (offset == size). It scans backward from EOF
// counting record-terminating newlines, ignoring a single trailing newline at the
// very end (which terminates the last line rather than starting a new one), and
// stops at the start of the nth-from-last line; a file with fewer than n lines
// yields offset 0 (the whole file). A trailing partial line (no newline yet) is
// kept within the window so its bytes reach the reader's pending buffer.
func tailOffset(f *os.File, n int) (int64, error) {
	if n < 0 {
		return 0, nil
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if n == 0 || size == 0 {
		return size, nil
	}
	const chunk = 32 * 1024
	buf := make([]byte, chunk)
	count := 0
	pos := size
	skipTrailing := true // the final '\n' terminates the last line, not a new one
	for pos > 0 {
		readSize := int64(chunk)
		if pos < readSize {
			readSize = pos
		}
		start := pos - readSize
		if _, err := f.ReadAt(buf[:readSize], start); err != nil && err != io.EOF {
			return 0, err
		}
		for i := int(readSize) - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			abs := start + int64(i)
			if skipTrailing && abs == size-1 {
				skipTrailing = false
				continue
			}
			skipTrailing = false
			count++
			if count == n {
				return abs + 1, nil
			}
		}
		pos = start
	}
	return 0, nil
}

// lineReader tails one append-only file a line at a time, opening it lazily (so a
// file that does not exist yet is simply skipped until it appears) and holding a
// partial trailing line until its newline arrives — so a half-written JSON object
// is never handed to a parser. drain reads every complete line available right now;
// flush releases a held partial line at end-of-input in non-follow mode. It is the
// tailing primitive TailStream interleaves the agent stream and the health log
// through.
type lineReader struct {
	path string
	// tail is the initial backlog window, applied once when the file is first
	// opened: keep the last `tail` complete lines (tail < 0 = the whole file, the
	// default; tail == 0 = none, i.e. seek to the current end and only report lines
	// appended afterwards). Later lines appended past the initial seek are always
	// reported — the window bounds the backlog, not the follow.
	tail    int
	f       *os.File
	r       *bufio.Reader
	pending string
}

// drain reads all currently-available complete lines, calling emit for each, and
// returns how many it emitted. It lazily opens the file on first use (and each
// call, until it exists). A bufio.Reader does not cache EOF, so re-draining the
// same reader after the file has grown yields the new lines — the tail property.
func (lr *lineReader) drain(emit func(string)) int {
	if lr.f == nil {
		f, err := os.Open(lr.path)
		if err != nil {
			return 0 // not created yet (or unreadable) — try again next drain
		}
		lr.f = f
		// Apply the backlog window once, at open: seek past everything but the last
		// `tail` records so a follow starts near the tip instead of replaying the
		// whole history. tailOffset lands on a record boundary, so the reader still
		// only ever sees complete lines.
		if off, err := tailOffset(f, lr.tail); err == nil {
			_, _ = f.Seek(off, io.SeekStart)
		}
		lr.r = bufio.NewReader(f)
	}
	n := 0
	for {
		line, err := lr.r.ReadString('\n')
		if len(line) > 0 {
			if strings.HasSuffix(line, "\n") {
				emit(lr.pending + line)
				lr.pending = ""
				n++
			} else {
				lr.pending += line
			}
		}
		if err != nil {
			return n // EOF (or read error): stop this pass, keep position for the next
		}
	}
}

// flush emits any held partial trailing line (a final record written without a
// newline), used only at end-of-input in non-follow mode. It reports whether it
// emitted anything.
func (lr *lineReader) flush(emit func(string)) bool {
	if lr.pending == "" {
		return false
	}
	emit(lr.pending)
	lr.pending = ""
	return true
}

func (lr *lineReader) close() {
	if lr.f != nil {
		lr.f.Close()
	}
}

// renderLine normalizes one recorded stream-json line and renders the events it
// yields through the shared renderer; lines that carry only transport noise
// normalize to nothing and print nothing.
func (r *streamRenderer) renderLine(line string) {
	for _, ev := range claudecode.Normalize([]byte(line)) {
		r.renderEvent(ev)
	}
}

// renderHealthLine renders one session/ctl.jsonl record — a daemon health
// transition — through the shared prefix vocabulary so it interleaves with the
// agent stream. An error-start is an error-role line (the trouble beginning, with
// its message); an error-end is a system-role line (it cleared). Unlike the agent
// stream, a health record carries its own recorded timestamp, so the prefix uses
// that rather than render-time wallclock. A malformed or unknown record prints
// nothing.
func (r *streamRenderer) renderHealthLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev reconcile.HealthEvent
	if json.Unmarshal([]byte(line), &ev) != nil {
		return
	}
	ts := r.now().Format(tsLayout)
	if t, err := time.Parse(time.RFC3339, ev.TS); err == nil {
		ts = t.Local().Format(tsLayout)
	}
	switch ev.Event {
	case "start":
		body := "ctl: " + ev.Class + " started"
		if ev.Message != "" {
			body += ": " + ev.Message
		}
		r.emitAt(roleError, ts, body)
	case "end":
		r.emitAt(roleSystem, ts, "ctl: "+ev.Class+" cleared")
	}
}

// openStream opens the stream file, waiting for it to appear when following. It
// returns (nil, nil) when the file is absent and we are not following — a session
// that simply has not produced a stream yet, which is not an error.
func openStream(ctx context.Context, path string, follow bool) (*os.File, error) {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if !follow {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}
