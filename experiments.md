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

Notes:
- Trial 04 needs `scripts/download-onnx-libs.sh` to produce `.gz` files; existing raw libs trigger a re-download.
- Extracted library verified byte-identical to the raw dylib (`cmp`).
