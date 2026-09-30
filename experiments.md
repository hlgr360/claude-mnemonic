# Experiment journal

## Campaign B: binary size

Metric: sum of bytes of worker, mcp and 6 hook binaries (release flags, `-s -w -trimpath`, `-tags fts5`). Lower is better.
Threshold: at least 3% drop, tests green. Stop: 5 consecutive no-improve trials or 15 trials.
Mutate: Go code and build flags. Frozen: public API, DB schema, plugin manifest.

| id | mutation | total bytes | delta vs base | kept? |
|----|----------|-------------|---------------|-------|
| 01 | baseline | 102698416 | - | base |
| 02 | pkg/hooks: raw HTTP/1.0 client replaces net/http | 87603952 | -14.7% | KEEP |
| 03 | statusline: shared client, drop net/http | 84531056 | -3.5% vs 02 | KEEP |
| 04 | embed gzipped ONNX runtime libs, gunzip at first use | 57216880 | -32.3% vs 03 | KEEP |
| 05 | hooks built with CGO_ENABLED=0 | 57216880 | 0% | revert (not applied) |
| - | statically dedupe or compress tokenizer.json | - | projected ~1.2-1.6% | not run, under 3% threshold |
| - | drop net/http from mcp | - | none possible | not run, oss-telemetry links net/http |

Campaign B stopped on diminishing returns: 102698416 -> 57216880 bytes (-44.3%).

## Campaign A: hook latency (stub worker, 200 runs, p50)

Harness: hook binary against a local stub worker (100-observation context), same machine, alternating runs. Lower is better.

| hook | before (ee28544) | after trial 02 | delta |
|------|------------------|----------------|-------|
| session-start | 4.6-5.6 ms | 3.3 ms | -30% or better |
| user-prompt | 4.3-4.7 ms | 3.5-3.7 ms | -15% to -22% |

Hooks now sit near the process-start floor. No further trials run.

## Campaign D: reliability

Metric: failing cases in the extraction fault test (`TestWriteGunzipped`). Lower is better.

| id | mutation | failing cases | kept? |
|----|----------|---------------|-------|
| 01 | baseline (truncated archive leaves partial lib.so) | 1 | base |
| 02 | extract via temp file and rename | 0 | KEEP |

## Campaign C: search accuracy

Harness: `scripts/eval-search` seeds 30 labelled observations, starts a real worker (fresh HOME, model cache copied), waits for the vector rebuild, then runs 30 paraphrased queries against `/api/context/search`. Metric: MRR@10 (higher is better). Threshold: at least +0.02. Runs are deterministic (repeat runs identical).

| id | mutation | MRR@10 | recall@5 | kept? |
|----|----------|--------|----------|-------|
| 01 | baseline | 0.7944 | 0.8667 | base |
| 02 | L2-normalise embeddings (similarity was negative for every vector hit, so search fell back to FTS) | 0.8944 | 0.9667 | KEEP |
| 03 | reranking alpha 0.7 -> 0.5 | 0.9111 | 0.9667 | revert (+0.017 < 0.02) |
| 04 | reranking alpha 0.7 -> 0.9 | 0.9111 | 0.9667 | revert (+0.017 < 0.02) |
| 05 | reranker off | 0.8889 | 0.9667 | revert |
| 06 | reranker pure mode | 0.8714 | 0.9667 | revert |
| 07 | CLS pooling instead of mean | 0.8944 | 0.9667 | revert |

Trial 02 changes stored vectors. `BGEStorageVersion` is bumped to `bge-v1.5-l2`, so existing installs rebuild vectors on next start.
Caveat: 30 queries is a small set, so borderline gains such as alpha 0.5 are not distinguishable from noise.

Notes:
- Trial 04 needs `scripts/download-onnx-libs.sh` to produce `.gz` files; existing raw libs trigger a re-download.
- Extracted library verified byte-identical to the raw dylib (`cmp`).
