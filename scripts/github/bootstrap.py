#!/usr/bin/env python3
"""Bootstrap GitHub labels, milestones, issues and the Projects (v2) board
from scripts/github/tickets.json.

Idempotent: issues are matched by their title prefix ("PLAT-001:", "PLAT:"),
so re-running only creates what is missing and refreshes field values.
Status is only set on newly added project items, so progress is never reset.

Usage:
  python3 scripts/github/bootstrap.py [--dry-run]

Requires: gh CLI authenticated with `repo` and `project` scopes.
"""
import json
import os
import subprocess
import sys
import time

DRY = "--dry-run" in sys.argv
HERE = os.path.dirname(os.path.abspath(__file__))
M = json.load(open(os.path.join(HERE, "tickets.json")))
REPO, OWNER = M["repo"], M["owner"]

STATUS_OPTIONS = [
    ("Backlog", "GRAY", "Not yet scheduled"),
    ("Todo", "BLUE", "Scheduled for the current milestone"),
    ("In progress", "YELLOW", "Actively being worked on"),
    ("In review", "PURPLE", "PR open"),
    ("Done", "GREEN", "Merged / closed"),
]
COLORS = ["BLUE", "GREEN", "YELLOW", "ORANGE", "RED", "PINK", "PURPLE", "GRAY"]
DOD = [
    "Authorization enforced",
    "Unit tests (test-first) and integration tests where applicable",
    "API contracts documented",
    "Errors handled consistently",
    "Logs and metrics available",
    "Relevant docs updated",
    "No secrets exposed",
    "Reviewed",
]


def log(msg):
    print(msg, flush=True)


def run(args, payload=None, ok_fail=False):
    for attempt in range(5):
        p = subprocess.run(["gh"] + args, input=payload, capture_output=True, text=True)
        if p.returncode == 0:
            return json.loads(p.stdout) if p.stdout.strip().startswith(("{", "[")) else p.stdout
        err = p.stderr + p.stdout
        if "rate limit" in err.lower() and attempt < 4:
            log(f"  rate limited, sleeping 60s ({attempt + 1}/4)")
            time.sleep(60)
            continue
        transient = ("timeout", "connection reset", "eof", "http 502", "http 503", "http 504", "tls")
        if any(s in err.lower() for s in transient) and attempt < 4:
            log(f"  transient error, retrying in {5 * (attempt + 1)}s: {err.strip()[:120]}")
            time.sleep(5 * (attempt + 1))
            continue
        if ok_fail:
            return None
        raise RuntimeError(f"gh {' '.join(args[:3])} failed: {err.strip()}")


def gql(query, ok_fail=False, **variables):
    body = json.dumps({"query": query, "variables": variables})
    res = run(["api", "graphql", "--input", "-"], payload=body, ok_fail=ok_fail)
    if res is None:
        return None
    if res.get("errors"):
        if ok_fail:
            log(f"  graphql warning: {res['errors'][0].get('message')}")
            return None
        raise RuntimeError(json.dumps(res["errors"]))
    return res["data"]


def rest(method, path, data=None, ok_fail=False):
    args = ["api", "-X", method, path, "-H", "Accept: application/vnd.github+json"]
    if data is not None:
        return run(args + ["--input", "-"], payload=json.dumps(data), ok_fail=ok_fail)
    return run(args, ok_fail=ok_fail)


# ---------------------------------------------------------------- labels
def ensure_labels():
    labels = [(f"epic:{e['key'].lower()}", "5319e7", e["goal"][:100]) for e in M["epics"]]
    labels += [("priority:p0", "b60205", "Must have for MVP"),
               ("priority:p1", "d93f0b", "Should have for MVP"),
               ("priority:p2", "fbca04", "Nice to have / post-MVP")]
    labels += [(f"type:{t}", "1d76db", t) for t in ["feature", "chore", "test", "docs", "spike", "epic"]]
    for name, color, desc in labels:
        log(f"label {name}")
        if not DRY:
            run(["label", "create", name, "--color", color, "--description", desc, "--force", "-R", REPO])


# ------------------------------------------------------------ milestones
def ensure_milestones():
    existing = {m["title"]: m["number"] for m in rest("GET", f"repos/{REPO}/milestones?state=all&per_page=100")}
    out = {}
    for ms in M["milestones"]:
        if ms["title"] in existing:
            out[ms["key"]] = existing[ms["title"]]
            continue
        log(f"milestone + {ms['title']}")
        if DRY:
            out[ms["key"]] = 0
            continue
        r = rest("POST", f"repos/{REPO}/milestones",
                 {"title": ms["title"], "description": ms["description"], "due_on": ms["due"] + "T23:59:59Z"})
        out[ms["key"]] = r["number"]
    return out


# --------------------------------------------------------------- project
FIELDS_Q = """
query($id: ID!) { node(id: $id) { ... on ProjectV2 { fields(first: 50) { nodes {
  ... on ProjectV2FieldCommon { id name dataType }
  ... on ProjectV2SingleSelectField { options { id name } }
  ... on ProjectV2IterationField { configuration { iterations { id title startDate } } }
}}}}}"""


def ensure_project():
    d = gql("query($l: String!) { user(login: $l) { id projectsV2(first: 100) { nodes { id number title url } } } }",
            l=OWNER)
    for p in d["user"]["projectsV2"]["nodes"]:
        if p["title"] == M["project_title"]:
            log(f"project exists: {p['url']}")
            return p
    log(f"project + {M['project_title']}")
    if DRY:
        return None
    repo_id = rest("GET", f"repos/{REPO}")["node_id"]
    p = gql("""mutation($o: ID!, $t: String!, $r: ID!) { createProjectV2(input: {ownerId: $o, title: $t, repositoryId: $r})
              { projectV2 { id number title url } } }""", o=d["user"]["id"], t=M["project_title"], r=repo_id)
    p = p["createProjectV2"]["projectV2"]
    gql("""mutation($id: ID!, $d: String!) { updateProjectV2(input: {projectId: $id, shortDescription: $d, public: false})
          { projectV2 { id } } }""", ok_fail=True, id=p["id"],
        d="Konduite MVP: agent collaboration platform (M0-M5)")
    log(f"  created {p['url']}")
    return p


def get_fields(pid):
    return {f["name"]: f for f in gql(FIELDS_Q, id=pid)["node"]["fields"]["nodes"] if f}


def opts(names):
    return [{"name": n, "color": COLORS[i % len(COLORS)], "description": ""} for i, n in enumerate(names)]


def ensure_fields(pid):
    fields = get_fields(pid)
    status = fields.get("Status")
    want = [o[0] for o in STATUS_OPTIONS]
    if status and [o["name"] for o in status["options"]] != want:
        log("field Status -> " + ", ".join(want))
        if not DRY:
            gql("""mutation($f: ID!, $o: [ProjectV2SingleSelectFieldOptionInput!]) {
                  updateProjectV2Field(input: {fieldId: $f, singleSelectOptions: $o}) { projectV2Field { __typename } } }""",
                ok_fail=True, f=status["id"],
                o=[{"name": n, "color": c, "description": d} for n, c, d in STATUS_OPTIONS])
    single = {
        "Epic": [e["key"] for e in M["epics"]],
        "Priority": ["P0", "P1", "P2"],
        "Size": ["XS", "S", "M", "L"],
    }
    for name, values in single.items():
        if name in fields:
            continue
        log(f"field + {name}")
        if not DRY:
            gql("""mutation($p: ID!, $n: String!, $o: [ProjectV2SingleSelectFieldOptionInput!]) {
                  createProjectV2Field(input: {projectId: $p, dataType: SINGLE_SELECT, name: $n, singleSelectOptions: $o})
                  { projectV2Field { __typename } } }""", p=pid, n=name, o=opts(values))
    if "PRD ref" not in fields:
        log("field + PRD ref")
        if not DRY:
            gql("""mutation($p: ID!) { createProjectV2Field(input: {projectId: $p, dataType: TEXT, name: "PRD ref"})
                  { projectV2Field { __typename } } }""", p=pid)
    if "Week" not in fields:
        log("field + Week (iteration)")
        if not DRY:
            start = M["iteration_start"]
            weeks = max(m["week"] for m in M["milestones"])
            import datetime as dt
            s = dt.date.fromisoformat(start)
            its = [{"startDate": (s + dt.timedelta(days=7 * i)).isoformat(), "duration": 7, "title": f"Week {i + 1}"}
                   for i in range(weeks)]
            gql("""mutation($p: ID!, $c: ProjectV2IterationFieldConfigurationInput!) {
                  createProjectV2Field(input: {projectId: $p, dataType: ITERATION, name: "Week", iterationConfiguration: $c})
                  { projectV2Field { __typename } } }""", ok_fail=True, p=pid,
                c={"startDate": start, "duration": 7, "iterations": its})
    return {} if DRY else get_fields(pid)


# ---------------------------------------------------------------- issues
def existing_issues():
    out = {}
    for i in run(["issue", "list", "-R", REPO, "--state", "all", "--limit", "1000",
                  "--json", "number,title,id,body"]):
        key = i["title"].split(":")[0].strip()
        out[key] = i
    return out


def ticket_body(t, epic_num, nums):
    deps = ", ".join(f"#{nums[d]} ({d})" if nums.get(d) else d for d in t["deps"]) or "None"
    ms = t["m"] or "Backlog (post-MVP)"
    lines = [
        f"<!-- ticket:{t['id']} -->",
        f"**Epic:** #{epic_num} · **Milestone:** {ms} · **Priority:** {t['p']} · **Size:** {t['size']} · **PRD:** {t['prd']}",
        "", "## Goal", t["goal"],
        "", "## Acceptance criteria", *[f"- [ ] {a}" for a in t["ac"]],
        "", "## Dependencies", f"Blocked by: {deps}",
        "", "## Definition of done (PRD §21)", *[f"- [ ] {d}" for d in DOD],
    ]
    return "\n".join(lines)


def epic_body(e):
    return "\n".join([f"<!-- epic:{e['key']} -->", "## Goal", e["goal"], "",
                      "Tickets are tracked as sub-issues of this epic.",
                      "Plan: see `scripts/github/tickets.json` and the Konduite MVP project board."])


def create_issue(title, body, labels, milestone):
    log(f"issue + {title}")
    if DRY:
        return {"number": 0, "id": 0, "node_id": None}
    data = {"title": title, "body": body, "labels": labels}
    if milestone:
        data["milestone"] = milestone
    r = rest("POST", f"repos/{REPO}/issues", data)
    time.sleep(1)  # stay under the secondary content-creation rate limit
    return r


def issue_ids(number):
    r = rest("GET", f"repos/{REPO}/issues/{number}")
    return {"number": number, "id": r["id"], "node_id": r["node_id"], "body": r["body"] or ""}


def main():
    ensure_labels()
    ms = ensure_milestones()
    project = ensure_project()
    fields = ensure_fields(project["id"]) if project else {}
    have = existing_issues()
    issues = {}  # key -> {number, id, node_id, created}

    for e in M["epics"]:
        if e["key"] in have:
            issues[e["key"]] = {**issue_ids(have[e["key"]]["number"]), "created": False}
        else:
            r = create_issue(e["title"], epic_body(e), [f"epic:{e['key'].lower()}", "type:epic"], None)
            issues[e["key"]] = {"number": r["number"], "id": r["id"], "node_id": r["node_id"], "created": True}

    for t in M["tickets"]:
        epic = t["id"].split("-")[0]
        if t["id"] in have:
            issues[t["id"]] = {**issue_ids(have[t["id"]]["number"]), "created": False}
            continue
        nums = {k: v["number"] for k, v in issues.items()}
        labels = [f"epic:{epic.lower()}", f"priority:{t['p'].lower()}", f"type:{t['type']}"]
        r = create_issue(f"{t['id']}: {t['title']}", ticket_body(t, issues[epic]["number"], nums), labels,
                         ms.get(t["m"]) if t["m"] else None)
        issues[t["id"]] = {"number": r["number"], "id": r["id"], "node_id": r["node_id"], "created": True}

    if DRY:
        log(f"dry run: {len(M['epics'])} epics, {len(M['tickets'])} tickets")
        return

    nums = {k: v["number"] for k, v in issues.items()}

    # Second pass: resolve dependency numbers in bodies, sub-issues, native dependencies.
    for t in M["tickets"]:
        epic = t["id"].split("-")[0]
        it = issues[t["id"]]
        body = ticket_body(t, nums[epic], nums)
        current = it.get("body")
        if current is None or current.strip() != body.strip():
            if current is None or f"<!-- ticket:{t['id']} -->" in current:
                rest("PATCH", f"repos/{REPO}/issues/{it['number']}", {"body": body})
        rest("POST", f"repos/{REPO}/issues/{nums[epic]}/sub_issues", {"sub_issue_id": it["id"]}, ok_fail=True)
        for d in t["deps"]:
            if d in issues:
                rest("POST", f"repos/{REPO}/issues/{it['number']}/dependencies/blocked_by",
                     {"issue_id": issues[d]["id"]}, ok_fail=True)
        log(f"linked {t['id']} -> #{it['number']}")

    # Project items and field values.
    pid = project["id"]
    opt = lambda f, n: next((o["id"] for o in fields.get(f, {}).get("options", []) if o["name"] == n), None)
    week_ids = {}
    if "Week" in fields:
        for i, it in enumerate(fields["Week"]["configuration"]["iterations"]):
            week_ids[i + 1] = it["id"]
    ms_week = {m["key"]: m["week"] for m in M["milestones"]}

    def set_fields(item_id, values):
        parts, i = [], 0
        for fname, value in values:
            if value is None or fname not in fields:
                continue
            parts.append(f'f{i}: updateProjectV2ItemFieldValue(input: {{projectId: "{pid}", itemId: "{item_id}", '
                         f'fieldId: "{fields[fname]["id"]}", value: {value}}}) {{ projectV2Item {{ id }} }}')
            i += 1
        if parts:
            gql("mutation {\n" + "\n".join(parts) + "\n}")

    rows = [(e["key"], e["key"], None) for e in M["epics"]] + [(t["id"], t["id"].split("-")[0], t) for t in M["tickets"]]
    for key, epic, t in rows:
        it = issues[key]
        res = gql("""mutation($p: ID!, $c: ID!) { addProjectV2ItemById(input: {projectId: $p, contentId: $c})
                     { item { id status: fieldValueByName(name: "Status") {
                         ... on ProjectV2ItemFieldSingleSelectValue { name } } } } }""",
                  p=pid, c=it["node_id"])["addProjectV2ItemById"]["item"]
        item = res["id"]
        sel = lambda f, n: (f'{{singleSelectOptionId: "{opt(f, n)}"}}' if opt(f, n) else None)
        values = [("Epic", sel("Epic", epic))]
        if not res.get("status"):  # never overwrite progress, only fill blanks
            values.append(("Status", sel("Status", "Backlog")))
        if t:
            values += [("Priority", sel("Priority", t["p"])), ("Size", sel("Size", t["size"])),
                       ("PRD ref", json.dumps({"text": t["prd"]}).replace('"text"', "text"))]
            w = week_ids.get(ms_week.get(t["m"])) if t["m"] else None
            if w:
                values.append(("Week", f'{{iterationId: "{w}"}}'))
        set_fields(item, values)
        log(f"project item {key}")

    log(f"\nDone: {len(M['epics'])} epics, {len(M['tickets'])} tickets -> {project['url']}")


if __name__ == "__main__":
    main()
