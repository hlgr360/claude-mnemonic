#!/usr/bin/env python3
"""Tests for scripts/llm-eval. Run: python3 -m unittest scripts/test_llm_eval.py -v"""
import importlib.util
import json
import os
import sqlite3
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "llm-eval"))


def load(name):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, "llm-eval", name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


ek = load("evalkit")
sm = load("summaries")
cf = load("conflicts")
br = load("briefs")


class Kit(unittest.TestCase):
    def test_the_workers_prompt_can_still_be_read_from_source(self):
        system, prompt = ek.current_prompt({"kind": "stop", "last_assistant": "I edited a.go and added a test."})
        self.assertIn("memory extraction agent", system)
        self.assertIn("PROGRESS SUMMARY CHECKPOINT", prompt)
        self.assertIn("I edited a.go", prompt)
        self.assertIn("<next_steps>", prompt)
        _, with_conv = ek.current_prompt({"kind": "precompact", "last_assistant": "x", "conversation": "User: hi"})
        self.assertIn("about to be compacted", with_conv)
        self.assertIn("User: hi", with_conv)

    def test_long_replies_are_cut_like_the_worker_does(self):
        _, prompt = ek.current_prompt({"kind": "stop", "last_assistant": "a" * 5000})
        self.assertIn("(truncated)", prompt)

    def test_parsers(self):
        xml = "noise <summary><request>r</request><learned>l &amp; m</learned></summary> tail"
        self.assertEqual(ek.parse_xml_summary(xml)["request"], "r")
        self.assertEqual(ek.parse_xml_summary(xml)["notes"], "")
        self.assertIsNone(ek.parse_xml_summary("no tags"))
        self.assertEqual(ek.parse_json_summary('x {"request": "r", "notes": null} y')["notes"], "")
        self.assertIsNone(ek.parse_json_summary("{broken"))
        self.assertIsNone(ek.parse_json_summary("[1]"))
        self.assertEqual(ek.parse_json_object('```json\n{"a": 1}\n```'), {"a": 1})

    def test_helpers(self):
        self.assertAlmostEqual(ek.cosine([1, 0], [1, 0]), 1.0)
        self.assertAlmostEqual(ek.cosine([1, 0], [0, 1]), 0.0)
        self.assertEqual(ek.cosine([0, 0], [1, 1]), 0.0)
        self.assertEqual(ek.safe_name("qllama/bge:latest"), "qllama_bge_latest")
        self.assertEqual(ek.mean([1, None, 3]), 2)
        self.assertEqual(ek.strip_system_xml("a<system-reminder>x\ny</system-reminder> b"), "a b")

    def test_ollama_url(self):
        old = os.environ.get("OLLAMA_HOST")
        try:
            os.environ["OLLAMA_HOST"] = "box:1234/"
            self.assertEqual(ek.ollama_url(), "http://box:1234")
            os.environ["OLLAMA_HOST"] = ""
            self.assertEqual(ek.ollama_url(), "http://localhost:11434")
        finally:
            if old is None:
                os.environ.pop("OLLAMA_HOST", None)
            else:
                os.environ["OLLAMA_HOST"] = old

    def test_the_tailored_prompt_does_not_contain_the_extraction_system_prompt(self):
        s = {"kind": "stop", "last_assistant": "I fixed foo.go"}
        self.assertNotIn("memory extraction", ek.SYSTEM_TAILORED)
        self.assertIn("never invent", ek.SYSTEM_TAILORED)
        self.assertIn("I fixed foo.go", ek.tailored_prompt(s))
        self.assertEqual(set(ek.EXAMPLE_OUT), set(ek.FIELDS))


def write_transcript(path, turns):
    with open(path, "w", encoding="utf-8") as f:
        for kind, content in turns:
            f.write(json.dumps({"type": kind, "message": {"role": kind, "content": content}}) + "\n")
        f.write("not json\n")


REPLY = "I edited internal/foo.go and implemented the retry, then updated bar.py and added tests in baz_test.go. " * 6


class Summaries(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(self.dir, ignore_errors=True))

    def test_read_turns_keeps_text_only_and_removes_reminders(self):
        path = os.path.join(self.dir, "t.jsonl")
        write_transcript(path, [
            ("user", "hello <system-reminder>secret</system-reminder>"),
            ("assistant", [{"type": "text", "text": "hi"}, {"type": "tool_use", "name": "Bash"}]),
            ("user", [{"type": "tool_result", "content": "x"}]),
            ("system", "ignored"),
        ])
        self.assertEqual(sm.read_turns(path), [("user", "hello"), ("assistant", "hi")])

    def test_good_reply(self):
        self.assertTrue(sm.good_reply(REPLY))
        self.assertFalse(sm.good_reply("short"))
        self.assertFalse(sm.good_reply("x" * 800), "no signs of work")
        self.assertFalse(sm.good_reply(REPLY + " memory extraction agent"))
        self.assertFalse(sm.good_reply(REPLY + " <task-notification>"))
        self.assertFalse(sm.good_reply(REPLY * 30), "too long")

    def test_excerpt_is_oldest_first_cut_and_bounded(self):
        msgs = [("user", "first"), ("assistant", "b" * 3000), ("user", "last")]
        e = sm.excerpt(msgs, 2)
        self.assertTrue(e.startswith("User: first"))
        self.assertTrue(e.endswith("User: last"))
        self.assertIn(" …", e)
        self.assertLessEqual(len(sm.excerpt(msgs, 2, budget=100)), 100)
        self.assertEqual(sm.excerpt(msgs, 0), "User: first")

    def test_spread(self):
        self.assertEqual(sm.spread([1, 2, 3], 5), [1, 2, 3])
        self.assertEqual(len(sm.spread(list(range(20)), 4)), 4)
        self.assertEqual(sm.spread(list(range(20)), 4)[0], 0)

    def test_build_samples(self):
        turns = []
        for i in range(10):
            turns += [("user", f"please do step {i}"), ("assistant", f"step {i}: " + REPLY)]
        path = os.path.join(self.dir, "abcdef12-long.jsonl")
        write_transcript(path, turns)
        samples = sm.build_samples([path], n_stop=2, n_pre=2)
        self.assertEqual([s["kind"] for s in samples], ["stop", "stop", "precompact", "precompact"])
        self.assertEqual([s["id"] for s in samples], ["S01-stop", "S02-stop", "S03-precompact", "S04-precompact"])
        self.assertTrue(all(s["session"] == "abcdef12" for s in samples))
        self.assertTrue(samples[2]["conversation"].startswith(("User:", "Assistant:")))
        self.assertEqual(sm.build_samples([os.path.join(self.dir, "missing.jsonl")]), [])

    def test_mechanical_checks(self):
        sample = {"kind": "stop", "last_assistant": "I changed internal/foo.go for issue #12 and bumped it to 1.2.3.", "conversation": ""}
        grounded = {f: "" for f in ek.FIELDS} | {"completed": "Changed internal/foo.go (#12), version 1.2.3."}
        m = sm.auto_metrics(grounded, sample)
        self.assertTrue(m["valid"])
        self.assertEqual(m["ungrounded"], 0)
        self.assertEqual(m["empty_fields"], 5)
        invented = {f: "n/a" for f in ek.FIELDS} | {"completed": "Edited bar/baz.py and fixed #99; see Progress Summary Checkpoint."}
        m = sm.auto_metrics(invented, sample)
        self.assertGreaterEqual(m["ungrounded"], 2, "a file and an issue number that the source never mentions")
        self.assertEqual(m["generic"], 1)
        self.assertFalse(sm.auto_metrics(None, sample)["valid"])

    def test_result_paths_are_filesystem_safe(self):
        self.assertTrue(sm.result_path("/o", "qllama/bge:latest", "tailored").endswith("qllama_bge_latest__tailored.json"))


class Conflicts(unittest.TestCase):
    def test_the_gold_set_is_well_formed(self):
        pairs = cf.gold_pairs()
        self.assertEqual(len(pairs), 24)
        self.assertEqual(len({p["id"] for p in pairs}), 24)
        self.assertTrue(all(p["gold"] in cf.RELATIONS for p in pairs))
        kinds = {r: sum(p["gold"] == r for p in pairs) for r in cf.RELATIONS}
        self.assertTrue(all(v >= 3 for v in kinds.values()), kinds)
        self.assertGreater(kinds["related"], 0)
        for p in pairs:
            self.assertNotEqual(p["older"]["title"], p["newer"]["title"])
            self.assertTrue(p["older"]["narrative"] and p["newer"]["narrative"])
            self.assertEqual((p["older"]["date"], p["newer"]["date"]), ("2026-01-10", "2026-02-20"))

    def test_relations_are_grouped(self):
        self.assertEqual({r: cf.GROUP[r] for r in cf.RELATIONS},
                         {"duplicate": "merge", "supersedes": "replace", "contradicts": "replace", "related": "keep", "unrelated": "keep"})
        self.assertEqual(cf.SCHEMA["properties"]["relation"]["enum"], cf.RELATIONS)

    def make_db(self):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(d, ignore_errors=True))
        path = os.path.join(d, "m.db")
        con = sqlite3.connect(path)
        con.execute("CREATE TABLE observations (id INTEGER PRIMARY KEY, project TEXT, title TEXT, subtitle TEXT, narrative TEXT, created_at_epoch INTEGER)")
        rows = [(1, "p_1", "Old", "s", "narrative one", 1700000000000), (2, "p_1", "Newer", None, "narrative two", 1700000100000),
                (3, "p_1", "", "no title", "x", 1700000200000), (4, "other_2", "Other project", "s", "must not be read", 1700000300000)]
        con.executemany("INSERT INTO observations VALUES (?,?,?,?,?,?)", rows)
        con.commit()
        con.close()
        return path

    def test_read_observations_is_one_project_and_read_only(self):
        path = self.make_db()
        obs = cf.read_observations(path, "p_1")
        self.assertEqual([o["id"] for o in obs], [1, 2], "other projects and untitled rows are left out")
        self.assertEqual(obs[1]["subtitle"], "", "NULL becomes an empty string")
        self.assertEqual(obs[0]["date"], "2023-11-14")
        with self.assertRaises(sqlite3.OperationalError):
            con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
            con.execute("DELETE FROM observations")

    def test_build_pairs_pairs_each_with_older_similar_ones(self):
        obs = [{"id": i, "title": f"t{i}", "subtitle": "", "narrative": "", "date": "", "epoch": i} for i in range(4)]
        vectors = [[1, 0], [0.99, 0.1], [0, 1], [0.98, 0.2]]
        pairs = cf.build_pairs(obs, vectors, min_sim=0.9, per=2, cap=10)
        found = {(p["newer"]["id"], p["older"]["id"]) for p in pairs}
        self.assertEqual(found, {(1, 0), (3, 0), (3, 1)}, "only older observations, only above the threshold")
        self.assertTrue(all(p["gold"] is None and p["source"] == "db" for p in pairs))
        self.assertEqual(pairs[0]["sim"], max(p["sim"] for p in pairs), "the most similar pair comes first")
        self.assertEqual(len(cf.build_pairs(obs, vectors, min_sim=0.0, per=3, cap=2)), 2)
        self.assertNotIn("epoch", pairs[0]["older"])

    def test_a_model_without_structured_output_is_judged_on_its_plain_answer(self):
        """gemma4's MLX build in Ollama answers 501 to a JSON schema; LLM_EVAL_NO_FORMAT=1 sends none."""
        import io
        from unittest import mock

        sent = []

        class Reply(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

        def fake_urlopen(req, timeout=None):
            sent.append(json.loads(req.data))
            return Reply(json.dumps({"message": {"content": "```json\n{\"relation\": \"Supersedes\", \"confidence\": \"High\", \"reason\": \"changed\"}\n```"},
                                     "done_reason": "stop", "eval_count": 5, "eval_duration": 10**9}).encode())

        pair = cf.gold_pairs()[0]
        kit = cf.ek  # the module conflicts.py itself uses (the test's own `ek` is a separate load)
        with mock.patch.object(kit.urllib.request, "urlopen", fake_urlopen):
            with mock.patch.object(kit, "NO_FORMAT", False):
                cf.judge_pair("m", pair)
            with mock.patch.object(kit, "NO_FORMAT", True):
                got = cf.judge_pair("m", pair)
        self.assertEqual(sent[0]["format"], cf.SCHEMA, "by default the schema is sent")
        self.assertNotIn("format", sent[1], "in no-format mode it is not")
        self.assertEqual((got["relation"], got["confidence"]), ("supersedes", "high"), "a fenced, capitalised answer is accepted")

    def test_an_unknown_relation_is_not_an_answer(self):
        from unittest import mock

        with mock.patch.object(cf.ek, "run_ollama", return_value={"raw": '{"relation": "Increase", "confidence": "High", "reason": "x"}', "secs": 1}):
            got = cf.judge_pair("m", cf.gold_pairs()[0])
        self.assertIsNone(got["relation"])

    def test_pair_prompt(self):
        p = cf.gold_pairs()[0]
        prompt = cf.pair_prompt(p)
        self.assertIn("API client timeout is 10 seconds", prompt)
        self.assertIn("raised from 10 to 30", prompt)
        self.assertIn("saved 2026-02-20", prompt)
        self.assertIn("Be conservative", prompt)

    def test_replacing_a_true_duplicate_is_not_counted_as_harmful(self):
        pairs = [{"id": "G1", "source": "gold", "gold": "duplicate"}]
        s = cf.summarise(pairs, {"G1": {"relation": "supersedes", "confidence": "high"}}, {}, "gold")
        self.assertEqual((s["false_replace"], s["not_replace_total"]), (0, 0))
        self.assertEqual((s["dup_replaced"], s["dup_total"]), (1, 1))
        self.assertEqual(s["high_replace_wrong"], 0)

    def test_truth_prefers_the_known_answer(self):
        self.assertEqual(cf.truth({"id": "G1", "gold": "related"}, {"G1": {"relation": "supersedes"}}), "related")
        self.assertEqual(cf.truth({"id": "R1", "gold": None}, {"R1": {"relation": "duplicate"}}), "duplicate")
        self.assertIsNone(cf.truth({"id": "R2", "gold": None}, {}))

    def test_scoring_counts_the_dangerous_mistakes(self):
        pairs = [{"id": f"G{i}", "source": "gold", "gold": g} for i, g in enumerate(["related", "unrelated", "supersedes", "contradicts", "duplicate"])]
        answers = {
            "G0": {"relation": "supersedes", "confidence": "high"},     # wrong: would delete a good memory, and sure of it
            "G1": {"relation": "unrelated", "confidence": "low"},       # right
            "G2": {"relation": "contradicts", "confidence": "medium"},  # right in the coarse sense (replace)
            "G3": {"relation": "related", "confidence": "high"},        # missed
            "G4": {"relation": None, "confidence": None},               # no usable answer
        }
        s = cf.summarise(pairs, answers, {}, "gold")
        self.assertEqual(s["n"], 5)
        self.assertEqual(s["exact"], 1)
        self.assertEqual(s["coarse"], 2)
        self.assertEqual((s["false_replace"], s["not_replace_total"]), (1, 2), "only related and unrelated notes must never be replaced")
        self.assertEqual((s["dup_replaced"], s["dup_total"]), (0, 1))
        self.assertEqual((s["caught"], s["replace_total"]), (1, 2))
        self.assertEqual((s["high_replace_wrong"], s["high_replace"]), (1, 1))
        self.assertEqual(s["unanswered"], 1)
        self.assertIsNone(cf.summarise(pairs, answers, {}, "db"))



class Briefs(unittest.TestCase):
    def make_db(self):
        path = os.path.join(tempfile.mkdtemp(), "t.db")
        con = sqlite3.connect(path)
        con.executescript("""
            CREATE TABLE observations (id INTEGER PRIMARY KEY, project TEXT, type TEXT, title TEXT, subtitle TEXT, narrative TEXT,
                scope TEXT, is_archived INTEGER, is_superseded INTEGER, importance_score REAL, created_at_epoch INTEGER);
            CREATE TABLE session_summaries (id INTEGER PRIMARY KEY, project TEXT, sdk_session_id TEXT, request TEXT, notes TEXT,
                completed TEXT, learned TEXT, created_at_epoch INTEGER);
            CREATE TABLE project_aliases (alias TEXT PRIMARY KEY, canonical TEXT);
        """)
        rows = [  # id, project, type, title, sub, narrative, scope, archived, superseded, importance, epoch
            (1, "app_aaaaaa", "decision", "Old decision", None, "We chose A.", "project", 0, 0, 1.0, 1_700_000_000_000),
            (2, "app_aaaaaa", "bugfix", "Fixed the cache", "sub", "Cache lifetime was 60 minutes.", None, 0, 0, 2.0, 1_700_100_000_000),
            (3, "app_aaaaaa", "feature", "Archived", None, "x", "project", 1, 0, 1.0, 1_700_200_000_000),
            (4, "app_aaaaaa", "feature", "Superseded", None, "x", "project", 0, 1, 1.0, 1_700_300_000_000),
            (5, "app_aaaaaa", "feature", "A global one", None, "x", "global", 0, 0, 1.0, 1_700_400_000_000),
            (6, "app_aaaaaa", "discovery", "Newest", None, "Latest state.", "project", 0, 0, 1.0, 1_700_500_000_000),
            (7, "other_bbbbbb", "discovery", "Other project", None, "y", "project", 0, 0, 1.0, 1_700_600_000_000),
        ]
        con.executemany("INSERT INTO observations VALUES (?,?,?,?,?,?,?,?,?,?,?)", rows)
        con.executemany("INSERT INTO session_summaries VALUES (?,?,?,?,?,?,?,?)", [
            (1, "app_aaaaaa", "thread-release", "Release work", "ship it", "tests written", "names not ids", 1_700_700_000_000),
            (2, "app_aaaaaa", "thread-docs", "Docs", "", "", "", 1_700_800_000_000),
            (3, "app_aaaaaa", "claude-session-1", "Not a thread note", "", "", "", 1_700_900_000_000),
            (4, "app_aaaaaa", "brief-app_aaaaaa", "Project brief", "", "", "", 1_701_000_000_000),
        ])
        con.executemany("INSERT INTO project_aliases VALUES (?, ?)", [("old_cccccc", "app_aaaaaa")])
        con.commit()
        con.close()
        return path

    def sample(self, **extra):
        s = br.read_project(self.make_db(), "app_aaaaaa")
        s.update({"sid": "B01", "as_of": "2023-11-20"})
        s.update(extra)
        return s

    def test_the_limits_and_the_system_prompt_agree_with_the_workers_source(self):
        brief = open(os.path.join(ek.REPO, "internal", "worker", "sdk", "brief.go"), encoding="utf-8").read()
        worker = open(os.path.join(ek.REPO, "internal", "worker", "brief.go"), encoding="utf-8").read()
        const = lambda src, name: int(eval(__import__("re").search(rf"{name}\s*=\s*([0-9* ]+)", src).group(1)))  # noqa: S307 (digits and * only)
        self.assertEqual(const(brief, "briefNarrativeChars"), br.NARRATIVE_CHARS)
        self.assertEqual(const(brief, "briefTitleChars"), br.TITLE_CHARS)
        self.assertEqual(const(brief, "briefThreadChars"), br.THREAD_CHARS)
        self.assertEqual(const(brief, "briefMaxPromptBytes"), br.MAX_PROMPT_BYTES)
        self.assertEqual(const(worker, "briefObservationLimit"), br.OBSERVATION_LIMIT)
        self.assertEqual(const(worker, "briefThreadLimit"), br.THREAD_LIMIT)
        system = br.worker_system_prompt()
        for section in br.SECTIONS:
            self.assertIn(f'"## {section}"', system)
        self.assertIn("At most 300 words", system)

    def test_the_input_is_what_the_worker_reads(self):
        s = self.sample()
        self.assertEqual([o["id"] for o in s["observations"]], [1, 2, 6], "live, project-scoped notes only, oldest first")
        self.assertEqual(s["total"], 3)
        self.assertEqual(s["name"], "app")
        self.assertEqual([t["request"] for t in s["threads"]], ["Docs", "Release work"], "thread notes only, the latest first")
        self.assertEqual(s["threads"][1]["goal"], "ship it")
        self.assertEqual(s["threads"][1]["progress"], "tests written")

    def test_the_most_important_notes_are_kept_when_there_are_too_many(self):
        path = self.make_db()
        con = sqlite3.connect(path)
        for i in range(10, 10 + br.OBSERVATION_LIMIT + 5):
            con.execute("INSERT INTO observations VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                        (i, "big_dddddd", "discovery", f"n{i}", None, "x", "project", 0, 0, 1.0 if i != 12 else 9.0, 1_700_000_000_000 + i))
        con.commit()
        con.close()
        s = br.read_project(path, "big_dddddd")
        self.assertEqual(len(s["observations"]), br.OBSERVATION_LIMIT)
        self.assertEqual(s["total"], br.OBSERVATION_LIMIT + 5)
        self.assertIn(12, [o["id"] for o in s["observations"]], "the important note survives the cut")
        self.assertEqual([o["id"] for o in s["observations"]], sorted(o["id"] for o in s["observations"]), "and they read oldest first")

    def test_global_notes_are_only_used_when_asked_for(self):
        path = self.make_db()
        self.assertEqual([o["id"] for o in br.read_project(path, "app_aaaaaa", include_global=True)["observations"]], [1, 2, 5, 6])
        self.assertNotIn(5, [o["id"] for o in br.read_project(path, "app_aaaaaa")["observations"]])

    def test_the_database_is_opened_read_only(self):
        path = self.make_db()
        br.read_project(path, "app_aaaaaa")
        con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
        with self.assertRaises(sqlite3.OperationalError):
            con.execute("DELETE FROM observations")

    def test_largest_projects_skips_aliases_and_small_ones(self):
        path = self.make_db()
        con = sqlite3.connect(path)
        con.execute("INSERT INTO observations VALUES (20,'old_cccccc','discovery','t',NULL,'x','project',0,0,1.0,1)")
        con.commit()
        con.close()
        self.assertEqual(br.largest_projects(path, 5, 1)[0], "app_aaaaaa", "the largest comes first")
        self.assertNotIn("old_cccccc", br.largest_projects(path, 5, 1), "an alias is not a project of its own")
        self.assertEqual(br.largest_projects(path, 5, 2), ["app_aaaaaa"], "a project below the minimum is left out")

    def test_the_prompt_is_the_workers_request(self):
        system, prompt, used = br.build_prompt(self.sample())
        self.assertEqual(system, br.worker_system_prompt())
        self.assertTrue(prompt.startswith("PROJECT BRIEF REQUEST\nAS OF: 2023-11-20\nPROJECT: app\n\n"))
        self.assertIn("CURRENT WORK (the developer's own checkpoint notes, most recent first):\n- Docs (updated 2023-11-", prompt)
        self.assertIn("- Release work (updated 2023-11-", prompt)
        self.assertIn("; goal: ship it; progress: tests written; decisions: names not ids\n", prompt)
        self.assertIn("OBSERVATIONS (3 of 3, oldest first):\n\n[#1] (decision, 2023-11-14) Old decision\n  We chose A.\n\n", prompt)
        self.assertIn("[#2] (bugfix, 2023-11-16) Fixed the cache\n  sub\n  Cache lifetime was 60 minutes.\n", prompt)
        self.assertEqual([o["id"] for o in used], [1, 2, 6])

    def test_text_is_clipped_like_the_worker_does(self):
        self.assertEqual(br.clip_runes("a  b\n c", 10), "a b c", "whitespace is collapsed")
        self.assertEqual(br.clip_runes("x" * 20, 10), "x" * 10 + "…")
        s = self.sample()
        s["observations"][0]["narrative"] = "n" * 1000
        _, prompt, _ = br.build_prompt(s)
        self.assertIn("n" * br.NARRATIVE_CHARS + "…", prompt)
        self.assertNotIn("n" * (br.NARRATIVE_CHARS + 1), prompt)

    def test_a_too_large_request_drops_the_oldest_notes(self):
        s = self.sample()
        s["observations"] = [dict(s["observations"][0], id=i, narrative="word " * 90, epoch=1_700_000_000_000 + i) for i in range(1, 400)]
        s["total"] = 399
        _, prompt, used = br.build_prompt(s)
        self.assertLessEqual(len(prompt.encode()) + len(br.worker_system_prompt().encode()), br.MAX_PROMPT_BYTES)
        self.assertLess(len(used), 399)
        self.assertEqual(used[-1]["id"], 399, "the newest notes are kept")
        self.assertIn(f"OBSERVATIONS ({len(used)} of 399, oldest first)", prompt)

    def test_cleaning_matches_what_the_worker_stores(self):
        raw = ("```markdown\n## What this is\nA tool [#1] with a made-up claim [#999] and a mix [#2, #998]. Mail dev@example.org.\n"
               "<private>secret</private>\n## Open items\n- invented\n## Conventions and gotchas\nKeep it small [#6].\n```")
        got = br.clean_body(raw, {1, 2, 6})
        self.assertTrue(got.startswith("## What this is"), "no code fence")
        self.assertIn("A tool [#1] with a made-up claim and a mix [#2].", got, "an unknown citation goes, with the space before it")
        self.assertNotIn("dev@example.org", got)
        self.assertIn("[email removed]", got)
        self.assertNotIn("secret", got)
        self.assertNotIn("Open items", got)
        self.assertNotIn("invented", got)
        self.assertIn("## Conventions and gotchas\nKeep it small [#6].", got)

    def test_the_mechanical_checks(self):
        s = self.sample()
        good = ("## What this is\nA service [#1].\n## Current state\nWorks [#2, #6]. Port 8080 in `config.yml`.\n"
                "## Key decisions (and why)\nChose A [#1].\n## Conventions and gotchas\nNone.")
        m = br.metrics({"raw": good, "known": [1, 2, 6]}, s)
        self.assertTrue(m["sections"] and not m["unwanted"] and not m["over_limit"])
        self.assertEqual((m["cites"], m["invented"], m["distinct"], m["emails"]), (4, 0, 3, 0))
        self.assertGreaterEqual(m["ungrounded"], 2, "8080 and config.yml are not in the notes")
        bad = "## Current state\nx [#77] a@b.co\n## What this is\ny\n## Open items\n- z"
        m = br.metrics({"raw": bad, "known": [1, 2, 6]}, s)
        self.assertFalse(m["sections"], "sections out of order or missing")
        self.assertTrue(m["unwanted"])
        self.assertEqual((m["invented"], m["emails"]), (1, 1))
        long = br.metrics({"raw": "## What this is\n" + "word " * 400, "known": []}, s)
        self.assertTrue(long["over_limit"])
        self.assertFalse(br.metrics({"raw": "  ", "known": []}, s)["ok"])

    def test_specifics(self):
        self.assertEqual(br.specifics("Port 8080, file main.go and `Foo` plus 7."), {"8080", "main.go", "foo"})

    def test_result_paths_and_scoring_table(self):
        import io
        import contextlib

        out = tempfile.mkdtemp()
        s = self.sample()
        json.dump([s], open(os.path.join(out, "samples.json"), "w"))
        self.assertTrue(br.result_path(out, "gemma4:e4b-mlx").endswith("briefs__gemma4_e4b-mlx.json"))
        os.makedirs(os.path.join(out, "results"))
        good = "## What this is\nA [#1].\n## Current state\nB [#2].\n## Key decisions (and why)\nC.\n## Conventions and gotchas\nD."
        json.dump([{"sid": "B01", "model": "haiku", "raw": good, "known": [1, 2, 6], "secs": 5.0}], open(br.result_path(out, "haiku"), "w"))
        json.dump({"haiku|B01": {"faithfulness": 5, "currency": 4, "usefulness": 5, "concision": 5, "unsupported": []}},
                  open(os.path.join(out, "results", "briefs_judge.json"), "w"))
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            br.cmd_score(type("A", (), {"out": out, "judge": False})())
        table = buf.getvalue()
        self.assertIn("haiku", table)
        self.assertIn("1/1", table, "one valid brief of one")
        self.assertIn("5.00", table, "the judged faithfulness")


if __name__ == "__main__":
    unittest.main()
