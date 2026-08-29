// Syncs GitHub pull requests into Linear.
//
// Behaviour (see .github/scripts/linear-pr-sync/README.md for the full spec):
//   opened / reopened  -> create parent issue + "review needed" sub-issues,
//                         and request the reviewers on the GitHub PR itself
//   milestoned         -> move parent (and children) out of Triage into the release project
//   demilestoned       -> move parent (and children) back to the core project / Triage
//   closed (merged)    -> parent -> "Merged. Ready for testing", review sub-issues -> Done,
//                         and (if the parent is in a release project) a Q&A testing sub-issue
//                         is created (state "Todo", assigned to the QA owner)
//   closed (unmerged)  -> if closed by a reviewer: their sub-issue -> Done, parent -> Done,
//                         other sub-issue -> Cancelled
//                         otherwise: parent + review sub-issues -> Cancelled
//                         (no Q&A testing sub-issue is created)
//
// All identity/state resolution is done by NAME at runtime, so the only hard config
// is: the Linear team key and the reviewer mapping.
//
// Fail-soft: Linear syncing is a side-channel automation and must never block a
// pull request. Missing config (e.g. an expired/unset secret) or any runtime
// error is downgraded to a GitHub warning annotation and the process exits 0.

import fs from "node:fs";
import { LinearClient } from "@linear/sdk";

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Names of required env vars that were missing. We collect them instead of
// throwing at module-load time so the script can fail soft (see main()).
const missingEnv = [];

const cfg = {
  apiKey: requireEnv("LINEAR_API_KEY"),
  teamKey: requireEnv("LINEAR_TEAM_KEY"),
  // The "Gonka core" folder is the GON *team*, not a project. Leave empty so that
  // PRs without a milestone stay in the team's Triage with no project attached.
  // Set it only if you actually want a project assigned to un-milestoned PRs.
  coreProjectName: process.env.CORE_PROJECT_NAME || "",
  // GitHub milestone "v0.2.15" maps to Linear project "Upgrade v0.2.15".
  milestoneProjectPrefix:
    process.env.MILESTONE_PROJECT_PREFIX || "Upgrade ",
  // Bucket for PRs that were merged/approved WITHOUT a milestone, so they can be
  // sorted into the right release project manually. Created if missing.
  unsortedProjectName:
    process.env.UNSORTED_PROJECT_NAME || "Merged — no milestone (to sort)",
  reviewers: JSON.parse(process.env.REVIEWERS || "[]"),
  firstContributorLabel:
    process.env.FIRST_CONTRIBUTOR_LABEL || "first-time contributor",
  backlogStateName: process.env.BACKLOG_STATE_NAME || "Backlog",
  doneStateName: process.env.DONE_STATE_NAME || "Done",
  cancelledStateName: process.env.CANCELLED_STATE_NAME || "Canceled",
  // Review sub-issue state when the reviewer did NOT approve on GitHub.
  notDoneStateName: process.env.NOT_DONE_STATE_NAME || "Not done",
  releaseStartStateName: process.env.RELEASE_START_STATE_NAME || "Backlog",
  // On merge the parent moves to this state and a Q&A testing sub-issue is created.
  mergedStateName: process.env.MERGED_STATE_NAME || "Merged. Ready for testing",

  // Q&A testing sub-issue (created when a milestoned PR is merged).
  qaTeamKey: process.env.QA_TEAM_KEY || "QA",
  qaTodoStateName: process.env.QA_TODO_STATE_NAME || "Todo",
  testingSuffix: process.env.TESTING_SUFFIX || "Testing",
  qaAssigneeEmail:
    process.env.QA_ASSIGNEE_EMAIL || "maria.mitina@productscience.ai",
  qaSubscriberEmails: (
    process.env.QA_SUBSCRIBER_EMAILS ||
    "dbogdan@engenious.io,maria.mitina@productscience.ai"
  )
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean),

  // Auto-request the reviewers on the GitHub PR (set to "false" to disable).
  requestGithubReviewers:
    (process.env.REQUEST_GITHUB_REVIEWERS || "true").toLowerCase() !== "false",
};

const milestoneProjectName = (milestone) =>
  `${cfg.milestoneProjectPrefix}${milestone}`;

// Constructed only when the API key is present; when it's missing, main() bails
// early (fail-soft) before any client call is made.
const client = cfg.apiKey ? new LinearClient({ apiKey: cfg.apiKey }) : null;

// Marker embedded in the parent issue description. Also used as an attachment URL,
// which is how we reliably find the issue again on later events.
const prMarker = (repo, number) => `github-pr:${repo}#${number}`;

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

async function main() {
  // Fail-soft: if the Linear credentials/config are missing (e.g. an expired or
  // unset secret), warn and exit cleanly instead of blocking the PR check.
  if (missingEnv.length) {
    ghWarn(
      `Linear sync skipped — missing config: ${missingEnv.join(", ")}. ` +
        `Set the repository secrets/variables to re-enable Linear syncing.`,
    );
    return;
  }

  const pr = await loadPullRequest();
  if (!pr) return;

  const eventName = process.env.GITHUB_EVENT_NAME || "";
  const action = process.env.GITHUB_EVENT_ACTION || "opened";
  console.log(
    `Event: ${eventName || "pull_request"}/${action} for PR ${pr.repo}#${pr.number} ("${pr.title}")`,
  );

  if (eventName === "pull_request_review") {
    await onReviewSubmitted(pr);
    return;
  }

  switch (action) {
    case "opened":
    case "reopened":
      await onOpenedOrReopened(pr);
      break;
    case "milestoned":
      await onMilestoned(pr);
      break;
    case "demilestoned":
      await onDemilestoned(pr);
      break;
    case "closed":
      await onClosed(pr);
      break;
    default:
      console.log(`No handler for action "${action}", nothing to do.`);
  }
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

async function onOpenedOrReopened(pr) {
  if (cfg.requestGithubReviewers) await requestGithubReviewers(pr);

  const team = await getTeam();
  const states = await getStates(team);

  const existing = await findParentIssue(pr);

  // With a milestone -> the release project. Without -> optional core project (usually
  // none: the issue just lives in the GON team's Triage).
  const project = pr.milestone
    ? await getOrCreateProject(milestoneProjectName(pr.milestone), team.id)
    : await findCoreProject(team.id);
  const projectId = project ? project.id : null;

  // No milestone -> Triage. With milestone -> the release project's start state.
  const parentState = pr.milestone
    ? states.releaseStart
    : states.triage || states.backlog;

  if (existing) {
    // Reopened / re-synced: revive the issue rather than creating a duplicate.
    await client.updateIssue(existing.id, {
      stateId: parentState.id,
      projectId,
    });
    const children = await getChildren(existing);
    for (const child of children) {
      await client.updateIssue(child.id, {
        stateId: states.backlog.id,
        projectId,
      });
    }
    console.log(`Revived existing parent issue ${existing.identifier}.`);
    return;
  }

  const labelIds = [];
  if (pr.firstTimeContributor) {
    const label = await getOrCreateLabel(cfg.firstContributorLabel, team.id);
    labelIds.push(label.id);
  }

  const parentRes = await client.createIssue({
    teamId: team.id,
    projectId,
    stateId: parentState.id,
    title: pr.title,
    description: parentDescription(pr),
    labelIds,
  });
  const parent = await parentRes.issue;
  console.log(`Created parent issue ${parent.identifier}.`);

  // Attach the PR URL so we can look this issue up again later.
  await client.createAttachment({
    issueId: parent.id,
    url: pr.url,
    title: `GitHub PR #${pr.number}`,
  });

  const reviewers = reviewersFor(pr.authorLogin);
  for (const reviewer of reviewers) {
    const user = await getUserByEmail(reviewer.email);
    await client.createIssue({
      teamId: team.id,
      projectId,
      parentId: parent.id,
      stateId: states.backlog.id,
      title: `${pr.title} — review needed`,
      description: `Review needed for ${pr.url}\n\n${prMarker(pr.repo, pr.number)}`,
      assigneeId: user ? user.id : undefined,
    });
    console.log(
      `Created review sub-issue for ${reviewer.name}${user ? "" : " (unassigned: Linear user not found)"}.`,
    );
  }
}

async function onMilestoned(pr) {
  if (!pr.milestone) return;
  const parent = await findParentIssue(pr);
  if (!parent) return warnNoParent(pr);

  const team = await getTeam();
  const states = await getStates(team);
  const project = await getOrCreateProject(
    milestoneProjectName(pr.milestone),
    team.id,
  );

  await client.updateIssue(parent.id, {
    projectId: project.id,
    stateId: states.releaseStart.id,
  });
  for (const child of await getChildren(parent)) {
    await client.updateIssue(child.id, { projectId: project.id });
  }
  console.log(
    `Moved ${parent.identifier} to release project "${pr.milestone}".`,
  );
}

async function onDemilestoned(pr) {
  const parent = await findParentIssue(pr);
  if (!parent) return warnNoParent(pr);

  const team = await getTeam();
  const states = await getStates(team);
  const project = await findCoreProject(team.id);
  const projectId = project ? project.id : null;
  const backToState = states.triage || states.backlog;

  await client.updateIssue(parent.id, {
    projectId,
    stateId: backToState.id,
  });
  for (const child of await getChildren(parent)) {
    await client.updateIssue(child.id, { projectId });
  }
  console.log(`Moved ${parent.identifier} back to Triage.`);
}

async function onClosed(pr) {
  const parent = await findParentIssue(pr);
  if (!parent) return warnNoParent(pr);

  const team = await getTeam();
  const states = await getStates(team);
  const children = await getChildren(parent);

  if (pr.merged) {
    // Ensure the parent lives in a project. If it was merged without a milestone,
    // drop it into the "unsorted" bucket so it can be triaged into a release later.
    const projectId = await ensureParentProject(pr, parent, children, team, {
      logLabel: "merged",
    });

    await client.updateIssue(parent.id, { stateId: states.merged.id });

    // A review sub-issue is Done only if that reviewer actually approved on GitHub;
    // otherwise it becomes "Not done". Leave any Q&A child alone.
    const qaTeam = await getTeamByKey(cfg.qaTeamKey);
    const approved = await fetchApprovedReviewerLogins(pr);
    const reviewerByUser = await reviewerByUserId();
    for (const child of children) {
      if ((await childTeamId(child)) === qaTeam.id) continue;
      const assigneeId = await childAssigneeId(child);
      const reviewer = assigneeId ? reviewerByUser.get(assigneeId) : null;
      const didApprove =
        reviewer && approved.has(reviewer.github.toLowerCase());
      await client.updateIssue(child.id, {
        stateId: didApprove ? states.done.id : states.notDone.id,
      });
    }

    await maybeCreateQaTestingIssue(pr, parent, children, qaTeam, projectId);
    console.log(
      `PR merged -> ${parent.identifier} set to "${cfg.mergedStateName}"; review sub-issues resolved by approval.`,
    );
    return;
  }

  const closerReviewer = reviewerByLogin(pr.closerLogin);
  if (closerReviewer) {
    // Closed by one of the reviewers -> treat as an accepted/handled close.
    await client.updateIssue(parent.id, { stateId: states.done.id });
    const closerUser = await getUserByEmail(closerReviewer.email);
    for (const child of children) {
      const assigneeId = await childAssigneeId(child);
      const isClosers = closerUser && assigneeId === closerUser.id;
      await client.updateIssue(child.id, {
        stateId: isClosers ? states.done.id : states.cancelled.id,
      });
    }
    console.log(
      `PR closed by reviewer ${closerReviewer.name} -> parent Done, their sub-issue Done, others Cancelled.`,
    );
    return;
  }

  // Closed by someone who is not a reviewer (no merge): cancel the parent and the
  // review sub-issues. No Q&A testing sub-issue is created (that only happens on merge).
  await client.updateIssue(parent.id, { stateId: states.cancelled.id });
  for (const child of children) {
    await client.updateIssue(child.id, { stateId: states.cancelled.id });
  }
  console.log(
    `PR closed by ${pr.closerLogin || "unknown"} -> ${parent.identifier} and review sub-issues Cancelled.`,
  );
}

// Approval from a reviewer: mark their review sub-issue Done. If the PR has no
// milestone yet, park the parent in the "unsorted" project so we can see approved
// work that still needs to be assigned to a release.
async function onReviewSubmitted(pr) {
  if (!pr.review || (pr.review.state || "").toLowerCase() !== "approved") {
    console.log("Review is not an approval; nothing to do.");
    return;
  }
  const reviewer = reviewerByLogin(pr.review.login);
  if (!reviewer) {
    console.log(`Approval by non-reviewer ${pr.review.login}; ignoring.`);
    return;
  }

  const parent = await findParentIssue(pr);
  if (!parent) return warnNoParent(pr);

  const team = await getTeam();
  const states = await getStates(team);
  const children = await getChildren(parent);

  const user = await getUserByEmail(reviewer.email);
  if (user) {
    for (const child of children) {
      if ((await childAssigneeId(child)) === user.id) {
        await client.updateIssue(child.id, { stateId: states.done.id });
        console.log(`Approved by ${reviewer.name} -> their review sub-issue Done.`);
      }
    }
  }

  if (!pr.milestone) {
    const current = await parent.project;
    if (!current) {
      const project = await getOrCreateProject(
        cfg.unsortedProjectName,
        team.id,
      );
      await client.updateIssue(parent.id, {
        projectId: project.id,
        stateId: states.releaseStart.id,
      });
      for (const child of children) {
        await client.updateIssue(child.id, { projectId: project.id });
      }
      console.log(
        `Approved without milestone -> moved ${parent.identifier} to "${cfg.unsortedProjectName}".`,
      );
    }
  }
}

// ---------------------------------------------------------------------------
// Reviewer logic
// ---------------------------------------------------------------------------

// If the PR author is one of the reviewers, only the *other* reviewer(s) review it.
function reviewersFor(authorLogin) {
  const isAuthor = (r) =>
    r.github && authorLogin &&
    r.github.toLowerCase() === authorLogin.toLowerCase();
  const withoutAuthor = cfg.reviewers.filter((r) => !isAuthor(r));
  return withoutAuthor.length ? withoutAuthor : cfg.reviewers;
}

function reviewerByLogin(login) {
  if (!login) return null;
  return (
    cfg.reviewers.find(
      (r) => r.github && r.github.toLowerCase() === login.toLowerCase(),
    ) || null
  );
}

// ---------------------------------------------------------------------------
// Linear resolvers (cached per run)
// ---------------------------------------------------------------------------

const _teams = new Map();
async function getTeamByKey(key) {
  if (_teams.has(key)) return _teams.get(key);
  const res = await client.teams({ filter: { key: { eq: key } } });
  if (!res.nodes.length) {
    throw new Error(`Linear team with key "${key}" not found.`);
  }
  _teams.set(key, res.nodes[0]);
  return res.nodes[0];
}

async function getTeam() {
  return getTeamByKey(cfg.teamKey);
}

const _teamStateNodes = new Map();
async function teamStateNodes(team) {
  if (_teamStateNodes.has(team.id)) return _teamStateNodes.get(team.id);
  const res = await team.states();
  _teamStateNodes.set(team.id, res.nodes);
  return res.nodes;
}

// Find a workflow state by preferred names, then by fallback types.
function pickState(nodes, names, types) {
  for (const name of names) {
    const s = nodes.find((x) => x.name.toLowerCase() === name.toLowerCase());
    if (s) return s;
  }
  for (const type of types) {
    const s = nodes.find((x) => x.type === type);
    if (s) return s;
  }
  return null;
}

let _states;
async function getStates(team) {
  if (_states) return _states;
  const nodes = await teamStateNodes(team);

  _states = {
    triage: pickState(nodes, [], ["triage"]),
    backlog: pickState(nodes, [cfg.backlogStateName], ["backlog"]),
    done: pickState(nodes, [cfg.doneStateName], ["completed"]),
    cancelled: pickState(
      nodes,
      [cfg.cancelledStateName, "canceled", "cancelled"],
      ["canceled"],
    ),
    notDone: pickState(nodes, [cfg.notDoneStateName], ["canceled"]),
    releaseStart: pickState(
      nodes,
      [cfg.releaseStartStateName],
      ["backlog", "unstarted"],
    ),
    merged: pickState(nodes, [cfg.mergedStateName], ["started"]),
  };

  for (const [k, v] of Object.entries(_states)) {
    if (!v && k !== "triage") {
      throw new Error(
        `Could not resolve Linear workflow state for "${k}". Check the *_STATE_NAME variables.`,
      );
    }
  }
  return _states;
}

// Resolve the "Todo" state in the Q&A team for new testing sub-issues.
async function getQaTodoState() {
  const qaTeam = await getTeamByKey(cfg.qaTeamKey);
  const nodes = await teamStateNodes(qaTeam);
  const state = pickState(
    nodes,
    [cfg.qaTodoStateName, "To do", "Todo"],
    ["unstarted", "backlog"],
  );
  if (!state) {
    throw new Error(`Could not resolve QA "Todo" state (team ${cfg.qaTeamKey}).`);
  }
  return { qaTeam, state };
}

const _projects = new Map();
async function getOrCreateProject(name, teamId) {
  if (_projects.has(name)) return _projects.get(name);
  const res = await client.projects({ filter: { name: { eq: name } } });
  let project = res.nodes[0];
  if (!project) {
    const created = await client.createProject({ name, teamIds: [teamId] });
    project = await created.project;
    console.log(`Created Linear project "${name}".`);
  }
  _projects.set(name, project);
  return project;
}

// Optional project for un-milestoned PRs. Returns null when unconfigured or missing,
// in which case the parent simply lives in the team's Triage with no project.
async function findCoreProject(teamId) {
  if (!cfg.coreProjectName) return null;
  return await getOrCreateProject(cfg.coreProjectName, teamId);
}

const _users = new Map();
async function getUserByEmail(email) {
  if (!email) return null;
  if (_users.has(email)) return _users.get(email);
  const res = await client.users({ filter: { email: { eq: email } } });
  const user = res.nodes[0] || null;
  _users.set(email, user);
  return user;
}

async function getOrCreateLabel(name, teamId) {
  const res = await client.issueLabels({ filter: { name: { eq: name } } });
  const existing = res.nodes.find((l) => l.name.toLowerCase() === name.toLowerCase());
  if (existing) return existing;
  const created = await client.createIssueLabel({ name, teamId });
  console.log(`Created Linear label "${name}".`);
  return await created.issueLabel;
}

// Find the parent issue previously created for this PR, via its attachment URL.
async function findParentIssue(pr) {
  let attachments;
  try {
    attachments = await client.attachmentsForURL(pr.url);
  } catch (e) {
    console.log(`attachmentsForURL failed: ${e.message}`);
    return null;
  }
  for (const att of attachments.nodes) {
    const issue = await att.issue;
    if (!issue) continue;
    const parentRef = await issue.parent;
    if (!parentRef) return issue; // parent issue has no parent of its own
  }
  return null;
}

async function getChildren(issue) {
  const res = await issue.children();
  return res.nodes;
}

async function childAssigneeId(child) {
  const assignee = await child.assignee;
  return assignee ? assignee.id : null;
}

async function childTeamId(child) {
  const team = await child.team;
  return team ? team.id : null;
}

// Returns the parent's project id, moving it (and children) into the "unsorted"
// bucket first if it has none (e.g. merged without a milestone).
async function ensureParentProject(pr, parent, children, team, { logLabel }) {
  const current = await parent.project;
  if (current) return current.id;
  const project = await getOrCreateProject(cfg.unsortedProjectName, team.id);
  await client.updateIssue(parent.id, { projectId: project.id });
  for (const child of children) {
    await client.updateIssue(child.id, { projectId: project.id });
  }
  console.log(
    `No milestone on ${logLabel} -> moved ${parent.identifier} to "${cfg.unsortedProjectName}".`,
  );
  return project.id;
}

// Map Linear user id -> reviewer config, so we can tell which review sub-issue
// belongs to which reviewer by its assignee.
async function reviewerByUserId() {
  const map = new Map();
  for (const r of cfg.reviewers) {
    const user = await getUserByEmail(r.email);
    if (user) map.set(user.id, r);
  }
  return map;
}

// Set of lowercased GitHub logins whose *latest* review on the PR is an approval.
async function fetchApprovedReviewerLogins(pr) {
  const approved = new Set();
  const token = process.env.GH_TOKEN;
  if (!token) {
    console.log("No GH_TOKEN; cannot read PR reviews (treating as no approvals).");
    return approved;
  }
  const res = await fetch(
    `https://api.github.com/repos/${pr.repo}/pulls/${pr.number}/reviews?per_page=100`,
    {
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: "application/vnd.github+json",
      },
    },
  );
  if (!res.ok) {
    console.log(`Could not fetch PR reviews (${res.status}).`);
    return approved;
  }
  const reviews = await res.json();
  const latest = new Map();
  for (const rv of reviews) {
    const login = rv.user?.login?.toLowerCase();
    if (!login) continue;
    const state = (rv.state || "").toUpperCase();
    if (state === "COMMENTED") continue; // comment-only reviews don't change approval
    latest.set(login, state); // reviews are chronological; keep the last
  }
  for (const [login, state] of latest) {
    if (state === "APPROVED") approved.add(login);
  }
  return approved;
}

// When a milestoned PR is merged, hand it off to Q&A: create a "<title> — Testing"
// sub-issue in the QA team (state Todo), assigned to the QA owner, with the reviewers
// subscribed so they get notified.
async function maybeCreateQaTestingIssue(pr, parent, children, qaTeam, projectId) {
  if (!projectId) {
    console.log("Parent has no project; skipping Q&A testing sub-issue.");
    return;
  }

  // Idempotency: don't create a second QA sub-issue on a duplicate merge event.
  for (const child of children) {
    if ((await childTeamId(child)) === qaTeam.id) {
      console.log("Q&A testing sub-issue already exists; skipping.");
      return;
    }
  }

  const { state: todo } = await getQaTodoState();
  const assignee = await getUserByEmail(cfg.qaAssigneeEmail);

  const subscriberIds = [];
  for (const email of cfg.qaSubscriberEmails) {
    const user = await getUserByEmail(email);
    if (user) subscriberIds.push(user.id);
  }

  await client.createIssue({
    teamId: qaTeam.id,
    parentId: parent.id,
    projectId,
    stateId: todo.id,
    title: `${pr.title} — ${cfg.testingSuffix}`,
    assigneeId: assignee ? assignee.id : undefined,
    subscriberIds: subscriberIds.length ? subscriberIds : undefined,
    description: qaTestingDescription(pr),
  });
  console.log(
    `Created Q&A testing sub-issue (assignee ${cfg.qaAssigneeEmail}${assignee ? "" : " — not found, unassigned"}).`,
  );
}

function qaTestingDescription(pr) {
  const cc = cfg.qaSubscriberEmails.map((e) => `@${e}`).join(" ");
  return [
    `The pull request linked to the parent issue (${pr.url}) has been merged and is ready for testing.`,
    "",
    `cc: ${cc}`,
  ].join("\n");
}

// Request the configured reviewers on the GitHub PR itself. Uses the Actions token,
// which has write access to the base repo even for fork PRs (pull_request_target).
async function requestGithubReviewers(pr) {
  const logins = reviewersFor(pr.authorLogin)
    .map((r) => r.github)
    .filter(Boolean);
  if (!logins.length) return;

  const token = process.env.GH_TOKEN;
  if (!token) {
    console.log("No GH_TOKEN available; skipping GitHub reviewer request.");
    return;
  }

  const res = await fetch(
    `https://api.github.com/repos/${pr.repo}/pulls/${pr.number}/requested_reviewers`,
    {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: "application/vnd.github+json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ reviewers: logins }),
    },
  );
  if (!res.ok) {
    // 422 typically means a login isn't a collaborator or is the PR author.
    console.log(
      `GitHub reviewer request returned ${res.status}: ${await res.text()}`,
    );
  } else {
    console.log(`Requested GitHub reviewers: ${logins.join(", ")}`);
  }
}

// ---------------------------------------------------------------------------
// GitHub event parsing
// ---------------------------------------------------------------------------

async function loadPullRequest() {
  // Manual re-run path: fetch PR details via the GitHub API.
  if (process.env.MANUAL_PR_NUMBER && !process.env.GITHUB_EVENT_ACTION) {
    return await loadPullRequestFromApi(
      process.env.MANUAL_REPO,
      process.env.MANUAL_PR_NUMBER,
    );
  }

  const eventPath = process.env.GITHUB_EVENT_PATH;
  if (!eventPath || !fs.existsSync(eventPath)) {
    throw new Error("GITHUB_EVENT_PATH not available.");
  }
  const event = JSON.parse(fs.readFileSync(eventPath, "utf8"));
  const pull = event.pull_request;
  if (!pull) {
    console.log("Event has no pull_request payload, skipping.");
    return null;
  }

  const merged = Boolean(pull.merged);
  const closerLogin = merged
    ? pull.merged_by?.login
    : event.sender?.login;

  return {
    repo: event.repository?.full_name,
    number: pull.number,
    title: pull.title,
    url: pull.html_url,
    authorLogin: pull.user?.login,
    milestone: pull.milestone?.title || null,
    merged,
    closerLogin,
    firstTimeContributor:
      pull.author_association === "FIRST_TIME_CONTRIBUTOR" ||
      pull.author_association === "FIRST_TIMER",
    review: event.review
      ? { state: event.review.state, login: event.review.user?.login }
      : null,
  };
}

async function loadPullRequestFromApi(repo, number) {
  const token = process.env.GH_TOKEN;
  const res = await fetch(
    `https://api.github.com/repos/${repo}/pulls/${number}`,
    {
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: "application/vnd.github+json",
      },
    },
  );
  if (!res.ok) throw new Error(`GitHub API ${res.status}: ${await res.text()}`);
  const pull = await res.json();
  return {
    repo,
    number: pull.number,
    title: pull.title,
    url: pull.html_url,
    authorLogin: pull.user?.login,
    milestone: pull.milestone?.title || null,
    merged: Boolean(pull.merged),
    closerLogin: pull.merged_by?.login,
    firstTimeContributor:
      pull.author_association === "FIRST_TIME_CONTRIBUTOR" ||
      pull.author_association === "FIRST_TIMER",
  };
}

function parentDescription(pr) {
  return [
    `GitHub pull request: ${pr.url}`,
    `Author: @${pr.authorLogin}`,
    pr.firstTimeContributor ? "First-time contributor 🎉" : "",
    "",
    prMarker(pr.repo, pr.number),
  ]
    .filter(Boolean)
    .join("\n");
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function requireEnv(name) {
  const v = process.env[name];
  if (!v) {
    missingEnv.push(name);
    return "";
  }
  return v;
}

// Emit a GitHub Actions warning annotation (visible in the checks UI) without
// failing the job.
function ghWarn(message) {
  const oneLine = String(message).replace(/\r?\n/g, " ");
  console.log(`::warning title=linear-pr-sync::${oneLine}`);
}

function warnNoParent(pr) {
  console.log(
    `No Linear parent issue found for ${pr.repo}#${pr.number}; nothing to update.`,
  );
}

main().catch((err) => {
  // Fail-soft: Linear syncing is a side-channel automation and must never block
  // a pull request. Log the full error for debugging, surface a warning
  // annotation, and exit 0 so the check stays green.
  console.error(err);
  ghWarn(
    `Linear sync failed (non-blocking): ${err && err.message ? err.message : err}`,
  );
  process.exit(0);
});
