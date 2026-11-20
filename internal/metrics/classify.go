package metrics

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Class is the coarse kind of a tool call (drv-054). Every call, including each Bash
// command, gets exactly one class, so exploration done via Bash or a subagent counts
// as exploration.
type Class string

const (
	ClassRead     Class = "read"
	ClassSearch   Class = "search"
	ClassEdit     Class = "edit"
	ClassBuild    Class = "build"
	ClassTest     Class = "test"
	ClassVCS      Class = "vcs"
	ClassSubagent Class = "subagent"
	ClassOther    Class = "other"
)

// Call is a classified tool call: its class, the file paths it reads and the ones
// it edits (as written; the fold relativizes them), and a normalized key
// identifying a search query so repeated searches can be spotted.
//
// Reads and Edits are collected independently of Class. A compound Bash call
// (`cat >> f <<EOF ... EOF; go build`) has one class (build) but still edits f, and
// `cat a; grep x` is a search that still reads a.
type Call struct {
	Class Class
	Reads []string
	Edits []string
	// Query is the normalized search key for a search-class call, "" otherwise.
	Query string
	// Range names the slice of Reads a ranged read took (Read offset/limit, `sed -n`,
	// `head`, `tail`, `awk`); "" is a whole-file read.
	Range string
}

// toolClasses maps non-Bash tool names to their class. Tools not listed are other.
// Web tools are other: they explore the internet, not the codebase.
var toolClasses = map[string]Class{
	"Read":         ClassRead,
	"NotebookRead": ClassRead,
	"Grep":         ClassSearch,
	"Glob":         ClassSearch,
	"LS":           ClassSearch,
	"Edit":         ClassEdit,
	"MultiEdit":    ClassEdit,
	"Write":        ClassEdit,
	"NotebookEdit": ClassEdit,
	"Task":         ClassSubagent,
	"Agent":        ClassSubagent,
}

// toolInput is the superset of input fields the non-Bash tools carry.
type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Path         string `json:"path"`
	Pattern      string `json:"pattern"`
	Glob         string `json:"glob"`
	Type         string `json:"type"`
	Command      string `json:"command"`
	Offset       int    `json:"offset"`
	Limit        int    `json:"limit"`
}

// bashCommand extracts a Bash tool call's command text from its raw input.
func bashCommand(input json.RawMessage) string {
	var in toolInput
	_ = json.Unmarshal(input, &in)
	return in.Command
}

// Classify classifies one tool call by tool name and raw input.
func Classify(tool string, input json.RawMessage) Call {
	var in toolInput
	_ = json.Unmarshal(input, &in)
	if tool == "Bash" {
		return ClassifyBash(in.Command)
	}
	c := Call{Class: toolClasses[tool]}
	if c.Class == "" {
		c.Class = ClassOther
	}
	p := firstNonEmpty(in.FilePath, in.NotebookPath)
	switch c.Class {
	case ClassRead:
		if p != "" {
			c.Reads = []string{p}
		}
		if in.Offset > 0 || in.Limit > 0 {
			c.Range = fmt.Sprintf("lines %d+%d", in.Offset, in.Limit)
		}
	case ClassEdit:
		if p != "" {
			c.Edits = []string{p}
		}
	case ClassSearch:
		c.Query = normQuery(tool + " " + in.Pattern + " " + in.Glob + " " + in.Type + " " + in.Path)
	}
	return c
}

// bashRule is one row of the Bash heuristics table: a command (the first word after
// wrappers are stripped), an optional subcommand prefix it must start with, and the
// class it implies. Rules are matched in order, and the first match wins within a
// segment, so specific rows (`git grep`) precede general ones (`git`).
type bashRule struct {
	cmd   string
	sub   string // required leading args, e.g. "test" for `go test`; "" matches any
	class Class
}

// bashRules is THE table of Bash command heuristics. Keep it the single place they
// live; classify_test.go pins each row.
var bashRules = []bashRule{
	// test runners
	{"go", "test", ClassTest},
	{"npm", "test", ClassTest},
	{"npm", "run test", ClassTest},
	{"npm", "t", ClassTest},
	{"yarn", "test", ClassTest},
	{"pnpm", "test", ClassTest},
	{"npx", "jest", ClassTest},
	{"npx", "vitest", ClassTest},
	{"npx", "playwright test", ClassTest},
	{"jest", "", ClassTest},
	{"vitest", "", ClassTest},
	{"pytest", "", ClassTest},
	{"python", "-m pytest", ClassTest},
	{"python3", "-m pytest", ClassTest},
	{"python", "-m unittest", ClassTest},
	{"python3", "-m unittest", ClassTest},
	{"tox", "", ClassTest},
	{"make", "test", ClassTest},
	{"make", "check", ClassTest},
	{"cargo", "test", ClassTest},
	{"draiver", "test", ClassTest},
	{"mvn", "test", ClassTest},
	{"gradle", "test", ClassTest},
	{"./gradlew", "test", ClassTest},
	{"rspec", "", ClassTest},
	{"bundle", "exec rspec", ClassTest},
	{"dotnet", "test", ClassTest},
	// builds, type-checks, vets
	{"go", "build", ClassBuild},
	{"go", "vet", ClassBuild},
	{"go", "install", ClassBuild},
	{"npm", "run build", ClassBuild},
	{"yarn", "build", ClassBuild},
	{"pnpm", "build", ClassBuild},
	{"tsc", "", ClassBuild},
	{"npx", "tsc", ClassBuild},
	{"cargo", "build", ClassBuild},
	{"cargo", "check", ClassBuild},
	{"make", "", ClassBuild},
	{"mvn", "", ClassBuild},
	{"gradle", "", ClassBuild},
	{"dotnet", "build", ClassBuild},
	// version control (search-like git subcommands first)
	{"git", "grep", ClassSearch},
	{"git", "ls-files", ClassSearch},
	{"git", "ls-tree", ClassSearch},
	{"git", "", ClassVCS},
	{"gh", "", ClassVCS},
	// search
	{"grep", "", ClassSearch},
	{"egrep", "", ClassSearch},
	{"fgrep", "", ClassSearch},
	{"rg", "", ClassSearch},
	{"ag", "", ClassSearch},
	{"ack", "", ClassSearch},
	{"find", "", ClassSearch},
	{"fd", "", ClassSearch},
	{"ls", "", ClassSearch},
	{"tree", "", ClassSearch},
	{"locate", "", ClassSearch},
	// read
	{"cat", "", ClassRead},
	{"head", "", ClassRead},
	{"tail", "", ClassRead},
	{"less", "", ClassRead},
	{"more", "", ClassRead},
	{"bat", "", ClassRead},
	{"nl", "", ClassRead},
	{"wc", "", ClassRead},
	{"sed", "", ClassRead}, // `sed -i` is promoted to edit in classifySegment
	{"awk", "", ClassRead},
	{"jq", "", ClassRead},
	{"diff", "", ClassRead},
}

// readArgCmds are the read commands whose non-flag arguments are file paths.
var readArgCmds = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true,
	"bat": true, "nl": true, "wc": true,
}

// classPriority orders classes when a compound command (`a && b; c`) spans several:
// the most consequential wins, so `cd x && go test ./...` is a test and
// `ls && cat f` is a search.
var classPriority = map[Class]int{
	ClassTest: 7, ClassBuild: 6, ClassEdit: 5, ClassVCS: 4, ClassSearch: 3, ClassRead: 2, ClassSubagent: 1, ClassOther: 0,
}

// wrappers are leading words stripped before the command word is read.
var wrappers = map[string]bool{"sudo": true, "time": true, "nice": true, "nohup": true, "command": true, "exec": true, "env": true}

var (
	envAssign    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	heredocStart = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	redirectTo   = regexp.MustCompile(`(?:^|[^0-9&<>])>>?\s*([^\s&|;<>]+)`)
)

// ClassifyBash classifies a shell command line. Heredoc bodies are set aside (they
// are data, not commands), then the line is split, quote-aware, into sequential
// segments (&&, ||, ;, newline). In each segment only the command before the first
// pipe sets the class: a trailing `| head` or `| grep x` filters output and does not
// make the call a read or a search. The highest-priority segment class wins. Reads
// and edits are collected from every segment, and from interpreter heredocs
// (`python3 - <<'EOF'`) that write files.
func ClassifyBash(cmd string) Call {
	best := Call{Class: ClassOther}
	script, bodies := splitHeredocs(cmd)
	for _, seg := range splitSegments(script) {
		c := classifySegment(seg)
		best.Reads = append(best.Reads, c.Reads...)
		best.Edits = append(best.Edits, c.Edits...)
		if c.Range != "" && best.Range == "" {
			best.Range = c.Range
		}
		if classPriority[c.Class] > classPriority[best.Class] {
			best.Class = c.Class
			best.Query = c.Query
		}
	}
	for _, h := range bodies {
		if !interpreters[commandWord(h.intro)] {
			continue
		}
		if edits := scriptWrites(h.body); len(edits) > 0 {
			best.Edits = append(best.Edits, edits...)
			if classPriority[ClassEdit] > classPriority[best.Class] {
				best.Class, best.Query = ClassEdit, ""
			}
		}
	}
	return best
}

// heredoc is one heredoc: the line that introduced it and its body.
type heredoc struct{ intro, body string }

// splitHeredocs separates heredoc bodies (`<<'EOF' ... EOF`) from the command text,
// keeping each introducing line in the script.
func splitHeredocs(cmd string) (string, []heredoc) {
	var out []string
	var docs []heredoc
	var term string
	var body []string
	for _, l := range strings.Split(cmd, "\n") {
		if term != "" {
			if strings.TrimSpace(l) == term {
				docs[len(docs)-1].body = strings.Join(body, "\n")
				term, body = "", nil
			} else {
				body = append(body, l)
			}
			continue
		}
		out = append(out, l)
		if m := heredocStart.FindStringSubmatch(l); m != nil {
			term = m[1]
			docs = append(docs, heredoc{intro: l})
		}
	}
	if term != "" { // unterminated: keep what there is
		docs[len(docs)-1].body = strings.Join(body, "\n")
	}
	return strings.Join(out, "\n"), docs
}

// splitSegments splits a script into sequential segments on &&, ||, ; and newline,
// ignoring separators inside single or double quotes.
func splitSegments(script string) []string {
	var segs []string
	var cur strings.Builder
	var quote rune
	rs := []rune(script)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' && i+1 < len(rs) {
				cur.WriteRune(r)
				i++
				r = rs[i]
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ';' || r == '\n':
			segs = append(segs, cur.String())
			cur.Reset()
			continue
		case (r == '&' || r == '|') && i+1 < len(rs) && rs[i+1] == r:
			segs = append(segs, cur.String())
			cur.Reset()
			i++
			continue
		}
		cur.WriteRune(r)
	}
	return append(segs, cur.String())
}

// pipelineHead returns the first command of a pipeline, ignoring | inside quotes.
func pipelineHead(seg string) string {
	var quote rune
	for i, r := range seg {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '|':
			return seg[:i]
		}
	}
	return seg
}

// interpreters are the commands whose heredoc body is a program, scanned for the
// files it writes.
var interpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "deno": true, "bun": true,
}

var (
	// scriptWriteRe spots a program that writes files: Python open(..., 'w'/'a'/'x'),
	// pathlib write_text/write_bytes, Node writeFileSync/appendFileSync, Ruby File.write.
	scriptWriteRe = regexp.MustCompile(`open\([^)\n]*,\s*['"][wax]b?\+?['"]|\.write_(text|bytes)\(|(write|append)FileSync\(|File\.write\(`)
	// scriptPathRe captures a relative file path literal used as an open() / Path()
	// argument or assigned to a variable.
	scriptPathRe = regexp.MustCompile(`(?:open\(|Path\(|FileSync\(|File\.write\(|=)\s*['"]([A-Za-z0-9_.][A-Za-z0-9_./-]*\.[A-Za-z0-9]+)['"]`)
)

// scriptWrites returns the file paths an interpreter heredoc writes, nil when it
// writes nothing. It is a heuristic: a writing program's path literals are taken as
// its targets.
func scriptWrites(body string) []string {
	if !scriptWriteRe.MatchString(body) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range scriptPathRe.FindAllStringSubmatch(body, -1) {
		p := m[1]
		if seen[p] || (strings.HasPrefix(p, ".") && !strings.HasPrefix(p, "./")) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// commandWord is the command name of a segment after wrappers and env assignments.
func commandWord(seg string) string {
	words := stripWrappers(shellWords(strings.TrimSpace(pipelineHead(seg))))
	if len(words) == 0 {
		return ""
	}
	return path.Base(words[0])
}

func stripWrappers(words []string) []string {
	for len(words) > 0 && (wrappers[words[0]] || envAssign.MatchString(words[0]) || words[0] == "(" || words[0] == "{") {
		words = words[1:]
	}
	// `timeout 60 go test` → `go test`
	if len(words) > 1 && words[0] == "timeout" {
		words = words[2:]
	}
	return words
}

// rangedReaders take a slice of a file rather than the whole of it.
var rangedReaders = map[string]bool{"sed": true, "head": true, "tail": true, "awk": true}

// classifySegment classifies one sequential segment.
func classifySegment(seg string) Call {
	head := strings.TrimSpace(pipelineHead(seg))
	// A redirect into a file is an edit, whatever produced the output
	// (`cat > f <<EOF`, `echo x >> f`). /dev/null and fd duplication are not edits.
	// Redirects are found on a copy with quoted text masked, so `awk 'NR>1'` is not
	// a redirect into a file named 1. Masking preserves byte offsets.
	masked := maskQuotes(head)
	var editPaths []string
	for _, m := range redirectTo.FindAllStringSubmatchIndex(masked, -1) {
		if p := strings.Trim(head[m[2]:m[3]], `'"`); p != "" && !strings.HasPrefix(p, "/dev/") {
			editPaths = append(editPaths, p)
		}
	}
	words := stripWrappers(shellWords(stripRedirects(head, masked)))
	if len(words) == 0 {
		return Call{Class: ClassOther, Edits: editPaths}
	}
	name := path.Base(words[0])
	if strings.HasPrefix(words[0], "./") {
		name = words[0] // keep ./gradlew style launchers distinct
	}
	edit := func(paths ...string) Call { return Call{Class: ClassEdit, Edits: append(editPaths, paths...)} }
	if name == "cd" || name == "export" || name == "echo" || name == "printf" || name == "true" || name == "set" {
		if len(editPaths) > 0 {
			return edit()
		}
		return Call{Class: ClassOther}
	}
	args := words[1:]
	rest := strings.Join(nonFlagsPrefix(args), " ")
	joined := strings.Join(args, " ")
	class := ClassOther
	for _, r := range bashRules {
		if r.cmd != name {
			continue
		}
		if r.sub == "" || rest == r.sub || strings.HasPrefix(rest, r.sub+" ") || joined == r.sub || strings.HasPrefix(joined, r.sub+" ") {
			class = r.class
			break
		}
	}
	// In-place editors are edits, not reads.
	if (name == "sed" || name == "perl") && hasInPlace(args) {
		return edit(lastNonFlag(args)...)
	}
	switch name {
	case "tee", "touch", "patch", "rm":
		return edit(nonFlags(args)...)
	case "mv", "cp":
		if name == "mv" {
			return edit(nonFlags(args)...)
		}
		return edit(lastNonFlag(args)...)
	}
	c := Call{Class: class, Edits: editPaths}
	switch class {
	case ClassRead:
		if readArgCmds[name] {
			c.Reads = fileArgs(args)
		} else if name == "sed" || name == "awk" || name == "jq" {
			// sed -n '1,80p' FILE / awk PROG FILE / jq EXPR FILE: the last arg is the file.
			c.Reads = lastNonFlag(args[min(1, len(args)):])
		}
		if rangedReaders[name] && len(c.Reads) > 0 {
			c.Range = normQuery(head)
		}
	case ClassSearch:
		c.Query = normQuery(head)
	}
	// Output redirected into a file by a read or search is an edit of that file;
	// tests and builds that write a log are still tests and builds.
	if len(editPaths) > 0 && class != ClassTest && class != ClassBuild {
		c.Class = ClassEdit
	}
	return c
}

// stripRedirects removes redirections (`> f`, `2>&1`, `<<'EOF'`, `< in`) so their
// targets are not mistaken for command arguments.
var redirectRe = regexp.MustCompile(`\d*(>>?|<<-?|<)&?\s*(['"][^'"]*['"]|[^\s;&|<>]+)?`)

func stripRedirects(s, masked string) string {
	var b strings.Builder
	last := 0
	for _, m := range redirectRe.FindAllStringIndex(masked, -1) {
		b.WriteString(s[last:m[0]])
		b.WriteByte(' ')
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// maskQuotes returns s with every byte inside single or double quotes replaced by
// '_', the quotes themselves kept, so offsets line up with s.
func maskQuotes(s string) string {
	b := []byte(s)
	var quote byte
	for i := 0; i < len(b); i++ {
		switch {
		case quote != 0 && b[i] == quote:
			quote = 0
		case quote != 0:
			b[i] = '_'
		case b[i] == '\'' || b[i] == '"':
			quote = b[i]
		}
	}
	return string(b)
}

// shellWords splits a command into words, honouring single and double quotes.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	inWord := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// nonFlagsPrefix returns the leading arguments up to the first flag, so `go test
// -run X ./...` and `go -C dir test` both expose their subcommand.
func nonFlagsPrefix(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// fileArgs returns the non-flag arguments that look like paths. A numeric argument
// (the `50` in `head -n 50`) is skipped.
func fileArgs(args []string) []string {
	var out []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "-n" || a == "-c" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "-") || a == "" || isNumber(a) || strings.ContainsAny(a, "<>") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// nonFlags returns every non-flag argument.
func nonFlags(args []string) []string {
	var out []string
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

// lastNonFlag returns the last non-flag argument, the file of `sed -i 's/a/b/' f`.
func lastNonFlag(args []string) []string {
	for i := len(args) - 1; i >= 0; i-- {
		if a := args[i]; a != "" && !strings.HasPrefix(a, "-") && !strings.ContainsAny(a, "<>") {
			return []string{a}
		}
	}
	return nil
}

func hasInPlace(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-i") || a == "--in-place" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "i") && len(a) <= 4) {
			return true
		}
	}
	return false
}

func isNumber(s string) bool {
	for _, r := range strings.TrimPrefix(s, "+") {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

var wsRun = regexp.MustCompile(`\s+`)

// normQuery normalizes a search for repeat detection: whitespace is collapsed,
// quotes are dropped and case is folded, so two searches that differ only by
// formatting count as the same query.
func normQuery(q string) string {
	q = strings.NewReplacer(`"`, "", `'`, "").Replace(q)
	return strings.ToLower(strings.TrimSpace(wsRun.ReplaceAllString(q, " ")))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
