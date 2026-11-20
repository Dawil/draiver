package metrics

import (
	"encoding/json"
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

// Call is a classified tool call: its class, the file paths it reads or edits (as
// written; the fold relativizes them), and a normalized key identifying a search
// query so repeated searches can be spotted.
type Call struct {
	Class Class
	Paths []string
	// Query is the normalized search key for a search-class call, "" otherwise.
	Query string
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
	switch c.Class {
	case ClassRead, ClassEdit:
		if p := firstNonEmpty(in.FilePath, in.NotebookPath); p != "" {
			c.Paths = []string{p}
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
	segSplit     = regexp.MustCompile(`&&|\|\||;|\n`)
	envAssign    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	heredocStart = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	redirectTo   = regexp.MustCompile(`(?:^|[^0-9&<>])>>?\s*([^\s&|;<>]+)`)
)

// ClassifyBash classifies a shell command line. The line is split into sequential
// segments (&&, ||, ;, newline). In each segment only the command before the first
// pipe counts: a trailing `| head` or `| grep x` filters output and does not make
// the call a read or a search. The highest-priority segment class wins, and paths
// are collected from every segment.
func ClassifyBash(cmd string) Call {
	best := Call{Class: ClassOther}
	var paths []string
	for _, seg := range segSplit.Split(stripHeredocs(cmd), -1) {
		c := classifySegment(seg)
		paths = append(paths, c.Paths...)
		if classPriority[c.Class] > classPriority[best.Class] {
			best.Class = c.Class
			best.Query = c.Query
		}
	}
	best.Paths = paths
	return best
}

// stripHeredocs drops heredoc bodies (`<<'EOF' ... EOF`), whose text is data, not
// commands, keeping the introducing line.
func stripHeredocs(cmd string) string {
	lines := strings.Split(cmd, "\n")
	var out []string
	var term string
	for _, l := range lines {
		if term != "" {
			if strings.TrimSpace(l) == term {
				term = ""
			}
			continue
		}
		out = append(out, l)
		if m := heredocStart.FindStringSubmatch(l); m != nil {
			term = m[1]
		}
	}
	return strings.Join(out, "\n")
}

// classifySegment classifies one sequential segment.
func classifySegment(seg string) Call {
	pipeline := strings.SplitN(seg, "|", 2)
	head := strings.TrimSpace(pipeline[0])
	// A redirect into a file is an edit, whatever produced the output
	// (`cat > f <<EOF`, `echo x >> f`). /dev/null and fd duplication are not edits.
	var editPaths []string
	for _, m := range redirectTo.FindAllStringSubmatch(head, -1) {
		if p := strings.Trim(m[1], `'"`); p != "" && !strings.HasPrefix(p, "/dev/") {
			editPaths = append(editPaths, p)
		}
	}
	words := shellWords(head)
	for len(words) > 0 && (wrappers[words[0]] || envAssign.MatchString(words[0]) || words[0] == "(" || words[0] == "{") {
		words = words[1:]
	}
	// `timeout 60 go test` → `go test`
	if len(words) > 1 && words[0] == "timeout" {
		words = words[2:]
	}
	if len(words) == 0 {
		return Call{Class: ClassOther}
	}
	name := path.Base(words[0])
	if strings.HasPrefix(words[0], "./") {
		name = words[0] // keep ./gradlew style launchers distinct
	}
	if name == "cd" || name == "export" || name == "echo" || name == "printf" || name == "true" || name == "set" {
		if len(editPaths) > 0 {
			return Call{Class: ClassEdit, Paths: editPaths}
		}
		return Call{Class: ClassOther}
	}
	args := words[1:]
	rest := strings.Join(nonFlagsPrefix(args), " ")
	class := ClassOther
	for _, r := range bashRules {
		if r.cmd != name {
			continue
		}
		joined := strings.Join(args, " ")
		if r.sub == "" || rest == r.sub || strings.HasPrefix(rest, r.sub+" ") || joined == r.sub || strings.HasPrefix(joined, r.sub+" ") {
			class = r.class
			break
		}
	}
	// In-place editors are edits, not reads.
	if (name == "sed" || name == "perl") && hasInPlace(args) {
		return Call{Class: ClassEdit, Paths: append(editPaths, lastNonFlag(args)...)}
	}
	if name == "tee" || name == "mv" || name == "cp" || name == "rm" || name == "touch" || name == "patch" {
		return Call{Class: ClassEdit, Paths: append(editPaths, lastNonFlag(args)...)}
	}
	if len(editPaths) > 0 && class != ClassTest && class != ClassBuild {
		return Call{Class: ClassEdit, Paths: editPaths}
	}
	c := Call{Class: class}
	switch class {
	case ClassRead:
		if readArgCmds[name] {
			c.Paths = fileArgs(args)
		} else if name == "sed" || name == "awk" || name == "jq" {
			// sed -n '1,80p' FILE / awk PROG FILE / jq EXPR FILE: the last arg is the file.
			c.Paths = lastNonFlag(args[min(1, len(args)):])
		}
	case ClassSearch:
		c.Query = normQuery(head)
	}
	return c
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
