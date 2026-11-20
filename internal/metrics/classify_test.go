package metrics

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestClassifyBash pins the Bash heuristics table: each row is a command line and
// the class, reads and edits it must produce.
func TestClassifyBash(t *testing.T) {
	cases := []struct {
		cmd   string
		class Class
		reads []string
		edits []string
	}{
		// read
		{"cat internal/a.go", ClassRead, []string{"internal/a.go"}, nil},
		{"head -n 50 README.md", ClassRead, []string{"README.md"}, nil},
		{"tail -20 a.log b.log", ClassRead, []string{"a.log", "b.log"}, nil},
		{"sed -n '1,80p' cmd/root.go", ClassRead, []string{"cmd/root.go"}, nil},
		{"less docs/x.md", ClassRead, []string{"docs/x.md"}, nil},
		{"wc -l internal/a.go internal/b.go", ClassRead, []string{"internal/a.go", "internal/b.go"}, nil},
		{"cat a.go | head -5", ClassRead, []string{"a.go"}, nil},
		// search
		{"grep -rn foo internal", ClassSearch, nil, nil},
		{"rg Pattern", ClassSearch, nil, nil},
		{"find . -name '*.go'", ClassSearch, nil, nil},
		{"ls internal", ClassSearch, nil, nil},
		{"fd go", ClassSearch, nil, nil},
		{"git grep foo", ClassSearch, nil, nil},
		{"git ls-files", ClassSearch, nil, nil},
		{"go doc ./x | grep y", ClassOther, nil, nil},
		// test
		{"go test ./...", ClassTest, nil, nil},
		{"go test -run TestX ./internal/x", ClassTest, nil, nil},
		{"cd sub && go test ./...", ClassTest, nil, nil},
		{"npm test", ClassTest, nil, nil},
		{"npm run test -- --watch=false", ClassTest, nil, nil},
		{"pytest -q", ClassTest, nil, nil},
		{"python3 -m pytest tests", ClassTest, nil, nil},
		{"make test", ClassTest, nil, nil},
		{"draiver test unit", ClassTest, nil, nil},
		{"cargo test", ClassTest, nil, nil},
		{"timeout 60 go test ./x", ClassTest, nil, nil},
		{"CGO_ENABLED=0 go test ./x", ClassTest, nil, nil},
		{"go test ./... 2>&1 | tail -20", ClassTest, nil, nil},
		{"go test ./... > /tmp/out.txt", ClassTest, nil, []string{"/tmp/out.txt"}},
		// build
		{"go build ./...", ClassBuild, nil, nil},
		{"go vet ./internal/x", ClassBuild, nil, nil},
		{"make", ClassBuild, nil, nil},
		{"tsc --noEmit", ClassBuild, nil, nil},
		{"npm run build", ClassBuild, nil, nil},
		// vcs
		{"git status --short", ClassVCS, nil, nil},
		{"git commit -m 'x; y'", ClassVCS, nil, nil},
		{"gh pr view 1", ClassVCS, nil, nil},
		// edit
		{"sed -i 's/a/b/' x.go", ClassEdit, nil, []string{"x.go"}},
		{"sed -i 's/a(b)/c/; s/d/e/' internal/x.go", ClassEdit, nil, []string{"internal/x.go"}},
		{"echo hi > notes.txt", ClassEdit, nil, []string{"notes.txt"}},
		{"cat >> internal/o.go <<'EOF'\nfunc X() {}\nEOF", ClassEdit, nil, []string{"internal/o.go"}},
		{"cat > a.md <<EOF\nhello; world > there\nEOF\ngo build ./...", ClassBuild, nil, []string{"a.md"}},
		{"touch new.go", ClassEdit, nil, []string{"new.go"}},
		{"rm -f old.go", ClassEdit, nil, []string{"old.go"}},
		{"mv a.go b.go", ClassEdit, nil, []string{"a.go", "b.go"}},
		{"cp a.go b.go", ClassEdit, nil, []string{"b.go"}},
		{"python3 - <<'EOF'\np='internal/x.go'\ns=open(p).read()\nopen(p,'w').write(s)\nEOF", ClassEdit, nil, []string{"internal/x.go"}},
		{"python3 - <<'EOF'\nprint(open('a.json').read())\nEOF", ClassOther, nil, nil},
		// quoted text is not a redirect or a separator
		{"awk 'NR>1 {print}' data.csv", ClassRead, []string{"data.csv"}, nil},
		{"echo 'a > b'", ClassOther, nil, nil},
		{"echo x >/dev/null", ClassOther, nil, nil},
		// compound: highest priority class wins, reads and edits come from every segment
		{"cat a.go; grep -n x b.go", ClassSearch, []string{"a.go"}, nil},
		{"ls && cat f.go", ClassSearch, []string{"f.go"}, nil},
		// other
		{"echo hello", ClassOther, nil, nil},
		{"curl -s http://x", ClassOther, nil, nil},
		{"", ClassOther, nil, nil},
	}
	for _, c := range cases {
		got := ClassifyBash(c.cmd)
		if got.Class != c.class || !reflect.DeepEqual(got.Reads, c.reads) || !reflect.DeepEqual(got.Edits, c.edits) {
			t.Errorf("ClassifyBash(%q) = {%s reads=%q edits=%q}, want {%s reads=%q edits=%q}",
				c.cmd, got.Class, got.Reads, got.Edits, c.class, c.reads, c.edits)
		}
	}
}

func TestClassifyTools(t *testing.T) {
	in := func(v map[string]string) json.RawMessage { b, _ := json.Marshal(v); return b }
	cases := []struct {
		tool  string
		input json.RawMessage
		class Class
		reads []string
		edits []string
	}{
		{"Read", in(map[string]string{"file_path": "/w/a.go"}), ClassRead, []string{"/w/a.go"}, nil},
		{"Edit", in(map[string]string{"file_path": "/w/a.go"}), ClassEdit, nil, []string{"/w/a.go"}},
		{"MultiEdit", in(map[string]string{"file_path": "b.go"}), ClassEdit, nil, []string{"b.go"}},
		{"Write", in(map[string]string{"file_path": "c.go"}), ClassEdit, nil, []string{"c.go"}},
		{"NotebookEdit", in(map[string]string{"notebook_path": "n.ipynb"}), ClassEdit, nil, []string{"n.ipynb"}},
		{"Grep", in(map[string]string{"pattern": "x"}), ClassSearch, nil, nil},
		{"Glob", in(map[string]string{"pattern": "**/*.go"}), ClassSearch, nil, nil},
		{"Task", in(map[string]string{"prompt": "explore"}), ClassSubagent, nil, nil},
		{"Agent", in(map[string]string{"prompt": "explore"}), ClassSubagent, nil, nil},
		{"WebFetch", in(map[string]string{"url": "http://x"}), ClassOther, nil, nil},
		{"Bash", in(map[string]string{"command": "go test ./..."}), ClassTest, nil, nil},
	}
	for _, c := range cases {
		got := Classify(c.tool, c.input)
		if got.Class != c.class || !reflect.DeepEqual(got.Reads, c.reads) || !reflect.DeepEqual(got.Edits, c.edits) {
			t.Errorf("Classify(%s, %s) = %+v, want class %s reads %q edits %q", c.tool, c.input, got, c.class, c.reads, c.edits)
		}
	}
}

func TestSearchQueriesNormalize(t *testing.T) {
	a := ClassifyBash(`grep -rn "foo"  internal`)
	b := ClassifyBash(`grep -rn foo internal`)
	if a.Query == "" || a.Query != b.Query {
		t.Fatalf("near-identical searches should share a query key: %q vs %q", a.Query, b.Query)
	}
	g1 := Classify("Grep", json.RawMessage(`{"pattern":"Foo"}`))
	g2 := Classify("Grep", json.RawMessage(`{"pattern":"foo"}`))
	if g1.Query != g2.Query {
		t.Fatalf("case-only difference should repeat: %q vs %q", g1.Query, g2.Query)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		class  Class
		result string
		want   ErrKind
	}{
		{ClassOther, "Exit code 127\nbash: foo: command not found", ErrEnvDiscovery},
		{ClassTest, "Exit code 127\nbash: pytest: command not found", ErrEnvDiscovery},
		{ClassOther, "Error: unknown flag: --bogus", ErrEnvDiscovery},
		{ClassBuild, "make: *** No rule to make target 'lint'.  Stop.", ErrEnvDiscovery},
		{ClassOther, "npm ERR! Missing script: \"lint\"", ErrEnvDiscovery},
		{ClassOther, "fatal: could not read from remote repository", ErrEnvDiscovery},
		{ClassRead, "File does not exist.", ErrEnvDiscovery},
		{ClassTest, "--- FAIL: TestX (0.00s)\n    x_test.go:12: want 1 got 2\nFAIL", ErrTestFailure},
		{ClassTest, "--- FAIL: TestX\n x\n y\nopen /tmp/f: No such file or directory", ErrTestFailure},
		{ClassBuild, "./x.go:3:2: undefined: Foo", ErrBuildFailure},
		{ClassEdit, "<tool_use_error>String to replace not found in file.</tool_use_error>", ErrEditMechanics},
		{ClassEdit, "File has not been read yet. Read it first before writing to it.", ErrEditMechanics},
		{ClassOther, "Permission gate: Bash(rm -rf /) denied by policy", ErrPermission},
		{ClassOther, "Protocol gate: log a decision first", ErrPermission},
		{ClassOther, "something else broke", ErrOther},
	}
	for _, c := range cases {
		if got := ClassifyError(c.class, c.result); got != c.want {
			t.Errorf("ClassifyError(%s, %q) = %s, want %s", c.class, c.result, got, c.want)
		}
	}
}

func TestIsEmptySearch(t *testing.T) {
	for _, s := range []string{"", "No matches found", "No files found", "Exit code 1", "(no output)"} {
		if !IsEmptySearch(s) {
			t.Errorf("IsEmptySearch(%q) = false", s)
		}
	}
	if IsEmptySearch("a.go:1: foo") {
		t.Error("a hit is not an empty search")
	}
}

func TestIsDoc(t *testing.T) {
	for p, want := range map[string]bool{
		"AGENTS.md": true, "CLAUDE.md": true, "README.md": true, "sub/README": true,
		"docs/metrics.md": true, "pkg/docs/x.md": true,
		"main.go": false, "documentation.go": false, "changelog.d/x.md": false,
	} {
		if IsDoc(p) != want {
			t.Errorf("IsDoc(%q) != %v", p, want)
		}
	}
}

func TestNormalizeRemote(t *testing.T) {
	want := "github.com/owner/repo"
	for _, u := range []string{
		"https://github.com/owner/repo.git",
		"https://user:tok@GitHub.com/owner/repo",
		"git@github.com:owner/repo.git",
		"ssh://git@github.com/owner/repo.git/",
	} {
		if got := NormalizeRemote(u); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", u, got, want)
		}
	}
}
