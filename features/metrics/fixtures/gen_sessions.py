#!/usr/bin/env python3
"""Regenerate sessions.stream.jsonl, the drv-054 metrics-v2 transcript fixture.

Same wire shape as widget.stream.jsonl, but the stream is append-only across
restarts: two sessions (two SessionIDs), the first resumed once, so three
segments. Every per-request prompt size is chosen so the context figures,
compaction and per-file token attribution are obvious; features/metrics/README.md
tabulates them. Run from the repo root:
    python3 features/metrics/fixtures/gen_sessions.py
"""
import json, os
from datetime import datetime, timedelta, timezone

CWD = "/fixture/worktree"
MODEL = "claude-fixture-1"
T0 = datetime(2026, 11, 2, 10, 0, 0, tzinfo=timezone.utc)
SA, SB = "fixture-session-a", "fixture-session-b"

BIG = "\n".join("line %03d of big.go" % i for i in range(1, 201))

# Segments: (session id, start second, [(tool, input, is_error, result, prompt tokens)],
#            closing text prompt tokens)
SEGMENTS = [
    (SA, 0, [
        ("Read", {"file_path": CWD + "/AGENTS.md"}, False, "# Agents\nRun `go test ./...`.", 30000),        # 0 doc
        ("Bash", {"command": "sed -n '1,50p' src/big.go"}, False, BIG[:1000], 31000),                     # 1 first read of big.go (ranged)
        ("Bash", {"command": "sed -n '51,100p' src/big.go"}, False, BIG[1000:2000], 33000),              # 2 ranged read: new slice
        ("Bash", {"command": "sed -n '1,50p' src/big.go"}, False, BIG[:1000], 35000),                     # 3 same-range reread
        ("Read", {"file_path": CWD + "/src/steps_drv901.go"}, False, "package widget // drv-901 steps", 36000),  # 4 cross-ticket read
        ("Write", {"file_path": CWD + "/src/zz_drv905_focus_test.go", "content": "package widget\n"}, False,
         "File created successfully", 40000),                                                            # 5 first productive edit
        ("Bash", {"command": "cat > tmp/notes.txt <<'EOF'\nscratch\nEOF"}, False, "", 42000),             # 6 creates a transient file
        ("Bash", {"command": "cd /fixture/worktree && go test ./src/... 2>&1 | tail -5"}, False, "ok  \texample.com/widget\t0.003s", 60000),  # 7 green test; peak
    ], 20000),                                                                                           # closing text: compaction (60000 → 20000)
    (SA, 1000, [
        ("Bash", {"command": "rm tmp/notes.txt"}, False, "", 22000),                                       # 8 resume; scratch removed
        ("Bash", {"command": "draiver promote drv-905"}, False,
         'Error: unknown command "promote" for "draiver"\nRun \'draiver --help\' for usage.\nexit=1', 23000),  # 9 stale binary: env discovery
    ], 24000),
    (SB, 2000, [
        ("Read", {"file_path": CWD + "/src/big.go"}, False, BIG, 31000),                                   # 10 cross-session reread (whole)
        ("Edit", {"file_path": CWD + "/src/big.go", "old_string": "line 001", "new_string": "line one"}, False,
         "The file has been updated.", 35000),                                                           # 11 session B's first productive edit
        ("Bash", {"command": "go test ./src/..."}, False, "ok  \texample.com/widget\t0.003s", 36000),       # 12 green test
    ], 36500),
]


def ts(sec):
    return (T0 + timedelta(seconds=sec)).strftime("%Y-%m-%dT%H:%M:%S.000Z")


def usage(prompt):
    return {"input_tokens": prompt, "cache_read_input_tokens": 0,
            "cache_creation_input_tokens": 0, "output_tokens": 100}


def main():
    lines, n = [], 0
    for sid, start, calls, closing in SEGMENTS:
        lines.append({"type": "system", "subtype": "init", "cwd": CWD, "session_id": sid, "model": MODEL,
                      "tools": ["Bash", "Read", "Edit", "Write"], "timestamp": ts(start)})
        k = 0
        for tool, inp, err, res, prompt in calls:
            k += 1
            mid, tid = "msg_sessions_%02d" % n, "toolu_sessions_%02d" % n
            sec = start + 20 * k
            lines.append({"type": "assistant", "parent_tool_use_id": None, "session_id": sid, "timestamp": ts(sec),
                          "message": {"model": MODEL, "id": mid, "type": "message", "role": "assistant",
                                      "content": [{"type": "tool_use", "id": tid, "name": tool, "input": inp}],
                                      "usage": usage(prompt)}})
            lines.append({"type": "user", "parent_tool_use_id": None, "session_id": sid, "timestamp": ts(sec + 10),
                          "message": {"role": "user", "content": [
                              {"type": "tool_result", "tool_use_id": tid, "content": res, "is_error": err}]}})
            n += 1
        end = start + 20 * (k + 1)
        lines.append({"type": "assistant", "parent_tool_use_id": None, "session_id": sid, "timestamp": ts(end),
                      "message": {"model": MODEL, "id": "msg_sessions_end_%d" % start, "type": "message",
                                  "role": "assistant", "content": [{"type": "text", "text": "Segment done."}],
                                  "usage": usage(closing)}})
        lines.append({"type": "result", "subtype": "success", "is_error": False, "session_id": sid,
                      "result": "Done", "timestamp": ts(end + 10),
                      "usage": {"input_tokens": 1, "cache_read_input_tokens": 0,
                                "cache_creation_input_tokens": 0, "output_tokens": 1}})
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "sessions.stream.jsonl")
    with open(out, "w") as f:
        for l in lines:
            f.write(json.dumps(l, separators=(",", ":")) + "\n")


if __name__ == "__main__":
    main()
