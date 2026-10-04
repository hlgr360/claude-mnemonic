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


if __name__ == "__main__":
    unittest.main()
