# Linear ↔ GitHub PR sync

Mirrors every incoming pull request (including PRs from external forks) into Linear
and keeps the Linear issues in sync with the PR lifecycle.

This is a **custom** integration on top of the Linear API, not the native Linear GitHub
integration. The native integration cannot auto-create an issue per external PR, detect
first-time contributors, build the parent/sub-issue structure, or apply the reviewer/close
logic below — hence this Action.

## What it does

| GitHub event | Result in Linear |
|---|---|
| PR **opened** / **reopened** (no milestone) | Parent issue created in the **GON** team **Triage** (no project). First-time contributor → label added. One **"`<title>` — review needed"** sub-issue (state **Backlog**) per reviewer. The reviewers are also **requested on the GitHub PR** itself. |
| PR **opened** with a milestone already set | Same, but parent + sub-issues go straight into the release project (named after the milestone), not Triage. |
| PR **milestoned** (release milestone added) | Parent (and its sub-issues) move out of Triage into the project named after the milestone (created if missing). |
| PR **labeled `accepted`** by a maintainer | Parent (and sub-issues) move out of Triage into the release project (if a milestone is set) or the **Backlog for future mainnet upgrades** project (parent state **Backlog**) otherwise. This is how maintainer *acceptance* is cross-synced — GitHub Projects v2 board changes can't trigger a workflow, so we key off the label. The companion triage workflow sets the board **Status = accepted** on the same event. |
| PR **demilestoned** | Parent moves back to the GON team / Triage. |
| PR **review submitted = approved** by a reviewer | That reviewer's review sub-issue → **Done**. If the PR has no milestone, the parent is parked in the **unsorted** project so approved-but-unassigned work is visible. |
| PR **merged** | Parent → **Merged. Ready for testing**. Each review sub-issue → **Done** *only if that reviewer actually approved on GitHub*, otherwise → **Not done**. A **"`<title>` — Testing"** sub-issue is created in the **Q&A** team (state **Todo**), assigned to the QA owner, reviewers subscribed. If there was no milestone, the parent is first moved into the **unsorted** project (full pipeline still runs). |
| PR **closed** by a reviewer (Dima / Gabriel) without merge | Parent → **Done**, that reviewer's sub-issue → **Done**, the other sub-issue → **Cancelled**. |
| PR **closed** by anyone else without merge | Parent + review sub-issues → **Cancelled**. No Q&A testing sub-issue is created. |

**Reviewer assignment rule:** normally a sub-issue is created for (and a GitHub review is
requested from) each reviewer. If the PR author *is* one of the reviewers, only the *other*
reviewer(s) apply (e.g. a PR by Dima is reviewed only by Gabriel, and vice versa).

**Private comments:** internal comments you write on the Linear issue stay in Linear. Nothing
is pushed to GitHub unless explicitly added to this script, so your triage discussion is not
visible to external contributors.

## Setup

### 1. Create a Linear API key
Linear → Settings → API → Personal API keys (or an OAuth app token for a service account).
Add it as a **repository secret** named `LINEAR_API_KEY`.

> Prefer a dedicated service-account user in Linear so issues/comments aren't attributed to a person.

### 2. Add repository Variables
Settings → Secrets and variables → Actions → **Variables**:

| Variable | Required | Value for this workspace | Notes |
|---|---|---|---|
| `LINEAR_TEAM_KEY` | ✅ | `GON` | Team "Gonka-core"; the key on issue ids like `GON-123`. |
| `LINEAR_REVIEWERS` | ✅ | see below | Maps GitHub logins to Linear users. |
| `LINEAR_MILESTONE_PROJECT_PREFIX` | – | `Upgrade ` | GitHub milestone `v0.2.15` → project `Upgrade v0.2.15`. Default already `Upgrade `. |
| `LINEAR_UNSORTED_PROJECT_NAME` | – | `Merged — no milestone (to sort)` | Bucket for PRs merged/approved without a milestone. Created if missing. |
| `LINEAR_ACCEPTED_LABEL` | – | `accepted` | GitHub label a maintainer adds to mark a PR accepted (drives the accept cross-sync). |
| `LINEAR_BACKLOG_PROJECT_NAME` | – | `Backlog for future mainnet upgrades` | Project an *accepted* PR without a milestone moves into. Must already exist. |
| `LINEAR_ACCEPTED_STATE_NAME` | – | `Backlog` | Parent state when accepted without a milestone. |
| `LINEAR_CORE_PROJECT_NAME` | – | *(leave empty)* | The "Gonka core" folder is the GON team; un-milestoned PRs stay in Triage with no project. |
| `LINEAR_FIRST_CONTRIBUTOR_LABEL` | – | `first-time contributor` | Created if missing. (Existing alternatives: `contributor`, `Community-driven`.) |
| `LINEAR_BACKLOG_STATE_NAME` | – | `Backlog` | Default. |
| `LINEAR_DONE_STATE_NAME` | – | `Done` | Default. |
| `LINEAR_CANCELLED_STATE_NAME` | – | `Canceled` | Note the single-l spelling in this workspace. |
| `LINEAR_NOT_DONE_STATE_NAME` | – | `Not done` | Review sub-issue state when the reviewer never approved. |
| `LINEAR_RELEASE_START_STATE_NAME` | – | `Backlog` | State the parent gets when a milestone moves it into a release project. |
| `LINEAR_MERGED_STATE_NAME` | – | `Merged. Ready for testing` | State the parent gets on merge. |
| `LINEAR_QA_TEAM_KEY` | – | `QA` | Team that owns testing sub-issues. |
| `LINEAR_QA_TODO_STATE_NAME` | – | `Todo` | State for the QA testing sub-issue. |
| `LINEAR_TESTING_SUFFIX` | – | `Testing` | Title suffix: `<title> — Testing`. |
| `LINEAR_QA_ASSIGNEE_EMAIL` | – | `maria.mitina@productscience.ai` | Assignee of the QA testing sub-issue. |
| `LINEAR_QA_SUBSCRIBER_EMAILS` | – | `dbogdan@engenious.io,maria.mitina@productscience.ai` | Comma-separated; subscribed/mentioned on the QA sub-issue. |
| `LINEAR_REQUEST_GITHUB_REVIEWERS` | – | `true` | Set `false` to stop requesting reviewers on the PR. |

`LINEAR_REVIEWERS` is a JSON array (single line). `github` = GitHub login, `email` = the
person's Linear account email, `name` = display name for logs:

```json
[{"github":"DimaOrekhovPS","email":"dima.orekhov@productscience.ai","name":"Dima Orekhov"},{"github":"GLiberman","email":"gabriel@productscience.ai","name":"Gabriel Liberman"}]
```

### 3. Done
The workflow at `.github/workflows/linear-pr-sync.yml` runs automatically. To re-sync a single
PR manually, use the workflow's **Run workflow** button and pass the PR number.

## Notes & assumptions

- **Fail-soft:** Linear syncing is a side-channel automation and must never block a PR. If the
  `LINEAR_API_KEY` / config is missing (e.g. an expired secret) or any runtime error occurs, the
  script emits a GitHub **warning annotation** and exits 0 (green check). Because failures are
  silent by design, a scheduled monitoring workflow pings the Linear API and opens/refreshes a
  GitHub issue if the key stops working — see `.github/workflows/integration-healthcheck.yml`.
- **Accept cross-sync:** a maintainer accepts a PR by adding the `accepted` label. Only users with
  write access can add labels, so the label itself implies maintainer acceptance (external
  contributors can't add it). The board's Status field (Projects v2) can't trigger a workflow, so
  the label is the signal in both directions.
- Uses `pull_request_target` so secrets are available for fork PRs. It never checks out or runs
  PR code — only reads metadata and calls Linear. Do not add a checkout of the PR head.
- Workflow states, projects, users and labels are resolved **by name** at runtime, so no Linear
  IDs are hard-coded. Missing projects and the label are created automatically.
- The PR ↔ issue link is stored as a Linear **attachment** on the parent issue (the PR URL),
  which is how later events (milestone/close) find the right issue.
- On merge the parent moves to **Merged. Ready for testing**. Each review sub-issue is set to
  **Done** only when that reviewer's latest GitHub review is an *approval*; otherwise it becomes
  **Not done**. Approvals are read from the GitHub reviews API (comment-only reviews are ignored).
- If a PR is merged or approved **without a milestone**, it's assumed the milestone was simply
  forgotten: the parent is moved into the **unsorted** project (`LINEAR_UNSORTED_PROJECT_NAME`)
  so you can see everything that landed as merged/approved but isn't assigned to a release, and
  sort it manually. The rest of the pipeline (merged state, Q&A hand-off) still runs.
- Requesting a GitHub reviewer requires the person to be a repo collaborator; if not, GitHub
  returns 422 and the script logs it and continues (the Linear sub-issue is still created).
- The QA sub-issue subscribes `LINEAR_QA_SUBSCRIBER_EMAILS` (so they are notified) and lists
  them in the description.
