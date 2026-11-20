#!/bin/sh
# merged-repo.sh DIR — build the drv-054 base-commit inference fixture repo in DIR.
#
#   drv-801  main was merged INTO the branch, then the branch landed. Its tip is
#            "Merge remote-tracking branch 'forgejo/main' into draiver/drv-801/0001":
#            the subject names the branch, but it is NOT the landing merge.
#            Expected: base = main as merged in (tag m801-base), diff = only a801.txt.
#   drv-802  landed with --no-ff, then the branch was fast-forwarded onto the landing
#            merge (tip = "Merge pull request ... from draiver/drv-802/0001 into main").
#            Expected: base = main before landing (tag m802-base), diff = only a802.txt.
set -eu
D="$1"
g() { git -C "$D" -c user.email=f@f -c user.name=fixture -c commit.gpgsign=false "$@"; }
mkdir -p "$D"
git init -q -b main "$D"
echo base >"$D/base.txt"; g add -A; g commit -qm base

# drv-801: branch work, another ticket lands on main, main merged into the branch.
g checkout -q -b draiver/drv-801/0001
echo 801 >"$D/a801.txt"; g add -A; g commit -qm "drv-801 work"
g checkout -q main
echo other >"$D/other.txt"; g add -A; g commit -qm "drv-800 landed"
g tag m801-base
g checkout -q draiver/drv-801/0001
g merge -q --no-ff main -m "Merge remote-tracking branch 'forgejo/main' into draiver/drv-801/0001"
g checkout -q main
g merge -q --no-ff draiver/drv-801/0001 -m "Merge pull request 'drv-801' (#1) from draiver/drv-801/0001 into main"

# drv-802: plain branch, landed --no-ff, branch fast-forwarded onto the landing merge.
g tag m802-base
g checkout -q -b draiver/drv-802/0001
echo 802 >"$D/a802.txt"; g add -A; g commit -qm "drv-802 work"
g checkout -q main
g merge -q --no-ff draiver/drv-802/0001 -m "Merge pull request 'drv-802' (#2) from draiver/drv-802/0001 into main"
g branch -q -f draiver/drv-802/0001 main
