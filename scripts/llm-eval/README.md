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

## What we found

See the comments on issue #26. In short: no local model written for this yet is good enough to replace
Haiku for summaries, and for conflict detection even the best local model wrongly replaced about a quarter
of real near-neighbour pairs, so any conflict producer must start as a report-only proposal.

## Tests

`python3 -m unittest scripts/test_llm_eval.py` (also part of `make test-scripts`).
