"""Shared pieces of the local-LLM evaluation harness: backends, prompts, parsing, small helpers.

Standard library only. Talks to Ollama (OLLAMA_HOST, default localhost:11434) and, for the reference
answers, to the Claude CLI (`claude --print --model haiku`), the same way the worker does.
"""
import json
import math
import os
import re
import subprocess
import time
import urllib.request

REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
FIELDS = ["request", "investigated", "learned", "completed", "next_steps", "notes"]
EMBED_MODEL = "qllama/bge-small-en-v1.5"


def ollama_url():
    raw = os.environ.get("OLLAMA_HOST", "").strip() or "http://localhost:11434"
    if "://" not in raw:
        raw = "http://" + raw
    return raw.rstrip("/")


def safe_name(model):
    return model.replace(":", "_").replace("/", "_")


def mean(xs):
    xs = [x for x in xs if x is not None]
    return sum(xs) / len(xs) if xs else float("nan")


def strip_system_xml(text):
    return re.sub(r"<system-reminder>.*?</system-reminder>", "", text or "", flags=re.S).strip()


# ---------------------------------------------------------------- the worker's current summary prompt, read from its source
def _worker_prompt_parts():
    proc = open(os.path.join(REPO, "internal", "worker", "sdk", "processor.go"), encoding="utf-8").read()
    prompts = open(os.path.join(REPO, "internal", "worker", "sdk", "prompts.go"), encoding="utf-8").read()
    fn = prompts[prompts.index("func BuildSummaryPrompt"):]
    system = re.search(r"const systemPrompt = `(.*?)`", proc, re.S).group(1)
    intro = re.search(r'sb.WriteString\("(Write progress notes.*?)"\)\n', fn, re.S).group(1).encode().decode("unicode_escape")
    tail = re.search(r"sb.WriteString\(`(Respond in this XML format:.*?)`\)", fn, re.S).group(1)
    compact = re.search(r'sb.WriteString\("(The conversation is about to be compacted.*?)"\)\n', fn, re.S).group(1).encode().decode("unicode_escape")
    return system, intro, tail, compact


def current_prompt(sample):
    """The system prompt and the user prompt the worker sends today, as (system, prompt)."""
    system, intro, tail, compact = _worker_prompt_parts()
    p = "PROGRESS SUMMARY CHECKPOINT\n===========================\n" + intro
    if sample["kind"] == "precompact":
        p += compact + "Recent conversation (oldest first):\n" + sample["conversation"][-24000:] + "\n\n"
    last = strip_system_xml(sample["last_assistant"])
    if len(last) > 4000:
        last = last[:4000] + "... (truncated)"
    return system, p + "Claude's Full Response to User:\n" + last + "\n\n" + tail


# ---------------------------------------------------------------- a prompt written for small models (JSON, one worked example)
SYSTEM_TAILORED = (
    "You write short, factual progress notes about a software developer's coding session, so that the developer, or an "
    "assistant, can pick the work up again later. You are given part of the conversation between the developer and their "
    "coding assistant. Write only what the text supports: never invent file names, numbers, commands, versions or decisions. "
    "Be specific: name the files, tools, versions, commands and decisions that appear in the text, and say why a decision was "
    "made when the text says so. If a field has nothing real to say, use an empty string. Do not describe yourself, these "
    "notes or the conversation format, and do not comment on the developer."
)
EXAMPLE_IN = (
    "Assistant: The nightly build was failing because the cache key in .github/workflows/ci.yml included the lockfile hash, "
    "so every dependency bump missed the cache and the job hit its 20 minute limit. I changed the key to use only the Go "
    "version and added a restore-keys fallback. The next run took 6 minutes. I did not touch the release workflow. Still "
    "open: whether to prune caches older than 7 days, which needs a repository admin."
)
EXAMPLE_OUT = {
    "request": "Fix the nightly CI build that exceeded its time limit",
    "investigated": "The cache key in .github/workflows/ci.yml, which included the lockfile hash.",
    "learned": "Every dependency bump changed the key and missed the cache, so the job hit the 20 minute limit.",
    "completed": "Changed the cache key to the Go version only and added a restore-keys fallback; the next run took 6 minutes. The release workflow was left alone.",
    "next_steps": "Decide whether to prune caches older than 7 days (needs a repository admin).",
    "notes": "",
}
FIELD_HELP = (
    "request: what the developer wanted, at most 15 words\n"
    "investigated: what was looked at or checked\n"
    "learned: facts found out and decisions made, with the reason\n"
    "completed: what is now done or changed\n"
    "next_steps: what is still open or comes next\n"
    "notes: anything else worth knowing; may be empty\n"
    "Keep every field to at most 3 sentences."
)
SUMMARY_SCHEMA = {"type": "object", "properties": {f: {"type": "string"} for f in FIELDS}, "required": FIELDS}


def tailored_prompt(sample):
    if sample["kind"] == "precompact":
        heading, text = "CONVERSATION (oldest first; the earlier part was left out):", sample["conversation"][-24000:]
    else:
        heading, text = "THE ASSISTANT'S LAST REPLY:", strip_system_xml(sample["last_assistant"])[:4000]
    return (f"Here is an example.\n\nEXAMPLE TEXT:\n{EXAMPLE_IN}\n\nEXAMPLE NOTES (JSON):\n{json.dumps(EXAMPLE_OUT, indent=1)}\n\n"
            f"Now write the notes for the following text. Reply with only a JSON object with the fields below.\n{FIELD_HELP}\n\n"
            f"{heading}\n{text}")


# ---------------------------------------------------------------- parsing
def parse_xml_summary(text):
    m = re.search(r"<summary>(.*?)</summary>", text or "", re.S)
    if not m:
        return None
    out = {}
    for f in FIELDS:
        mm = re.search(r"<%s>([^<]*)</%s>" % (f, f), m.group(1))
        out[f] = mm.group(1).strip() if mm else ""
    return out


def parse_json_object(text):
    """The first JSON object in text as a dict, or None."""
    m = re.search(r"\{.*\}", text or "", re.S)
    if not m:
        return None
    try:
        d = json.loads(m.group(0))
    except ValueError:
        return None
    return d if isinstance(d, dict) else None


def parse_json_summary(text):
    d = parse_json_object(text)
    if d is None:
        return None
    return {f: (str(d.get(f, "")).strip() if d.get(f) is not None else "") for f in FIELDS}


# ---------------------------------------------------------------- backends
def run_haiku(prompt, timeout=240):
    t = time.time()
    p = subprocess.run(["claude", "--model", "haiku", "--print", "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "-p", prompt],
                       capture_output=True, text=True, cwd="/tmp", env=dict(os.environ, CLAUDE_MNEMONIC_INTERNAL="1"), timeout=timeout)
    return {"raw": p.stdout, "secs": round(time.time() - t, 1), "rc": p.returncode}


# Some Ollama builds cannot constrain the output to a JSON schema (the MLX gemma4 build answers
# "HTTP 501: structured output is unavailable"). LLM_EVAL_NO_FORMAT=1 sends no schema; the prompts already
# ask for JSON and the parsers take the first {...} of the answer, fenced or not.
NO_FORMAT = os.environ.get("LLM_EVAL_NO_FORMAT", "") == "1"


def run_ollama(model, system, prompt, schema=None, num_ctx=16384, timeout=420):
    msgs = ([{"role": "system", "content": system}] if system else []) + [{"role": "user", "content": prompt}]
    body = {"model": model, "stream": False, "messages": msgs, "keep_alive": "30m", "options": {"num_ctx": num_ctx, "temperature": 0.2}}
    if schema and not NO_FORMAT:
        body["format"] = schema
    t = time.time()
    req = urllib.request.Request(ollama_url() + "/api/chat", data=json.dumps(body).encode(), headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            d = json.loads(r.read())
    except Exception as e:  # a failure is a result, not a crash
        return {"raw": "", "secs": round(time.time() - t, 1), "error": str(e)}
    ec, ed = d.get("eval_count", 0), d.get("eval_duration", 0)
    return {"raw": d["message"]["content"], "secs": round(time.time() - t, 1), "done_reason": d.get("done_reason"),
            "prompt_tokens": d.get("prompt_eval_count"), "out_tokens": ec, "tok_per_s": round(ec / (ed / 1e9), 1) if ed else None}


def embed(texts, model=EMBED_MODEL):
    body = json.dumps({"model": model, "input": texts}).encode()
    req = urllib.request.Request(ollama_url() + "/api/embed", data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=300) as r:
        return json.loads(r.read())["embeddings"]


def cosine(a, b):
    na, nb = math.sqrt(sum(x * x for x in a)), math.sqrt(sum(x * x for x in b))
    return sum(x * y for x, y in zip(a, b)) / (na * nb) if na and nb else 0.0
