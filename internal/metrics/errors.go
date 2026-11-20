package metrics

import (
	"regexp"
	"strings"
)

// ErrKind is the kind of a failed tool call (drv-054). Errors are broken down by
// kind and never blended into one rate, because the kinds say different things:
// env-discovery errors are the AI-accessibility signal; a red test is normal TDD;
// edit-mechanics errors and permission denials say nothing about the codebase.
type ErrKind string

const (
	ErrNone          ErrKind = ""
	ErrEnvDiscovery  ErrKind = "env_discovery"
	ErrTestFailure   ErrKind = "test_failure"
	ErrBuildFailure  ErrKind = "build_failure"
	ErrEditMechanics ErrKind = "edit_mechanics"
	ErrPermission    ErrKind = "permission_denial"
	ErrOther         ErrKind = "other"
)

// errPattern is one row of the error-kind table: a case-insensitive regexp over the
// tool result text and the kind it implies.
type errPattern struct {
	re   *regexp.Regexp
	kind ErrKind
}

func rx(s string) *regexp.Regexp { return regexp.MustCompile(`(?i)` + s) }

// permissionPatterns: draiver's gates (protocol gate, permission gate) and the
// agent harness refusing or awaiting approval for a call.
var permissionPatterns = []*regexp.Regexp{
	rx(`^Protocol gate:`),
	rx(`^Permission gate:`),
	rx(`requires approval`),
	rx(`require approval`),
	rx(`requested permissions? to`),
	rx(`haven't granted`),
	rx(`^Permission to use \S+ denied`),
	rx(`^<tool_use_error>Blocked:`),
	rx(`user doesn't want to proceed`),
}

// editMechanicsPatterns: the agent's tool use itself was malformed. Edit
// `old_string` mismatches, stale reads, schema/input validation.
var editMechanicsPatterns = []*regexp.Regexp{
	rx(`String to replace not found`),
	rx(`Found \d+ matches of the string to replace`),
	rx(`File has been modified since read`),
	rx(`File has not been read yet`),
	rx(`InputValidationError`),
	rx(`old_string and new_string are (exactly )?the same`),
	rx(`EISDIR`),
}

// envDiscoveryPatterns: the agent could not find or drive the environment.
// Unknown commands, wrong invocations, missing files/scripts/binaries, missing
// env or auth. These are the AI-accessibility signal.
var envDiscoveryPatterns = []*regexp.Regexp{
	rx(`^Exit code 127\b`),
	rx(`command not found`),
	rx(`: not found\b`),
	rx(`unknown (flag|command|option|shorthand flag)`),
	rx(`unrecognized (option|argument|arguments)`),
	rx(`invalid (option|flag)`),
	rx(`(^|\n)usage: `),
	rx(`No such file or directory`),
	rx(`^File does not exist`),
	rx(`cannot find (module|package)`),
	rx(`no rule to make target`),
	rx(`Missing script`),
	rx(`is not set\b`),
	rx(`environment variable`),
	rx(`(unauthori[sz]ed|authentication (failed|required)|permission denied \(publickey\)|could not read from remote|does not appear to be a git repository|not logged in)`),
}

// cobraUnknown matches a cobra CLI rejecting its invocation: `unknown command "x" for
// "y"`, `unknown flag: --x`, `unknown shorthand flag: 'x' in -x`. It is env-discovery
// regardless of exit code or position in the output: an agent that pipes or echoes
// `exit=$?` gets a successful tool result, but it still drove a binary that doesn't
// have the verb (typically a stale installed build, drv-054 #19).
var cobraUnknown = rx(`unknown command "[^"\n]+" for "[^"\n]+"|unknown (shorthand )?flag: `)

// IsCobraUnknown reports whether output carries a cobra unknown-command/flag error.
func IsCobraUnknown(result string) bool { return cobraUnknown.MatchString(result) }

// ClassifyError assigns a failed call to a kind. class is the call's class, so a
// failed test run is a test failure and not a generic error. Ordering matters:
// permission and edit-mechanics outcomes are unambiguous; an env-discovery
// signature in the first lines (e.g. `go: command not found`, exit 127) beats a
// test or build failure, because the run never got going. For test and build calls
// only the head of the output is inspected, so a failing test that merely logs "No
// such file" is still a test failure.
func ClassifyError(class Class, result string) ErrKind {
	text := strings.TrimSpace(result)
	for _, re := range permissionPatterns {
		if re.MatchString(text) {
			return ErrPermission
		}
	}
	for _, re := range editMechanicsPatterns {
		if re.MatchString(text) {
			return ErrEditMechanics
		}
	}
	if cobraUnknown.MatchString(text) {
		return ErrEnvDiscovery
	}
	probe := text
	if class == ClassTest || class == ClassBuild {
		probe = firstLines(text, 3)
	}
	for _, re := range envDiscoveryPatterns {
		if re.MatchString(probe) {
			return ErrEnvDiscovery
		}
	}
	switch class {
	case ClassTest:
		return ErrTestFailure
	case ClassBuild:
		return ErrBuildFailure
	}
	return ErrOther
}

// emptySearchPatterns mark a search that came back with nothing.
var emptySearchPatterns = []*regexp.Regexp{
	rx(`^No matches found`),
	rx(`^No files found`),
	rx(`^Found 0 `),
	rx(`^Exit code 1$`), // grep/rg exit 1 with no output = no match
}

// IsEmptySearch reports whether a search call's result shows no hits.
func IsEmptySearch(result string) bool {
	text := strings.TrimSpace(result)
	if text == "" || text == "(no output)" {
		return true
	}
	for _, re := range emptySearchPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
