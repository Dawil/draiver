#!/usr/bin/env python3
"""Regenerate widget.stream.jsonl, the drv-054 hand-built transcript fixture.

It is shaped exactly like Claude Code's recorded stream-json (session/stream.jsonl):
a system/init frame, assistant lines carrying tool_use blocks and per-message
usage, user lines carrying tool_result blocks, and a closing result frame. Every
call is chosen so the right metric values are obvious; features/metrics/README.md
tabulates them. Run from the repo root:  python3 features/metrics/fixtures/gen_widget.py
"""
import json, os
from datetime import datetime, timedelta, timezone

CWD = "/fixture/worktree"
SID = "fixture-session-widget"
MODEL = "claude-fixture-1"
T0 = datetime(2026, 11, 1, 10, 0, 0, tzinfo=timezone.utc)


def ts(sec):
    return (T0 + timedelta(seconds=sec)).strftime("%Y-%m-%dT%H:%M:%S.000Z")


# (tool, input, is_error, result)
CALLS = [
    ("Read", {"file_path": CWD + "/AGENTS.md"}, False, "# Agents\nRun `go test ./...`."),          # 0 read, doc
    ("Grep", {"pattern": "Widget"}, False, "No matches found"),                                    # 1 search, empty
    ("Bash", {"command": "grep -rn widget src"}, True, "Exit code 1"),                             # 2 search, empty (not an error)
    ("Grep", {"pattern": "widget"}, False, "src/widget.go:3:func widget() int {"),                 # 3 search, repeat of 1
    ("Bash", {"command": "cat src/widget.go"}, False, "package widget\n\nfunc widget() int {"),   # 4 read
    ("Read", {"file_path": CWD + "/src/unused.go"}, False, "package widget // unused"),            # 5 read, never used
    ("Task", {"description": "explore", "prompt": "Find where widgets are configured"}, False, "They are not configured anywhere."),  # 6 subagent
    ("Bash", {"command": "make lint"}, True, "make: *** No rule to make target 'lint'.  Stop."),   # 7 build, env-discovery
    ("Bash", {"command": "widgetctl --help"}, True, "Exit code 127\nbash: line 1: widgetctl: command not found"),  # 8 env-discovery
    ("Edit", {"file_path": CWD + "/src/widget.go", "old_string": "func Widgit()", "new_string": "x"}, True,
     "<tool_use_error>String to replace not found in file.\nString: func Widgit()</tool_use_error>"),  # 9 edit-mechanics, no effect
    ("Write", {"file_path": CWD + "/src/scratch.go", "content": "package widget\n"}, False, "File created successfully"),  # 10 first edit (not in diff)
    ("Edit", {"file_path": CWD + "/src/widget.go", "old_string": "return 1", "new_string": "return 2"}, False, "The file has been updated."),  # 11 first PRODUCTIVE edit
    ("Bash", {"command": "go build ./..."}, False, ""),                                            # 12 build ok (try 2)
    ("Bash", {"command": "go test ./..."}, True, "--- FAIL: TestWidget (0.00s)\n    widget_test.go:9: got 1 want 2\nFAIL\nFAIL\texample.com/widget\t0.002s"),  # 13 test failure
    ("Read", {"file_path": CWD + "/src/widget.go"}, False, "package widget"),                      # 14 reread
    ("Edit", {"file_path": CWD + "/src/widget.go", "old_string": "got", "new_string": "want"}, False, "The file has been updated."),  # 15 edit
    ("Bash", {"command": "rm src/scratch.go"}, False, ""),                                         # 16 reverts scratch.go
    ("Bash", {"command": "go test ./..."}, False, "ok  \texample.com/widget\t0.003s"),             # 17 first green test (try 2)
    ("Bash", {"command": "git push --force origin main"}, True, "Permission gate: Bash(git push --force origin main) denied by policy"),  # 18 permission
    ("Bash", {"command": "git add -A && git commit -m 'Fix widget'"}, False, "[draiver/drv-901/0001 abc1234] Fix widget"),  # 19 vcs
]


def usage(i):
    # Request i's prompt is 1000 fresh + 1000*i cached tokens; it writes 100.
    return {"input_tokens": 1000, "cache_read_input_tokens": 1000 * i,
            "cache_creation_input_tokens": 0, "output_tokens": 100}


def main():
    lines = [{"type": "system", "subtype": "init", "cwd": CWD, "session_id": SID, "model": MODEL,
              "tools": ["Task", "Bash", "Read", "Edit", "Write", "Grep"], "timestamp": ts(0)}]
    for i, (tool, inp, err, res) in enumerate(CALLS):
        mid = "msg_fixture_%02d" % i
        tid = "toolu_fixture_%02d" % i
        sec = 30 * (i + 1)
        base = {"type": "assistant", "parent_tool_use_id": None, "session_id": SID, "timestamp": ts(sec)}
        if i == 0:
            # A thinking block and a tool_use block of ONE message arrive as two lines
            # that both repeat the message's usage: it must be counted once.
            lines.append(dict(base, message={"model": MODEL, "id": mid, "type": "message", "role": "assistant",
                                             "content": [{"type": "thinking", "thinking": "Start from AGENTS.md."}],
                                             "usage": usage(i)}))
        lines.append(dict(base, message={"model": MODEL, "id": mid, "type": "message", "role": "assistant",
                                         "content": [{"type": "tool_use", "id": tid, "name": tool, "input": inp}],
                                         "usage": usage(i)}))
        lines.append({"type": "user", "parent_tool_use_id": None, "session_id": SID, "timestamp": ts(sec + 10),
                      "message": {"role": "user", "content": [
                          {"type": "tool_result", "tool_use_id": tid, "content": res, "is_error": err}]}})
    end = 30 * (len(CALLS) + 1)
    lines.append({"type": "assistant", "parent_tool_use_id": None, "session_id": SID, "timestamp": ts(end),
                  "message": {"model": MODEL, "id": "msg_fixture_end", "type": "message", "role": "assistant",
                              "content": [{"type": "text", "text": "Done: widget fixed and committed."}],
                              "usage": usage(len(CALLS))}})
    lines.append({"type": "result", "subtype": "success", "is_error": False, "session_id": SID,
                  "result": "Done", "total_cost_usd": 0.42, "timestamp": ts(end + 30),
                  "usage": {"input_tokens": 21000, "cache_read_input_tokens": 210000,
                            "cache_creation_input_tokens": 0, "output_tokens": 2100}})
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "widget.stream.jsonl")
    with open(out, "w") as f:
        for l in lines:
            f.write(json.dumps(l, separators=(",", ":")) + "\n")


if __name__ == "__main__":
    main()
