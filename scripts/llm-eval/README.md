# Local-LLM evaluation harness

Tools to find out, on your own data, whether a local Ollama model can do work that claude-mnemonic
currently sends to Haiku. Used for issue #26. Standard library only; nothing in here is part of the plugin.

Needs a running Ollama (`OLLAMA_HOST`, default `localhost:11434`), the model under test, the bge embedding
model `qllama/bge-small-en-v1.5` (for similarity and for pairing observations), and the `claude` CLI for
the Haiku reference and judge (about 100 small calls per summaries run; it uses your Claude account).
Your transcripts and database are read locally and never committed; keep `--out` outside the repository.

## Summaries (`summaries.py`)

Can a local model write the session summaries the worker asks Haiku for?

```sh
python3 scripts/llm-eval/summaries.py samples --transcripts '~/.claude/projects/<project>/*.jsonl' --out /tmp/eval
python3 scripts/llm-eval/summaries.py run --out /tmp/eval --model haiku
python3 scripts/llm-eval/summaries.py run --out /tmp/eval --model gemma3:12b          # both prompts
python3 scripts/llm-eval/summaries.py score --out /tmp/eval --judge
```

It picks real turns (Stop-hook style: the last reply; PreCompact style: a conversation excerpt), runs each
through the worker's **current** prompt (read from `internal/worker/sdk`, so it stays in step) and through a
**tailored** prompt written for small models (JSON output through Ollama's `format`, one worked example),
and scores every candidate with a blind Haiku judge (faithfulness, specificity, usefulness, 1 to 5) plus
mechanical checks. The judge is Haiku, so it may favour Haiku-style text; read some outputs too.

## Conflicts (`conflicts.py`)

Can a local model tell whether a newer observation replaces an older one? claude-mnemonic has a complete
but unused conflict system, and superseded observations are deleted after three days, so a wrong "replace"
deletes a good memory.

```sh
python3 scripts/llm-eval/conflicts.py pairs --db ~/.claude-mnemonic/claude-mnemonic.db --project NAME_abc123 --out /tmp/conf
python3 scripts/llm-eval/conflicts.py run --out /tmp/conf --model haiku
python3 scripts/llm-eval/conflicts.py run --out /tmp/conf --model gemma3:12b
python3 scripts/llm-eval/conflicts.py score --out /tmp/conf
```

Pairs are every observation of one project with its closest older observations (the database is opened
read-only and only the named project is read), plus 24 built-in invented pairs whose correct answer is
known. Real pairs are scored against Haiku, which is a reference and not the truth: in a manual check it
over-called "supersedes" on notes that merely refine each other. The key number is **harmful replace**: notes
that both stay true (related, unrelated) that the model would replace. Replacing a true duplicate is harmless.

## Models that cannot do structured output

Both scripts ask Ollama for a JSON schema (`format`) in some prompts. Some builds cannot honour one: the MLX build of
`gemma4` answers `HTTP 501: structured output is unavailable`. Run with `LLM_EVAL_NO_FORMAT=1` to send no schema; the
prompts already ask for JSON, the parser takes the first `{...}` of the answer (fenced or not), and the conflict
relation is matched case-insensitively. The worker itself never sends a schema, so this only concerns the harness.

```sh
LLM_EVAL_NO_FORMAT=1 python3 scripts/llm-eval/conflicts.py run --out /tmp/conf --model gemma4:e4b-mlx
LLM_EVAL_NO_FORMAT=1 python3 scripts/llm-eval/summaries.py run --out /tmp/eval --model gemma4:e4b-mlx
```

## Project briefs (`briefs.py`)

Can a local model write the project brief (the rollup) the worker asks Haiku for? A brief condenses up to 100 of a
project's observations and the developer's checkpoint notes into one dated orientation.

```sh
python3 scripts/llm-eval/briefs.py samples --db ~/.claude-mnemonic/claude-mnemonic.db --auto 4 --min-obs 5 --out /tmp/brief
python3 scripts/llm-eval/briefs.py run --out /tmp/brief --model haiku
python3 scripts/llm-eval/briefs.py run --out /tmp/brief --model gemma4:e4b-mlx
python3 scripts/llm-eval/briefs.py score --out /tmp/brief --judge
```

`samples` builds each request exactly as the worker does (the live, project-scoped observations by importance, oldest
first; the thread notes), `run` sends the worker's **current** brief prompt (read from `internal/worker/sdk/brief.go`)
and keeps what the worker would store, and `score` prints mechanical checks (the four sections in order, the 300-word
limit, invented citations, emails, an open-items section, specifics not in the notes, similarity to Haiku's brief) and a
blind Haiku judge (faithfulness, currency, usefulness, concision). The worker leaves global-scope notes out of a brief,
and most notes of a real archive are global, so its input can be small; `--include-global` (with `--num-ctx 32768`
when running) also tries the models on the fuller 100-note input. A prompt that does not fit the context window loses
its start, system prompt included, so mind `--num-ctx` (the worker's default is 16384).



See the comments on issue #26. In short: no local model written for this yet is good enough to replace
Haiku for summaries, and for conflict detection even the best local model wrongly replaced about a quarter
of real near-neighbour pairs, so any conflict producer must start as a report-only proposal.

`gemma4:e4b-mlx` (Ollama 0.34.4, no schema), measured on the same 90 real pairs, 24 known pairs and 10 sample turns:
it is the best local model so far and the first one close to Haiku on conflicts. On the known pairs it never replaced a
note that stays true (0/9, like Haiku) and caught 10/10 real replacements. On the real pairs it wrongly replaced 3/72 notes
that stay true (the strict Haiku prompt: 2/72; `gemma3:12b`: 20/72), and found 9 of the 17 replacements Haiku labels, the
same as the strict Haiku prompt. All three mistakes were high-confidence "supersedes" on project-milestone notes, the
failure every local model shows, so conflicts must stay proposals. For summaries (judged by Haiku, 1 to 5) its tailored
prompt gave faithfulness 4.4, specificity 4.6, usefulness 4.0 (Haiku 4.9, 4.8, 4.9), 8 of 10 with faithfulness 4 or more,
and it is not good enough to replace Haiku; with the worker's current XML prompt 3 of 10 answers did not parse. It is slower
than `gemma3:12b` on conflicts (about 12 s a pair, Haiku 8 s). The judge is Haiku and ten samples are few.

Briefs, four of the largest projects of one archive, judged by Haiku (1 to 5): on the worker's own input (6 to 12
notes) `gemma4:e4b-mlx` scored faithfulness 4.75, currency 5.0, usefulness 5.0 and concision 5.0 against Haiku's 4.50,
5.0, 4.75 and 5.0, with 4 of 4 faithfulness 4 or better, in 18 s a brief (Haiku 21 s). On the fuller input (51 to 100
notes) it scored 4.75, 4.75, 4.75 and 4.75 against Haiku's 4.00, 3.50, 4.75 and 5.00, in 38 s (Haiku 22 s), and `gemma3:12b`
scored 4.50, 4.50, 4.25 and 4.75. So for this task a local model is competitive with Haiku, unlike for summaries. Caveats:
four briefs per set, the judge is Haiku, gemma4 cites fewer notes (5 to 8 distinct against 8 to 20) and writes longer than the
300 words it was asked for (all four on the fuller input; the worker caps what it stores), and a mistake here is a stale
orientation, not a deleted memory.

## Tests

`python3 -m unittest scripts/test_llm_eval.py` (also part of `make test-scripts`).
