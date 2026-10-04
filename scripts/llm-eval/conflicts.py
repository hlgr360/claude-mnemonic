#!/usr/bin/env python3
"""Can a local model tell whether a newer observation replaces an older one?

claude-mnemonic has a complete but unused conflict system (observations can be marked superseded, and
superseded ones are deleted after three days). Nothing creates conflicts yet. A model that does must almost
never call two observations that both stay true "replacing", because that would delete a good memory. This
harness measures exactly that, on two kinds of pairs:

  * real pairs: each observation of one project of your own database with its closest older observations,
    labelled by Haiku as the reference (a reference, not the truth);
  * a built-in set of invented pairs with known answers (--gold), so recall and false positives are
    measured against ground truth too.

    conflicts.py pairs --db ~/.claude-mnemonic/claude-mnemonic.db --project NAME_abc123 --out DIR
    conflicts.py run   --out DIR --model haiku
    conflicts.py run   --out DIR --model granite3.3:8b
    conflicts.py score --out DIR

The database is opened read-only. Haiku calls use your Claude account.
"""
import argparse
import json
import os
import sqlite3
import sys
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import evalkit as ek  # noqa: E402

RELATIONS = ["duplicate", "supersedes", "contradicts", "related", "unrelated"]
GROUP = {"duplicate": "merge", "supersedes": "replace", "contradicts": "replace", "related": "keep", "unrelated": "keep"}
SCHEMA = {"type": "object", "properties": {
    "relation": {"type": "string", "enum": RELATIONS},
    "confidence": {"type": "string", "enum": ["low", "medium", "high"]},
    "reason": {"type": "string"}}, "required": ["relation", "confidence", "reason"]}

PROMPT = """Two notes were saved from a developer's coding sessions. OLDER was saved first, NEWER later. Decide how NEWER relates to OLDER.

Choose exactly one relation:
- duplicate: NEWER says essentially the same thing as OLDER and adds nothing new.
- supersedes: NEWER describes a change that makes OLDER out of date (a value, behaviour, decision or state that has since changed), so OLDER should no longer be trusted.
- contradicts: the two make claims that cannot both be true and nothing says one came after the other.
- related: they are about the same area, but both stay true and each adds something; neither replaces the other.
- unrelated: they are about different things.
Be conservative: choose supersedes or contradicts only when the notes clearly say so. Being similar, or about the same file or tool, is not enough.

OLDER (saved {older_date}):
Title: {older_title}
Summary: {older_subtitle}
Details: {older_narrative}

NEWER (saved {newer_date}):
Title: {newer_title}
Summary: {newer_subtitle}
Details: {newer_narrative}

Reply with only JSON: {{"relation": "...", "confidence": "low|medium|high", "reason": "one sentence"}}"""


def note(title, narrative, subtitle=""):
    return {"title": title, "subtitle": subtitle, "narrative": narrative, "date": "", "id": None}


# Invented pairs with known answers: (older, newer, relation). None of this is from a real project.
GOLD = [
    # a change that makes the older note out of date
    (note("API client timeout is 10 seconds", "The HTTP client in client.go uses a 10 second timeout for every call."),
     note("API client timeout raised to 30 seconds", "To stop spurious failures on large exports the HTTP client timeout in client.go was raised from 10 to 30 seconds."), "supersedes"),
    (note("CI uses Postgres 14", "The CI service container runs Postgres 14."),
     note("CI upgraded to Postgres 16", "The CI service container was upgraded from Postgres 14 to Postgres 16; 14 is no longer used anywhere in CI."), "supersedes"),
    (note("Cache key includes the lockfile hash", "The build cache key in ci.yml is built from the Go version and the hash of go.sum."),
     note("Cache key now uses only the Go version", "The build cache key was changed to use only the Go version, because the lockfile hash made every dependency bump miss the cache."), "supersedes"),
    (note("Sessions are stored in Redis", "We decided to keep user sessions in Redis for fast expiry."),
     note("Sessions moved from Redis to the database", "Sessions are now stored in the main database; the Redis session store was removed to cut one moving part."), "supersedes"),
    (note("Feature flag new_checkout is off in production", "The new_checkout flag is disabled for all users in production."),
     note("new_checkout flag enabled in production", "On March 3 the new_checkout flag was switched on for all users in production."), "supersedes"),
    (note("The worker listens on port 8080", "The background worker's HTTP server binds to port 8080."),
     note("Worker port moved to 9090", "The worker now listens on 9090 because 8080 clashed with the local proxy."), "supersedes"),
    (note("Linting runs only on changed files", "The lint job checks only the files changed in the pull request."),
     note("Linting now covers the whole repository", "The lint job was changed to run on the whole repository on every push."), "supersedes"),
    # claims that cannot both be true
    (note("The export job is safe to rerun", "According to the docs the export job is idempotent, so a failed run can simply be retried."),
     note("The export job duplicates rows when rerun", "In our test a second run of the export job duplicated 1,200 rows, so it is not safe to retry."), "contradicts"),
    (note("Config is loaded from ~/.app/config.yaml", "The application reads its configuration from ~/.app/config.yaml at startup."),
     note("Config is read from the XDG config directory", "The application reads $XDG_CONFIG_HOME/app/config.yaml and ignores any file in the home directory."), "contradicts"),
    (note("Migration 0042 adds the email index", "Migration 0042 creates the index on users.email."),
     note("Migration 0042 only renames a column", "Migration 0042 only renames users.mail to users.email; the index on it is created by migration 0043."), "contradicts"),
    # the same thing said again
    (note("Tests need Redis on localhost:6379", "The test suite needs a running Redis on localhost port 6379."),
     note("Start Redis before running the tests", "To run the test suite, start Redis locally on port 6379."), "duplicate"),
    (note("deploy.sh needs AWS_PROFILE", "The deploy script requires the AWS_PROFILE environment variable to be set."),
     note("Deploy fails without AWS_PROFILE", "deploy.sh fails unless the AWS_PROFILE environment variable is set."), "duplicate"),
    (note("The project builds with Go 1.22", "The project is built with Go 1.22."),
     note("Go 1.22 is the build version", "Go version 1.22 is what the project builds with."), "duplicate"),
    (note("Run make lint before committing", "Always run make lint before you commit."),
     note("Lint before you commit", "Before committing, run make lint."), "duplicate"),
    (note("Staging database is reset every Monday", "The staging database is reset every Monday morning."),
     note("Staging DB is wiped weekly", "Every Monday the staging DB gets wiped and recreated."), "duplicate"),
    # same area, both stay true
    (note("API client retries failed calls 3 times", "The API client retries a failed call up to 3 times."),
     note("API client backs off exponentially between retries", "Between retries the API client waits with exponential backoff."), "related"),
    (note("Auth tokens expire after 15 minutes", "Access tokens are valid for 15 minutes."),
     note("Refresh tokens live in an httpOnly cookie", "Refresh tokens are stored in an httpOnly cookie, not in local storage."), "related"),
    (note("The worker exposes /health", "The worker serves a /health endpoint that reports readiness."),
     note("The worker exposes /metrics", "The worker serves /metrics in Prometheus format."), "related"),
    (note("Cache key includes the Go version", "The build cache key contains the Go version."),
     note("Build caches are pruned after 7 days", "Entries in the build cache are removed after 7 days without use."), "related"),
    (note("Order IDs are UUIDs", "Order IDs are version 4 UUIDs."),
     note("Order IDs appear in URLs", "Orders are addressed as /orders/{id} in the web app."), "related"),
    (note("Logging uses zerolog", "The service logs through zerolog."),
     note("Log level comes from LOG_LEVEL", "The log level is set with the LOG_LEVEL environment variable."), "related"),
    # different subjects
    (note("The Dockerfile uses a distroless base image", "The runtime image is built on a distroless base."),
     note("Release notes come from commit messages", "Release notes are generated from conventional commit messages."), "unrelated"),
    (note("Tests need Redis on port 6379", "The test suite needs a running Redis."),
     note("The docs site is built with Vite", "The documentation website is a Vite project in docs/."), "unrelated"),
    (note("The events table uses a bigint primary key", "events.id is a bigint."),
     note("The mobile app targets iOS 17", "The mobile app's minimum deployment target is iOS 17."), "unrelated"),
]


def fmt_date(epoch):
    import time
    return time.strftime("%Y-%m-%d", time.gmtime(epoch / 1000)) if epoch else ""


def read_observations(db, project):
    con = sqlite3.connect(f"file:{os.path.abspath(db)}?mode=ro", uri=True)
    try:
        rows = con.execute("SELECT id, title, subtitle, narrative, created_at_epoch FROM observations "
                           "WHERE project = ? AND COALESCE(title,'') <> '' ORDER BY created_at_epoch, id", (project,)).fetchall()
    finally:
        con.close()
    return [{"id": r[0], "title": r[1] or "", "subtitle": r[2] or "", "narrative": r[3] or "", "date": fmt_date(r[4]), "epoch": r[4]} for r in rows]


def observation_text(o):
    return f"{o['title']}. {o['subtitle']}. {o['narrative'][:600]}"


def build_pairs(obs, vectors, min_sim=0.65, per=3, cap=90):
    """(newer, older) pairs: each observation with its most similar older ones, the most similar pairs first."""
    pairs = []
    for j in range(len(obs)):
        sims = sorted(((ek.cosine(vectors[j], vectors[i]), i) for i in range(j)), reverse=True)[:per]
        pairs += [(s, j, i) for s, i in sims if s >= min_sim]
    pairs.sort(reverse=True)
    return [{"id": f"R{k:03d}", "source": "db", "sim": round(s, 3), "older": {k2: v for k2, v in obs[i].items() if k2 != "epoch"},
             "newer": {k2: v for k2, v in obs[j].items() if k2 != "epoch"}, "gold": None} for k, (s, j, i) in enumerate(pairs[:cap], 1)]


def gold_pairs():
    out = []
    for k, (older, newer, rel) in enumerate(GOLD, 1):
        older, newer = dict(older, date="2026-01-10"), dict(newer, date="2026-02-20")
        out.append({"id": f"G{k:03d}", "source": "gold", "sim": None, "older": older, "newer": newer, "gold": rel})
    return out


def pair_prompt(pair):
    o, n = pair["older"], pair["newer"]
    return PROMPT.format(older_date=o["date"], older_title=o["title"], older_subtitle=o["subtitle"], older_narrative=o["narrative"][:1500],
                         newer_date=n["date"], newer_title=n["title"], newer_subtitle=n["subtitle"], newer_narrative=n["narrative"][:1500])


def judge_pair(model, pair):
    prompt = pair_prompt(pair)
    r = ek.run_haiku(prompt) if model == "haiku" else ek.run_ollama(model, "", prompt, schema=SCHEMA)
    d = ek.parse_json_object(r["raw"]) or {}
    rel = d.get("relation").strip().lower() if isinstance(d.get("relation"), str) else None
    rel = rel if rel in RELATIONS else None  # without a schema the model may capitalise it
    return {"id": pair["id"], "model": model, "relation": rel, "confidence": d.get("confidence").strip().lower() if isinstance(d.get("confidence"), str) else None, "reason": d.get("reason", ""), "secs": r["secs"], "raw": r["raw"][:400]}


# ------------------------------------------------------------------ commands
def cmd_pairs(args):
    obs = read_observations(args.db, args.project)
    if len(obs) < 2:
        sys.exit(f"project {args.project!r} has {len(obs)} observations")
    vectors = ek.embed([observation_text(o) for o in obs])
    pairs = build_pairs(obs, vectors, args.min_sim, args.per, args.max_pairs)
    if not args.no_gold:
        pairs += gold_pairs()
    os.makedirs(args.out, exist_ok=True)
    json.dump(pairs, open(os.path.join(args.out, "pairs.json"), "w"), indent=1)
    print(f"{len(obs)} observations -> {sum(p['source'] == 'db' for p in pairs)} real pairs, {sum(p['source'] == 'gold' for p in pairs)} gold pairs")


def results_path(out, model):
    return os.path.join(out, "results", f"conflicts__{ek.safe_name(model)}.json")


def cmd_run(args):
    pairs = json.load(open(os.path.join(args.out, "pairs.json")))
    os.makedirs(os.path.join(args.out, "results"), exist_ok=True)
    with ThreadPoolExecutor(4 if args.model == "haiku" else 1) as ex:
        out = list(ex.map(lambda p: judge_pair(args.model, p), pairs))
    json.dump(out, open(results_path(args.out, args.model), "w"), indent=1)
    print(args.model, "done:", sum(1 for r in out if r["relation"]), "of", len(out), "answered")


def truth(pair, haiku):
    """The label to score against: the known answer for gold pairs, Haiku's for real ones."""
    return pair["gold"] if pair["gold"] else (haiku.get(pair["id"]) or {}).get("relation")


def summarise(pairs, answers, haiku, source):
    rows = [(p, answers.get(p["id"])) for p in pairs if p["source"] == source]
    rows = [(p, a, truth(p, haiku)) for p, a in rows if truth(p, haiku)]
    n = len(rows)
    if not n:
        return None
    ans = [(a["relation"] if a and a["relation"] else None, t, (a or {}).get("confidence")) for _, a, t in rows]
    exact = sum(1 for r, t, _ in ans if r == t)
    coarse = sum(1 for r, t, _ in ans if r and GROUP[r] == GROUP[t])
    # Replacing notes that both stay true (related, unrelated) deletes a good memory. Replacing a true duplicate
    # loses nothing, because the newer note says the same thing, so it is counted on its own.
    must_keep = [(r, c) for r, t, c in ans if t in ("related", "unrelated")]
    duplicates = [(r, c) for r, t, c in ans if t == "duplicate"]
    must_replace = [(r, c) for r, t, c in ans if GROUP[t] == "replace"]
    false_replace = [(r, c) for r, c in must_keep if r and GROUP[r] == "replace"]
    caught = [(r, c) for r, c in must_replace if r and GROUP[r] == "replace"]
    high = [(r, t) for r, t, c in ans if r and GROUP[r] == "replace" and c == "high"]
    return {"n": n, "exact": exact, "coarse": coarse, "unanswered": sum(1 for r, _, _ in ans if r is None),
            "not_replace_total": len(must_keep), "false_replace": len(false_replace),
            "dup_total": len(duplicates), "dup_replaced": sum(1 for r, c in duplicates if r and GROUP[r] == "replace"),
            "replace_total": len(must_replace), "caught": len(caught),
            "high_replace": len(high), "high_replace_wrong": sum(1 for r, t in high if t in ("related", "unrelated"))}


def cmd_score(args):
    pairs = json.load(open(os.path.join(args.out, "pairs.json")))
    by_model = {}
    for f in sorted(os.listdir(os.path.join(args.out, "results"))):
        if f.startswith("conflicts__"):
            rs = json.load(open(os.path.join(args.out, "results", f)))
            by_model[rs[0]["model"]] = {r["id"]: r for r in rs}
    haiku = by_model.get("haiku", {})
    print("harmful replace = supersedes/contradicts on notes that both stay true (related, unrelated): that deletes a good memory.")
    print("dup replaced = a true duplicate called supersedes/contradicts: harmless, the newer note says the same thing.")
    for source, title in (("gold", "known answers"), ("db", "real pairs (reference: Haiku)")):
        if not any(p["source"] == source for p in pairs):
            continue
        print(f"\n== {title}")
        print(f"{'model':<18}{'n':>4}{'exact':>7}{'3-way':>7}{'harmful replace':>17}{'dup replaced':>14}{'replace caught':>16}{'high-conf harmful':>19}{'no answer':>10}")
        for model in sorted(by_model, key=lambda m: (m != "haiku", m)):
            if model == "haiku" and source == "db":
                continue  # Haiku is the reference there
            s = summarise(pairs, by_model[model], haiku, source)
            if s:
                print(f"{model:<18}{s['n']:>4}{s['exact']:>7}{s['coarse']:>7}{str(s['false_replace']) + '/' + str(s['not_replace_total']):>17}"
                      f"{str(s['dup_replaced']) + '/' + str(s['dup_total']):>14}{str(s['caught']) + '/' + str(s['replace_total']):>16}"
                      f"{str(s['high_replace_wrong']) + '/' + str(s['high_replace']):>19}{s['unanswered']:>10}")
    if haiku:
        dist = {}
        for p in pairs:
            if p["source"] == "db" and haiku.get(p["id"], {}).get("relation"):
                dist[haiku[p["id"]]["relation"]] = dist.get(haiku[p["id"]]["relation"], 0) + 1
        print("\nHaiku's labels on the real pairs:", dist)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("pairs", help="build the pairs file")
    s.add_argument("--db", required=True)
    s.add_argument("--project", required=True, help="one project id; only its observations are read")
    s.add_argument("--out", required=True)
    s.add_argument("--min-sim", type=float, default=0.65)
    s.add_argument("--per", type=int, default=3)
    s.add_argument("--max-pairs", type=int, default=90)
    s.add_argument("--no-gold", action="store_true", help="leave out the built-in pairs with known answers")
    s.set_defaults(fn=cmd_pairs)
    r = sub.add_parser("run", help="have one model judge every pair")
    r.add_argument("--out", required=True)
    r.add_argument("--model", required=True, help="'haiku' or an Ollama model name")
    r.set_defaults(fn=cmd_run)
    c = sub.add_parser("score", help="print the comparison")
    c.add_argument("--out", required=True)
    c.set_defaults(fn=cmd_score)
    args = p.parse_args(argv)
    args.fn(args)


if __name__ == "__main__":
    main()
