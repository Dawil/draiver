---
section: Added
---
**Agents are prompted to push and attach a review URL when raising review
(drvctl-026).** The onboarding skill's "Claim review" step (SKILL.md §5) and the
Loop line now teach the agent to push its attempt branch to a git remote and
claim review with a link the human can click — a `compare` URL by default
(`<base>/compare/<main>...<branch>`, derived from `git remote get-url` with a
trailing `.git` stripped), or a real PR via `--url`. Remote selection is
explicit: exactly one remote → use it; more than one → the new global-config key
`primary_remote` (a git remote name, sitting alongside `review_link_hosts` in
`~/.draiver/config.json`); ambiguous (several remotes with no primary, or a
primary absent from `git remote`) → escalate rather than guess. `draiver review`
is unchanged behaviourally — it still only appends and validates the link, never
pushing or opening a PR — but its `--help` now says so. `config.Config` gains an
optional `primary_remote` field (omitted → empty).
