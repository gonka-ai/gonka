# Linear ↔ GitHub Issue sync

Mirrors incoming GitHub **issues** into Linear. This is the lighter sibling of the PR
sync (`.github/scripts/linear-pr-sync/`): issues have no review/merge/QA lifecycle, so
there are no reviewer sub-issues and no Q&A hand-off.

## What it does

| GitHub event | Result |
|---|---|
| Issue **opened** / **reopened** | Linear issue created (or revived) in the **GON** team **Triage** (or the release project if a milestone is already set). First-time contributor → label. If the author is **external**, a polite acknowledgment comment is posted and the issue is added to the triage board (Status **new**, only if empty). |
| Issue **milestoned** | Moves into the release project named after the milestone. |
| Issue **demilestoned** | Moves back to Triage. |
| Issue **closed as completed** | Linear issue → **Done**. |
| Issue **closed as not planned** | Linear issue → **Cancelled**. |
| Triage board **Status → Accepted** | Handled centrally by the scheduled **accept-reconcile** job (in `.github/scripts/linear-pr-sync`), which reconciles both PRs and issues out of Triage into the release/backlog project. Not handled here. |

Internal comments in Linear stay in Linear. The only thing pushed to GitHub is the
one acknowledgment comment on external issues.

## Setup

Reuses the same secrets/variables as the PR sync:

- Secret `LINEAR_API_KEY`, secret `PROJECTS_TOKEN` (for the triage board; optional).
- Variables: `LINEAR_TEAM_KEY` (required), plus the optional `LINEAR_*` and
  `TRIAGE_PROJECT_NUMBER` variables (defaults match this workspace).
- `ISSUE_POST_ACK` (default `true`) — set `false` to disable the acknowledgment comment.
- `EXTERNAL_ISSUE_ACK_MESSAGE` — override the acknowledgment wording.

Workflow: `.github/workflows/linear-issue-sync.yml`.

## Notes

- **Fail-soft:** missing config or any runtime error becomes a GitHub warning
  annotation and the job exits 0 (green). The `integration-healthcheck` workflow
  monitors the tokens so silent failures get surfaced.
- **Accept signal:** acceptance is a maintainer moving the triage-board card to **Accepted**;
  the scheduled `accept-reconcile` job (in `.github/scripts/linear-pr-sync`) reconciles it into
  Linear. Projects v2 board changes can't trigger a workflow, so this issue sync doesn't handle
  acceptance directly.
- Uses the issue's node id for the triage board and stores the GitHub issue URL as a
  Linear attachment to find the issue again on later events.
