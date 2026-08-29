// Syncs GitHub issues into Linear — a lighter mirror of the PR sync.
//
// Issues don't have review/merge/QA lifecycles, so this is intentionally simpler
// than the PR sync (no reviewer sub-issues, no Q&A hand-off).
//
// Behaviour:
//   opened / reopened  -> create/revive a Linear issue in Triage; first-time
//                         contributor -> label. If the author is external, also
//                         post a polite acknowledgment comment and add the issue
//                         to the GitHub triage board (Status=new, only if empty).
//   labeled (accepted) -> a maintainer accepted it: move out of Triage into the
//                         release project (if milestoned) or the "Backlog for
//                         future mainnet upgrades" project (state Backlog). The
//                         GitHub board Status is set to accepted.
//   milestoned         -> move into the release project named after the milestone.
//   demilestoned       -> move back to Triage.
//   closed             -> completed -> Done; not planned -> Cancelled.
//
// Fail-soft: this is a side-channel automation and must never block anything.
// Missing config or any runtime error becomes a GitHub warning annotation and the
// process exits 0.

import fs from "node:fs";
import { LinearClient } from "@linear/sdk";

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

const missingEnv = [];

const cfg = {
  apiKey: requireEnv("LINEAR_API_KEY"),
  teamKey: requireEnv("LINEAR_TEAM_KEY"),
  milestoneProjectPrefix: process.env.MILESTONE_PROJECT_PREFIX || "Upgrade ",
  // Accept cross-sync (a maintainer adds this label).
  acceptedLabel: process.env.ACCEPTED_LABEL || "accepted",
  backlogProjectName:
    process.env.BACKLOG_PROJECT_NAME || "Backlog for future mainnet upgrades",
  firstContributorLabel:
    process.env.FIRST_CONTRIBUTOR_LABEL || "first-time contributor",
  // States (resolved by name, with type fallbacks).
  backlogStateName: process.env.BACKLOG_STATE_NAME || "Backlog",
  acceptedStateName: process.env.ACCEPTED_STATE_NAME || "Backlog",
  releaseStartStateName: process.env.RELEASE_START_STATE_NAME || "Backlog",
  doneStateName: process.env.DONE_STATE_NAME || "Done",
  cancelledStateName: process.env.CANCELLED_STATE_NAME || "Canceled",

  // GitHub triage board (Projects v2) for external issues.
  triageProjectOwner: process.env.PROJECT_OWNER || "",
  triageProjectNumber: parseInt(process.env.PROJECT_NUMBER || "0", 10),
  triageStatusField: process.env.STATUS_FIELD_NAME || "Status",
  triageNewStatus: process.env.STATUS_VALUE || "new",
  triageAcceptedStatus: process.env.ACCEPTED_STATUS_VALUE || "accepted",

  // Post an acknowledgment comment on external issues.
  postAck: (process.env.POST_ACK || "true").toLowerCase() !== "false",
};

const milestoneProjectName = (milestone) =>
  `${cfg.milestoneProjectPrefix}${milestone}`;

const client = cfg.apiKey ? new LinearClient({ apiKey: cfg.apiKey }) : null;

const issueMarker = (repo, number) => `github-issue:${repo}#${number}`;
const ackMarker = "<!-- gonka:ack-external-issue -->";
const INTERNAL = new Set(["OWNER", "MEMBER", "COLLABORATOR"]);

function requireEnv(name) {
  const v = process.env[name];
  if (!v) {
    missingEnv.push(name);
    return "";
  }
  return v;
}

function ghWarn(message) {
  const oneLine = String(message).replace(/\r?\n/g, " ");
  console.log(`::warning title=linear-issue-sync::${oneLine}`);
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

async function main() {
  if (missingEnv.length) {
    ghWarn(
      `Linear issue sync skipped — missing config: ${missingEnv.join(", ")}. ` +
        `Set the repository secrets/variables to re-enable it.`,
    );
    return;
  }

  const issue = loadIssue();
  if (!issue) return;

  const action = process.env.GITHUB_EVENT_ACTION || "opened";
  console.log(
    `Event: issues/${action} for ${issue.repo}#${issue.number} ("${issue.title}")`,
  );

  switch (action) {
    case "opened":
    case "reopened":
      await onOpenedOrReopened(issue);
      break;
    case "labeled":
      await onLabeled(issue);
      break;
    case "milestoned":
      await onMilestoned(issue);
      break;
    case "demilestoned":
      await onDemilestoned(issue);
      break;
    case "closed":
      await onClosed(issue);
      break;
    default:
      console.log(`No handler for action "${action}", nothing to do.`);
  }
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

async function onOpenedOrReopened(issue) {
  const team = await getTeam();
  const states = await getStates(team);

  const project = issue.milestone
    ? await getOrCreateProject(milestoneProjectName(issue.milestone), team.id)
    : null;
  const projectId = project ? project.id : null;
  const parentState = issue.milestone
    ? states.releaseStart
    : states.triage || states.backlog;

  const existing = await findLinearIssue(issue);
  if (existing) {
    await client.updateIssue(existing.id, { stateId: parentState.id, projectId });
    console.log(`Revived existing Linear issue ${existing.identifier}.`);
  } else {
    const labelIds = [];
    if (issue.firstTimeContributor) {
      const label = await getOrCreateLabel(cfg.firstContributorLabel, team.id);
      labelIds.push(label.id);
    }
    const created = await client.createIssue({
      teamId: team.id,
      projectId,
      stateId: parentState.id,
      title: issue.title,
      description: linearDescription(issue),
      labelIds,
    });
    const linearIssue = await created.issue;
    await client.createAttachment({
      issueId: linearIssue.id,
      url: issue.url,
      title: `GitHub issue #${issue.number}`,
    });
    console.log(`Created Linear issue ${linearIssue.identifier}.`);
  }

  // External-only side effects: acknowledgment comment + triage board.
  if (!INTERNAL.has(issue.authorAssociation) && !issue.isBot) {
    if (cfg.postAck) await postAcknowledgment(issue);
    await addToTriageBoard(issue, cfg.triageNewStatus, { overwrite: false });
  }
}

async function onLabeled(issue) {
  if (
    !issue.labelName ||
    issue.labelName.toLowerCase() !== cfg.acceptedLabel.toLowerCase()
  ) {
    console.log(
      `Label "${issue.labelName || ""}" is not the accept label; nothing to do.`,
    );
    return;
  }

  const linear = await findLinearIssue(issue);
  if (linear) {
    const team = await getTeam();
    const states = await getStates(team);
    let project;
    let stateId;
    if (issue.milestone) {
      project = await getOrCreateProject(
        milestoneProjectName(issue.milestone),
        team.id,
      );
      stateId = states.releaseStart.id;
    } else {
      project = await getOrCreateProject(cfg.backlogProjectName, team.id);
      stateId = states.accepted.id;
    }
    await client.updateIssue(linear.id, { projectId: project.id, stateId });
    console.log(
      `Issue accepted (label "${issue.labelName}") -> moved ${linear.identifier} to "${project.name}".`,
    );
  } else {
    warnNoLinear(issue);
  }

  // Reflect acceptance on the GitHub board too.
  await addToTriageBoard(issue, cfg.triageAcceptedStatus, { overwrite: true });
}

async function onMilestoned(issue) {
  if (!issue.milestone) return;
  const linear = await findLinearIssue(issue);
  if (!linear) return warnNoLinear(issue);
  const team = await getTeam();
  const states = await getStates(team);
  const project = await getOrCreateProject(
    milestoneProjectName(issue.milestone),
    team.id,
  );
  await client.updateIssue(linear.id, {
    projectId: project.id,
    stateId: states.releaseStart.id,
  });
  console.log(`Moved ${linear.identifier} to release project "${issue.milestone}".`);
}

async function onDemilestoned(issue) {
  const linear = await findLinearIssue(issue);
  if (!linear) return warnNoLinear(issue);
  const team = await getTeam();
  const states = await getStates(team);
  await client.updateIssue(linear.id, {
    projectId: null,
    stateId: (states.triage || states.backlog).id,
  });
  console.log(`Moved ${linear.identifier} back to Triage.`);
}

async function onClosed(issue) {
  const linear = await findLinearIssue(issue);
  if (!linear) return warnNoLinear(issue);
  const team = await getTeam();
  const states = await getStates(team);
  // "completed" -> Done; "not_planned" (or anything else) -> Cancelled.
  const done = issue.stateReason === "completed";
  await client.updateIssue(linear.id, {
    stateId: done ? states.done.id : states.cancelled.id,
  });
  console.log(
    `Issue closed (${issue.stateReason || "unknown"}) -> ${linear.identifier} ${done ? "Done" : "Cancelled"}.`,
  );
}

// ---------------------------------------------------------------------------
// Linear resolvers
// ---------------------------------------------------------------------------

let _team;
async function getTeam() {
  if (_team) return _team;
  const res = await client.teams({ filter: { key: { eq: cfg.teamKey } } });
  if (!res.nodes.length) {
    throw new Error(`Linear team with key "${cfg.teamKey}" not found.`);
  }
  _team = res.nodes[0];
  return _team;
}

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
  const nodes = (await team.states()).nodes;
  _states = {
    triage: pickState(nodes, [], ["triage"]),
    backlog: pickState(nodes, [cfg.backlogStateName], ["backlog"]),
    accepted: pickState(nodes, [cfg.acceptedStateName], ["backlog", "unstarted"]),
    releaseStart: pickState(
      nodes,
      [cfg.releaseStartStateName],
      ["backlog", "unstarted"],
    ),
    done: pickState(nodes, [cfg.doneStateName], ["completed"]),
    cancelled: pickState(
      nodes,
      [cfg.cancelledStateName, "canceled", "cancelled"],
      ["canceled"],
    ),
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

async function getOrCreateLabel(name, teamId) {
  const res = await client.issueLabels({ filter: { name: { eq: name } } });
  const existing = res.nodes.find(
    (l) => l.name.toLowerCase() === name.toLowerCase(),
  );
  if (existing) return existing;
  const created = await client.createIssueLabel({ name, teamId });
  console.log(`Created Linear label "${name}".`);
  return await created.issueLabel;
}

async function findLinearIssue(issue) {
  let attachments;
  try {
    attachments = await client.attachmentsForURL(issue.url);
  } catch (e) {
    console.log(`attachmentsForURL failed: ${e.message}`);
    return null;
  }
  for (const att of attachments.nodes) {
    const linear = await att.issue;
    if (linear) return linear;
  }
  return null;
}

function linearDescription(issue) {
  return [
    `Mirrored from GitHub issue ${issue.url}`,
    `Opened by @${issue.authorLogin}.`,
    "",
    issueMarker(issue.repo, issue.number),
  ].join("\n");
}

function warnNoLinear(issue) {
  console.log(
    `No Linear issue found for ${issue.repo}#${issue.number}; nothing to update.`,
  );
}

// ---------------------------------------------------------------------------
// GitHub side effects (ack comment + triage board)
// ---------------------------------------------------------------------------

async function postAcknowledgment(issue) {
  const token = process.env.GH_TOKEN;
  if (!token) return console.log("No GH_TOKEN; skipping acknowledgment.");

  // Don't post twice.
  const listRes = await fetch(
    `https://api.github.com/repos/${issue.repo}/issues/${issue.number}/comments?per_page=100`,
    { headers: ghHeaders(token) },
  );
  if (listRes.ok) {
    const comments = await listRes.json();
    if (comments.some((c) => (c.body || "").includes(ackMarker))) {
      console.log("Acknowledgment already posted; skipping.");
      return;
    }
  }

  const body =
    (process.env.ACK_MESSAGE ||
      [
        `👋 Thanks for opening this issue and helping improve Gonka, @${issue.authorLogin}!`,
        "",
        "A maintainer will triage it as part of our regular process. Please note that response",
        "times vary and opening an issue doesn't guarantee it will be addressed. Adding clear",
        "reproduction steps, logs, or context helps us a lot.",
        "",
        "We appreciate you taking the time to report this!",
      ].join("\n")) + `\n\n${ackMarker}`;

  const res = await fetch(
    `https://api.github.com/repos/${issue.repo}/issues/${issue.number}/comments`,
    { method: "POST", headers: ghHeaders(token), body: JSON.stringify({ body }) },
  );
  if (res.ok) console.log(`Posted acknowledgment on issue #${issue.number}.`);
  else console.log(`Acknowledgment comment failed (${res.status}).`);
}

// Add the issue to the GitHub Projects v2 triage board and set its Status.
// overwrite=false -> only set when the item currently has no status.
async function addToTriageBoard(issue, statusValue, { overwrite }) {
  const token = process.env.PROJECTS_TOKEN;
  if (!token) return console.log("No PROJECTS_TOKEN; skipping triage board.");
  if (!cfg.triageProjectOwner || !cfg.triageProjectNumber) {
    return console.log("Triage board not configured; skipping.");
  }

  const gql = (query, variables) =>
    fetch("https://api.github.com/graphql", {
      method: "POST",
      headers: ghHeaders(token),
      body: JSON.stringify({ query, variables }),
    }).then((r) => r.json());

  const projRes = await gql(
    `query($owner:String!,$number:Int!){
       organization(login:$owner){ projectV2(number:$number){ id fields(first:50){ nodes{ ... on ProjectV2SingleSelectField { id name options { id name } } } } } }
       user(login:$owner){ projectV2(number:$number){ id fields(first:50){ nodes{ ... on ProjectV2SingleSelectField { id name options { id name } } } } } }
     }`,
    { owner: cfg.triageProjectOwner, number: cfg.triageProjectNumber },
  );
  const project =
    projRes.data?.organization?.projectV2 || projRes.data?.user?.projectV2;
  if (!project) {
    console.log(`Triage project #${cfg.triageProjectNumber} not found; skipping.`);
    return;
  }

  const addRes = await gql(
    `mutation($projectId:ID!,$contentId:ID!){ addProjectV2ItemById(input:{projectId:$projectId,contentId:$contentId}){ item { id } } }`,
    { projectId: project.id, contentId: issue.nodeId },
  );
  const itemId = addRes.data?.addProjectV2ItemById?.item?.id;
  if (!itemId) {
    console.log("Could not add issue to triage board; skipping status.");
    return;
  }

  if (!overwrite) {
    const cur = await gql(
      `query($itemId:ID!,$fieldName:String!){ node(id:$itemId){ ... on ProjectV2Item { fieldValueByName(name:$fieldName){ ... on ProjectV2ItemFieldSingleSelectValue { optionId name } } } } }`,
      { itemId, fieldName: cfg.triageStatusField },
    );
    const existing = cur.data?.node?.fieldValueByName;
    if (existing && (existing.optionId || existing.name)) {
      console.log(`Board status already "${existing.name}"; leaving unchanged.`);
      return;
    }
  }

  const field = (project.fields?.nodes || []).find(
    (f) => f && f.name && f.name.toLowerCase() === cfg.triageStatusField.toLowerCase(),
  );
  const option = field?.options?.find(
    (o) => o.name.toLowerCase() === statusValue.toLowerCase(),
  );
  if (!field || !option) {
    console.log(`Status option "${statusValue}" not found; item added without status.`);
    return;
  }
  await gql(
    `mutation($projectId:ID!,$itemId:ID!,$fieldId:ID!,$optionId:String!){ updateProjectV2ItemFieldValue(input:{projectId:$projectId,itemId:$itemId,fieldId:$fieldId,value:{singleSelectOptionId:$optionId}}){ projectV2Item { id } } }`,
    { projectId: project.id, itemId, fieldId: field.id, optionId: option.id },
  );
  console.log(`Set board ${cfg.triageStatusField}="${statusValue}" on issue #${issue.number}.`);
}

function ghHeaders(token) {
  return {
    Authorization: `Bearer ${token}`,
    Accept: "application/vnd.github+json",
    "Content-Type": "application/json",
  };
}

// ---------------------------------------------------------------------------
// GitHub event parsing
// ---------------------------------------------------------------------------

function loadIssue() {
  const eventPath = process.env.GITHUB_EVENT_PATH;
  if (!eventPath || !fs.existsSync(eventPath)) {
    throw new Error("GITHUB_EVENT_PATH not available.");
  }
  const event = JSON.parse(fs.readFileSync(eventPath, "utf8"));
  const gh = event.issue;
  if (!gh) {
    console.log("Event has no issue payload, skipping.");
    return null;
  }
  // Pull requests also arrive as "issues" in some contexts; never treat a PR as an issue.
  if (gh.pull_request) {
    console.log("Payload is a pull request, not an issue; skipping.");
    return null;
  }
  return {
    repo: event.repository?.full_name,
    number: gh.number,
    title: gh.title,
    url: gh.html_url,
    nodeId: gh.node_id,
    authorLogin: gh.user?.login,
    authorAssociation: gh.author_association,
    isBot: gh.user?.type === "Bot",
    milestone: gh.milestone?.title || null,
    stateReason: gh.state_reason || null,
    firstTimeContributor:
      gh.author_association === "FIRST_TIME_CONTRIBUTOR" ||
      gh.author_association === "FIRST_TIMER",
    labelName: event.label?.name || null,
  };
}

main().catch((err) => {
  console.error(err);
  ghWarn(
    `Linear issue sync failed (non-blocking): ${err && err.message ? err.message : err}`,
  );
  process.exit(0);
});
