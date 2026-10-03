#!/usr/bin/env python3
"""Compare local models with Haiku on the summaries claude-mnemonic writes.

Takes real turns from your own Claude Code transcripts (never committed), runs them through Haiku and the
local models with the worker's current prompt and with a prompt written for small models, and scores the
results with a blind Haiku judge plus a few mechanical checks.

    summaries.py samples --transcripts '~/.claude/projects/<project>/*.jsonl' --out DIR
    summaries.py run     --out DIR --model haiku                       # the reference
    summaries.py run     --out DIR --model gemma3:12b --variant both   # current and tailored prompt
    summaries.py score   --out DIR --judge                             # table; --judge calls Haiku for each candidate

Haiku calls use your Claude account (about 100 small calls for a full run). Nothing is sent anywhere else.
"""
import argparse
import glob
import json
import os
import random
import re
import sys
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import evalkit as ek  # noqa: E402

WORK = [".go", ".ts", ".js", ".py", ".md", ".json", ".yaml", ".yml", "edited", "modified", "created", "deleted", "updated", "changed",
        "added", "removed", "fixed", "implemented", "refactored", "```", "lines ", "function ", "const ", "var ", "let ", "type ", "struct ", "class ", "def ", "func "]
IDENT = re.compile(r"[\w][\w./-]*\.(?:go|py|md|ts|vue|json|sh|yaml|yml|txt|toml|mod|sum)\b|#\d+\b|`[^`\n]{2,60}`|\b\d+(?:\.\d+)+\b|\b\d{2,}\b|\b[a-z]+_[a-z_]+\b|\b[a-z]+[A-Z][A-Za-z]{3,}\b")
GENERIC = ["progress summary checkpoint", "tool execution", "waiting for further", "no specific", "analysis of", "no work was completed"]


# ------------------------------------------------------------------ samples
def read_turns(path):
    """The text turns of a Claude Code transcript as [(role, text)], system reminders removed."""
    out = []
    try:
        lines = open(path, encoding="utf-8", errors="replace").read().splitlines()
    except OSError:
        return out
    for line in lines:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("type") not in ("user", "assistant"):
            continue
        c = (d.get("message") or {}).get("content")
        text = c if isinstance(c, str) else "\n".join(b.get("text", "") for b in (c or []) if isinstance(b, dict) and b.get("type") == "text")
        text = ek.strip_system_xml(text)
        if text:
            out.append((d["type"], text))
    return out


def good_reply(text):
    """A reply the worker would summarise: substantial, with signs of real work, not agent chatter."""
    return 500 <= len(text) <= 6000 and sum(w in text.lower() for w in WORK) >= 3 and "memory extraction agent" not in text \
        and "<task-notification" not in text and "<agent-message" not in text


def excerpt(msgs, upto, budget=24000, per=1500):
    """The most recent turns up to index `upto` that fit the budget, oldest first (what the PreCompact hook sends)."""
    picked, total = [], 0
    for role, text in reversed(msgs[:upto + 1]):
        if len(text) > per:
            text = text[:per] + " …"
        entry = ("User: " if role == "user" else "Assistant: ") + text
        if total + len(entry) + 2 > budget:
            break
        picked.append(entry)
        total += len(entry) + 2
    return "\n\n".join(reversed(picked))


def spread(items, n):
    if len(items) <= n:
        return list(items)
    step = len(items) / n
    return [items[int(i * step)] for i in range(n)]


def build_samples(files, n_stop=6, n_pre=4):
    stops, pres = [], []
    for f in files:
        msgs = read_turns(f)
        idx = [i for i, (r, t) in enumerate(msgs) if r == "assistant" and good_reply(t)]
        if len(idx) < 2:
            continue
        sid = os.path.basename(f)[:8]
        for frac in (0.3, 0.7):
            i = idx[int(len(idx) * frac)]
            stops.append({"kind": "stop", "session": sid, "last_assistant": msgs[i][1], "conversation": ""})
        for frac in ((0.5, 0.9) if len(idx) >= 8 else (0.6,) if len(idx) >= 3 else ()):
            i = idx[min(len(idx) - 1, int(len(idx) * frac))]
            conv = excerpt(msgs, i)
            if len(conv) > 3000:
                pres.append({"kind": "precompact", "session": sid, "last_assistant": msgs[i][1], "conversation": conv})
    chosen = spread(stops, n_stop) + spread(pres, n_pre)
    for k, s in enumerate(chosen, 1):
        s["id"] = f"S{k:02d}-{s['kind']}"
    return chosen


# ------------------------------------------------------------------ runs
def result_path(out, model, variant):
    return os.path.join(out, "results", f"{ek.safe_name(model)}__{variant}.json")


def one_run(model, variant, sample):
    if variant == "current":
        system, prompt = ek.current_prompt(sample)
        if model == "haiku":
            r = ek.run_haiku(system + "\n\n" + prompt)
        else:
            r = ek.run_ollama(model, system, prompt)
        parsed = ek.parse_xml_summary(r["raw"])
    else:
        prompt = ek.tailored_prompt(sample)
        if model == "haiku":
            r = ek.run_haiku(ek.SYSTEM_TAILORED + "\n\n" + prompt)
        else:
            r = ek.run_ollama(model, ek.SYSTEM_TAILORED, prompt, schema=ek.SUMMARY_SCHEMA)
        parsed = ek.parse_json_summary(r["raw"])
    r.update({"id": sample["id"], "variant": variant, "model": model, "parsed": parsed})
    return r


def cmd_samples(args):
    files = sorted(f for pat in args.transcripts for f in glob.glob(os.path.expanduser(pat)))
    if not files:
        sys.exit("no transcripts matched")
    samples = build_samples(files, args.stop, args.precompact)
    os.makedirs(args.out, exist_ok=True)
    json.dump(samples, open(os.path.join(args.out, "samples.json"), "w"), indent=1)
    for s in samples:
        print(s["id"], s["session"], "last", len(s["last_assistant"]), "conversation", len(s["conversation"]))


def cmd_run(args):
    samples = json.load(open(os.path.join(args.out, "samples.json")))
    variants = ["current", "tailored"] if args.variant == "both" else [args.variant]
    os.makedirs(os.path.join(args.out, "results"), exist_ok=True)
    for variant in variants:
        workers = 3 if args.model == "haiku" else 1  # local models run one at a time
        with ThreadPoolExecutor(workers) as ex:
            out = list(ex.map(lambda s: one_run(args.model, variant, s), samples))
        json.dump(out, open(result_path(args.out, args.model, variant), "w"), indent=1)
        for r in out:
            print(args.model, variant, r["id"], r["secs"], "s", "ok" if r["parsed"] else "UNPARSED", r.get("error", ""))


# ------------------------------------------------------------------ scoring
def source_text(s):
    return (s["conversation"][-24000:] if s["kind"] == "precompact" else ek.strip_system_xml(s["last_assistant"])[:4000]) + "\n" + ek.strip_system_xml(s["last_assistant"])


def specifics(text):
    return {m.strip("`").lower() for m in IDENT.findall(text)}


def auto_metrics(parsed, sample):
    if not parsed:
        return {"valid": False}
    text = " ".join(parsed.values())
    src = source_text(sample).lower()
    sp = specifics(text)
    return {"valid": True, "chars": len(text), "specifics": len(sp), "ungrounded": len([x for x in sp if x not in src]),
            "empty_fields": sum(1 for v in parsed.values() if not v or v.lower() in ("none", "n/a", "none.", "no")),
            "generic": sum(g in text.lower() for g in GENERIC)}


JUDGE = """You are checking notes written about part of a coding session. Judge only against the SOURCE TEXT.

SOURCE TEXT:
{source}

CANDIDATE NOTES (JSON):
{notes}

Score each from 1 to 5:
- faithfulness: 5 = every claim is supported by the source text; 3 = mostly supported with small inventions; 1 = invents or contradicts it.
- specificity: 5 = names the concrete files, commands, numbers and decisions from the source; 1 = generic filler.
- usefulness: 5 = someone could resume the work from these notes alone; 1 = useless or misleading.
List up to 5 claims in the notes that the source does not support or contradicts (quote them briefly); use an empty list if none.
Reply with only JSON: {{"faithfulness": n, "specificity": n, "usefulness": n, "unsupported": ["..."]}}"""


def judge_one(item):
    key, sample, parsed = item
    r = ek.run_haiku(JUDGE.format(source=source_text(sample)[:26000], notes=json.dumps(parsed, indent=1)))
    d = ek.parse_json_object(r["raw"])
    try:
        return "|".join(key), {k: d[k] for k in ("faithfulness", "specificity", "usefulness")} | {"unsupported": d.get("unsupported", [])}
    except (TypeError, KeyError):
        return "|".join(key), None


def load_results(out):
    res = {}
    for f in sorted(glob.glob(os.path.join(out, "results", "*__*.json"))):
        for r in json.load(open(f)):
            res[(r["model"], r["variant"], r["id"])] = r
    return res


def cmd_score(args):
    samples = {s["id"]: s for s in json.load(open(os.path.join(args.out, "samples.json")))}
    results = load_results(args.out)
    jpath = os.path.join(args.out, "results", "judge.json")
    judged = json.load(open(jpath)) if os.path.exists(jpath) else {}
    if args.judge:
        todo = [(k, samples[k[2]], r["parsed"]) for k, r in results.items() if r.get("parsed") and "|".join(k) not in judged]
        random.Random(7).shuffle(todo)  # the judge sees candidates in no particular order
        print(f"judging {len(todo)} candidates", file=sys.stderr)
        with ThreadPoolExecutor(4) as ex:
            for i, (k, v) in enumerate(ex.map(judge_one, todo), 1):
                if v:
                    judged[k] = v
                if i % 10 == 0:
                    json.dump(judged, open(jpath, "w"), indent=1)
        json.dump(judged, open(jpath, "w"), indent=1)

    ref = {k[2]: r["parsed"] for k, r in results.items() if k[0] == "haiku" and k[1] == "current" and r.get("parsed")}
    sims = {}
    for key, r in results.items():
        if r.get("parsed") and key[2] in ref and not (key[0] == "haiku" and key[1] == "current"):
            a, b = ek.embed([" ".join(r["parsed"].values()), " ".join(ref[key[2]].values())])
            sims[key] = ek.cosine(a, b)
    groups = {}
    for key, r in results.items():
        groups.setdefault((key[0], key[1]), []).append((key, r))
    print(f"{'model':<16}{'prompt':<9}{'valid':>7}{'secs':>6}{'faith':>6}{'spec':>5}{'useful':>7}{'F>=4':>6}{'ungr%':>6}{'empty':>6}{'gener':>6}{'sim':>6}")
    for (model, variant), items in sorted(groups.items(), key=lambda kv: (kv[0][0] != "haiku", kv[0])):
        met = [auto_metrics(r["parsed"], samples[k[2]]) for k, r in items]
        ok = [m for m in met if m["valid"]]
        j = [judged[k] for k in ("|".join(k) for k, _ in items) if k in judged]
        sp, ug = sum(m["specifics"] for m in ok), sum(m["ungrounded"] for m in ok)
        print(f"{model:<16}{variant:<9}{len(ok):>3}/{len(items):<3}{ek.mean([r['secs'] for _, r in items]):>6.1f}"
              f"{ek.mean([x['faithfulness'] for x in j]):>6.2f}{ek.mean([x['specificity'] for x in j]):>5.2f}{ek.mean([x['usefulness'] for x in j]):>7.2f}"
              f"{sum(x['faithfulness'] >= 4 for x in j):>3}/{len(j):<2}{(100 * ug / sp if sp else float('nan')):>6.0f}"
              f"{sum(m['empty_fields'] for m in ok) / max(1, len(ok)):>6.1f}{sum(m['generic'] for m in ok):>6}{ek.mean([sims.get(k) for k, _ in items]):>6.2f}")


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("samples", help="pick real turns from transcripts")
    s.add_argument("--transcripts", nargs="+", required=True, help="glob(s) of Claude Code transcript .jsonl files")
    s.add_argument("--out", required=True)
    s.add_argument("--stop", type=int, default=6)
    s.add_argument("--precompact", type=int, default=4)
    s.set_defaults(fn=cmd_samples)
    r = sub.add_parser("run", help="run one model over the samples")
    r.add_argument("--out", required=True)
    r.add_argument("--model", required=True, help="'haiku' or an Ollama model name")
    r.add_argument("--variant", choices=["current", "tailored", "both"], default="both")
    r.set_defaults(fn=cmd_run)
    c = sub.add_parser("score", help="print the comparison table")
    c.add_argument("--out", required=True)
    c.add_argument("--judge", action="store_true", help="have Haiku score every candidate that has not been judged yet")
    c.set_defaults(fn=cmd_score)
    args = p.parse_args(argv)
    args.fn(args)


if __name__ == "__main__":
    main()
