#!/usr/bin/env python3
"""Can a local model write the project brief the worker asks Haiku for?

A project brief (issue #32) rolls up to 100 of a project's observations, oldest first, and the developer's own
checkpoint notes into one dated orientation that a fresh chat reads first. This harness builds that request exactly
as the worker does, from your own database, has a model write the brief, and scores it:

  * mechanical checks: the four required sections in order, the 300-word limit, citations that exist in the request
    against invented ones, emails, an open-items section, specifics that are not in the notes, and similarity to
    Haiku's brief;
  * a blind Haiku judge: faithfulness to the notes, currency (a later note overrides an earlier one, one-off
    incidents are left out), usefulness as a first read, and concision.

    briefs.py samples --db ~/.claude-mnemonic/claude-mnemonic.db --auto 6 --out DIR      (or --project NAME_abc123 ...)
    briefs.py run     --out DIR --model haiku
    briefs.py run     --out DIR --model gemma4:e4b-mlx
    briefs.py score   --out DIR --judge

The database is opened read-only; the samples stay under --out, which should be outside the repository. Haiku calls
(the reference, and the judge) use your Claude account.
"""
import argparse
import json
import os
import random
import re
import sqlite3
import sys
import time
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import evalkit as ek  # noqa: E402

SECTIONS = ["What this is", "Current state", "Key decisions (and why)", "Conventions and gotchas"]
# The limits of internal/worker/sdk/brief.go (a test keeps them in step).
NARRATIVE_CHARS, TITLE_CHARS, THREAD_CHARS, MAX_PROMPT_BYTES, OBSERVATION_LIMIT, THREAD_LIMIT = 420, 160, 280, 90 * 1024, 100, 8
WORD_LIMIT = 300
UNWANTED = ["open item", "open thread", "to-do", "todo", "next step"]
CITATION = re.compile(r"([ \t]*)\[#([0-9][0-9,\s#]*)\]")
EMAIL = re.compile(r"[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}")


def worker_system_prompt():
    """The brief system prompt, read from the worker's source so the harness stays in step."""
    src = open(os.path.join(ek.REPO, "internal", "worker", "sdk", "brief.go"), encoding="utf-8").read()
    return re.search(r"const briefSystemPrompt = `(.*?)`", src, re.S).group(1)


def clip_runes(s, n):
    s = " ".join((s or "").split())
    return s if len(s) <= n else s[:n].strip() + "…"


def display_name(project):
    return re.sub(r"_[0-9a-f]{6}$", "", project)


def day(epoch_ms):
    return time.strftime("%Y-%m-%d", time.gmtime((epoch_ms or 0) / 1000))


# ------------------------------------------------------------------ the worker's input
LIVE = "project = ? AND (scope IS NULL OR scope = 'project') AND COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0"
# The worker leaves out global-scope notes. Most notes of a real archive can be global, so --include-global also
# tries the models on the fuller input (not what the worker sends today).
LIVE_ALL = "project = ? AND COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0"


def read_project(db, project, include_global=False):
    """What BriefInputs and GetThreadSummaries give the worker: the project's live observations by importance (at most
    100), oldest first, how many there are in all, and the thread notes, most recently updated first."""
    live = LIVE_ALL if include_global else LIVE
    con = sqlite3.connect(f"file:{os.path.abspath(db)}?mode=ro", uri=True)
    try:
        total = con.execute(f"SELECT COUNT(*) FROM observations WHERE {live}", (project,)).fetchone()[0]
        rows = con.execute(f"SELECT id, type, title, subtitle, narrative, created_at_epoch FROM observations WHERE {live} "
                           "ORDER BY COALESCE(importance_score, 1.0) DESC, created_at_epoch DESC LIMIT ?", (project, OBSERVATION_LIMIT)).fetchall()
        threads = con.execute("SELECT request, notes, completed, learned, created_at_epoch FROM session_summaries "
                              "WHERE project = ? AND sdk_session_id LIKE 'thread-%' ORDER BY created_at_epoch DESC LIMIT ?", (project, THREAD_LIMIT)).fetchall()
    finally:
        con.close()
    rows.sort(key=lambda r: (r[5], r[0]))
    return {
        "id": project, "name": display_name(project), "total": total,
        "observations": [{"id": r[0], "type": r[1] or "", "title": r[2] or "", "subtitle": r[3] or "", "narrative": r[4] or "", "epoch": r[5]} for r in rows],
        "threads": [{"request": t[0] or "", "goal": t[1] or "", "progress": t[2] or "", "decisions": t[3] or "", "epoch": t[4]} for t in threads],
    }


def largest_projects(db, n, min_obs, include_global=False):
    con = sqlite3.connect(f"file:{os.path.abspath(db)}?mode=ro", uri=True)
    try:
        aliases = {r[0] for r in con.execute("SELECT alias FROM project_aliases")} if con.execute(
            "SELECT 1 FROM sqlite_master WHERE name = 'project_aliases'").fetchone() else set()
        scope = "" if include_global else "AND (scope IS NULL OR scope = 'project') "
        rows = con.execute("SELECT project, COUNT(*) c FROM observations WHERE project IS NOT NULL AND project <> '' " + scope +
                           "AND COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0 GROUP BY project ORDER BY c DESC").fetchall()
    finally:
        con.close()
    return [p for p, c in rows if c >= min_obs and p not in aliases][:n]


def build_prompt(sample):
    """The worker's request (buildBriefPrompt): the oldest observations are dropped when it would be too large."""
    system = worker_system_prompt()
    used = sample["observations"]
    while True:
        out = ["PROJECT BRIEF REQUEST\n", f"AS OF: {sample['as_of']}\nPROJECT: {sample['name']}\n\n"]
        if sample["threads"]:
            out.append("CURRENT WORK (the developer's own checkpoint notes, most recent first):\n")
            for t in sample["threads"]:
                line = f"- {clip_runes(t['request'], 80)} (updated {day(t['epoch'])})"
                for label, text in (("goal", t["goal"]), ("progress", t["progress"]), ("decisions", t["decisions"])):
                    if text.strip():
                        line += f"; {label}: {clip_runes(text, THREAD_CHARS)}"
                out.append(line + "\n")
            out.append("\n")
        out.append(f"OBSERVATIONS ({len(used)} of {sample['total']}, oldest first):\n\n")
        for o in used:
            out.append(f"[#{o['id']}] ({o['type']}, {day(o['epoch'])}) {clip_runes(o['title'], TITLE_CHARS)}\n")
            if o["subtitle"].strip():
                out.append(f"  {clip_runes(o['subtitle'], TITLE_CHARS)}\n")
            if o["narrative"].strip():
                out.append(f"  {clip_runes(o['narrative'], NARRATIVE_CHARS)}\n")
            out.append("\n")
        prompt = "".join(out)
        if len(prompt.encode()) + len(system.encode()) <= MAX_PROMPT_BYTES or len(used) <= 1:
            return system, prompt, used
        used = used[max(1, len(used) // 10):]


# ------------------------------------------------------------------ what the worker stores
def clean_body(body, known):
    """What the worker keeps of the model's text (cleanBriefBody): no code fence, no open-items section, citations of notes
    that were not in the request removed, emails removed, private text removed."""
    body = (body or "").strip()
    if body.startswith("```"):
        for prefix in ("```markdown", "```md", "```"):
            if body.startswith(prefix):
                body = body[len(prefix):]
                break
        body = body.strip()
        if body.endswith("```"):
            body = body[:-3]
        body = body.strip()
    kept, skipping = [], False
    for line in body.split("\n"):
        if line.startswith("## ") or line == "##":
            heading = line[2:].strip().lower()
            skipping = any(heading.startswith(u) for u in UNWANTED)
        if not skipping:
            kept.append(line)
    body = "\n".join(kept).strip()

    def cite(m):
        keep = ["#" + n for n in re.findall(r"\d+", m.group(0)) if int(n) in known]
        return m.group(1) + "[" + ", ".join(keep) + "]" if keep else ""
    body = CITATION.sub(cite, body)
    body = EMAIL.sub("[email removed]", body)
    body = re.sub(r"<private>.*?</private>", "", body, flags=re.S)
    return body.strip()


def cited_ids(text):
    ids = []
    for m in CITATION.finditer(text or ""):
        ids += [int(n) for n in re.findall(r"\d+", m.group(2))]
    return ids


# ------------------------------------------------------------------ commands
def cmd_samples(args):
    projects = list(args.project or [])
    if args.auto:
        projects += [p for p in largest_projects(args.db, args.auto, args.min_obs, args.include_global) if p not in projects]
    if not projects:
        sys.exit("name a --project or use --auto N")
    as_of = time.strftime("%Y-%m-%d", time.gmtime())
    samples = []
    for i, p in enumerate(projects, 1):
        s = read_project(args.db, p, args.include_global)
        if s["total"] == 0:
            print("skipping", p, "(no live observations)")
            continue
        s.update({"sid": f"B{i:02d}", "as_of": as_of})
        samples.append(s)
    os.makedirs(args.out, exist_ok=True)
    json.dump(samples, open(os.path.join(args.out, "samples.json"), "w"), indent=1)
    for s in samples:
        _, prompt, used = build_prompt(s)
        print(s["sid"], s["name"], f"{s['total']} observations ({len(used)} used), {len(s['threads'])} thread notes, prompt {len(prompt) // 1024} KB")


def result_path(out, model):
    return os.path.join(out, "results", f"briefs__{ek.safe_name(model)}.json")


def one_run(model, sample, num_ctx=16384):
    system, prompt, used = build_prompt(sample)
    r = ek.run_haiku(system + "\n\n" + prompt) if model == "haiku" else ek.run_ollama(model, system, prompt, num_ctx=num_ctx)
    known = {o["id"] for o in used}
    r.update({"sid": sample["sid"], "model": model, "known": sorted(known), "cleaned": clean_body(r["raw"], known)})
    return r


def cmd_run(args):
    samples = json.load(open(os.path.join(args.out, "samples.json")))
    os.makedirs(os.path.join(args.out, "results"), exist_ok=True)
    with ThreadPoolExecutor(3 if args.model == "haiku" else 1) as ex:  # local models run one at a time
        out = list(ex.map(lambda s: one_run(args.model, s, args.num_ctx), samples))
    json.dump(out, open(result_path(args.out, args.model), "w"), indent=1)
    for r in out:
        print(args.model, r["sid"], r["secs"], "s", len(r["raw"].split()), "words", r.get("error", ""))


# ------------------------------------------------------------------ scoring
def specifics(text):
    """Numbers, file names and `code` words a brief states."""
    found = set(re.findall(r"\b\d{2,}\b", text)) | set(re.findall(r"`([^`]{2,40})`", text))
    found |= set(re.findall(r"\b[\w\-]+\.(?:go|py|js|ts|vue|md|json|sh|sql|yml|yaml)\b", text))
    return {x.lower() for x in found}


def metrics(r, sample):
    raw = r.get("raw") or ""
    _, prompt, _ = build_prompt(sample)
    headings = [h.strip() for h in re.findall(r"^##\s+(.+)$", raw, re.M)]
    wanted = [h for h in headings if any(h.lower().startswith(s.lower().split(" (")[0]) for s in SECTIONS)]
    in_order = [h.lower().split(" (")[0] for h in wanted] == [s.lower().split(" (")[0] for s in SECTIONS]
    known = set(r.get("known") or [])
    ids = cited_ids(raw)
    sp = specifics(raw)
    src = prompt.lower()
    return {"ok": bool(raw.strip()), "sections": in_order, "words": len(raw.split()), "over_limit": len(raw.split()) > WORD_LIMIT,
            "cites": len(ids), "invented": sum(1 for i in ids if i not in known), "distinct": len(set(i for i in ids if i in known)),
            "emails": len(EMAIL.findall(raw)), "unwanted": any(h.lower().startswith(u) for h in headings for u in UNWANTED),
            "specifics": len(sp), "ungrounded": sum(1 for x in sp if x not in src)}


JUDGE = """You are checking a project brief written for an assistant that starts a fresh conversation about a developer's project. Judge only against the NOTES it was written from. The notes are point-in-time and oldest first: a later note overrides an earlier one.

NOTES (the request the writer received):
{source}

THE BRIEF:
{brief}

Score from 1 to 5:
- faithfulness: 5 = every claim is supported by the notes; 3 = mostly supported with small inventions; 1 = invents or contradicts them.
- currency: 5 = states the project's latest state, says when a later note changed an earlier one and leaves out one-off incidents; 1 = presents superseded or one-off things as current.
- usefulness: 5 = an assistant could start work from it alone and knows what matters; 1 = generic or misleading.
- concision: 5 = dense, no filler or praise, within about 300 words; 1 = padded or rambling.
List up to 5 claims in the brief that the notes do not support or contradict (quote them briefly); use an empty list if none.
Reply with only JSON: {{"faithfulness": n, "currency": n, "usefulness": n, "concision": n, "unsupported": ["..."]}}"""


def judge_one(item):
    key, sample, brief = item
    _, prompt, _ = build_prompt(sample)
    r = ek.run_haiku(JUDGE.format(source=prompt[:60000], brief=brief))
    d = ek.parse_json_object(r["raw"])
    try:
        return key, {k: d[k] for k in ("faithfulness", "currency", "usefulness", "concision")} | {"unsupported": d.get("unsupported", [])}
    except (TypeError, KeyError):
        return key, None


def load_results(out):
    res = {}
    for name in sorted(os.listdir(os.path.join(out, "results"))) if os.path.isdir(os.path.join(out, "results")) else []:
        if name.startswith("briefs__") and name.endswith(".json"):
            for r in json.load(open(os.path.join(out, "results", name))):
                res[(r["model"], r["sid"])] = r
    return res


def cmd_score(args):
    samples = {s["sid"]: s for s in json.load(open(os.path.join(args.out, "samples.json")))}
    results = load_results(args.out)
    jpath = os.path.join(args.out, "results", "briefs_judge.json")
    judged = json.load(open(jpath)) if os.path.exists(jpath) else {}
    if args.judge:
        todo = [("|".join(k), samples[k[1]], r["raw"]) for k, r in results.items() if r.get("raw", "").strip() and "|".join(k) not in judged]
        random.Random(7).shuffle(todo)  # the judge sees the briefs in no particular order
        print(f"judging {len(todo)} briefs", file=sys.stderr)
        with ThreadPoolExecutor(4) as ex:
            for key, v in ex.map(judge_one, todo):
                if v:
                    judged[key] = v
        json.dump(judged, open(jpath, "w"), indent=1)
    ref = {k[1]: r["raw"] for k, r in results.items() if k[0] == "haiku" and r.get("raw", "").strip()}
    models = sorted({k[0] for k in results}, key=lambda m: (m != "haiku", m))
    print(f"{'model':<16}{'valid':>6}{'secs':>6}{'words':>6}{'>300':>5}{'cites':>6}{'invent':>7}{'distinct':>9}{'email':>6}{'unwant':>7}{'ungr%':>6}"
          f"{'faith':>6}{'curr':>5}{'useful':>7}{'conc':>5}{'F>=4':>6}{'sim':>6}")
    for m in models:
        items = [(k, r) for k, r in results.items() if k[0] == m]
        met = [metrics(r, samples[k[1]]) for k, r in items]
        ok = [x for x in met if x["ok"]]
        valid = sum(1 for x in met if x["ok"] and x["sections"] and not x["unwanted"])
        sp, ug = sum(x["specifics"] for x in ok), sum(x["ungrounded"] for x in ok)
        j = [judged["|".join(k)] for k, _ in items if "|".join(k) in judged]
        sims = []
        if m != "haiku":
            for k, r in items:
                if r.get("raw", "").strip() and k[1] in ref:
                    a, b = ek.embed([r["raw"], ref[k[1]]])
                    sims.append(ek.cosine(a, b))
        print(f"{m:<16}{valid:>3}/{len(items):<2}{ek.mean([r['secs'] for _, r in items]):>6.1f}{ek.mean([x['words'] for x in ok]):>6.0f}"
              f"{sum(x['over_limit'] for x in ok):>5}{sum(x['cites'] for x in ok):>6}{sum(x['invented'] for x in ok):>7}"
              f"{ek.mean([x['distinct'] for x in ok]):>9.1f}{sum(x['emails'] for x in ok):>6}{sum(x['unwanted'] for x in ok):>7}"
              f"{(100 * ug / sp if sp else 0):>6.1f}{ek.mean([x['faithfulness'] for x in j]):>6.2f}{ek.mean([x['currency'] for x in j]):>5.2f}"
              f"{ek.mean([x['usefulness'] for x in j]):>7.2f}{ek.mean([x['concision'] for x in j]):>5.2f}"
              f"{sum(1 for x in j if x['faithfulness'] >= 4):>3}/{len(j):<2}{ek.mean(sims):>6.2f}")
    print("\nvalid = the four sections in order and no open-items section. invent = citations of notes that were not in the request "
          "(the worker removes them). ungr% = specifics (numbers, file names, `code`) not found in the notes.")


def main():
    ap = argparse.ArgumentParser(description="Can a local model write the project brief?")
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("samples", help="build the briefs' inputs from a database")
    p.add_argument("--db", required=True)
    p.add_argument("--project", action="append", help="a project id (name_hash); repeatable")
    p.add_argument("--auto", type=int, default=0, help="also take the N largest projects")
    p.add_argument("--min-obs", type=int, default=10)
    p.add_argument("--include-global", action="store_true", help="also use global-scope notes (the worker does not)")
    p.add_argument("--out", required=True)
    p.set_defaults(fn=cmd_samples)
    p = sub.add_parser("run", help="have one model write every brief")
    p.add_argument("--out", required=True)
    p.add_argument("--model", required=True)
    p.add_argument("--num-ctx", type=int, default=16384,
                   help="context window for a local model; the worker's default is 16384, and a prompt that does not fit loses its start, system prompt included")
    p.set_defaults(fn=cmd_run)
    p = sub.add_parser("score", help="print the comparison")
    p.add_argument("--out", required=True)
    p.add_argument("--judge", action="store_true", help="have Haiku judge the briefs that are not judged yet")
    p.set_defaults(fn=cmd_score)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
