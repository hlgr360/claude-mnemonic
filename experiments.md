# Experiment journal

## Campaign B: binary size

Metric: sum of bytes of worker, mcp and 6 hook binaries (release flags, `-s -w -trimpath`, `-tags fts5`). Lower is better.
Threshold: at least 3% drop, tests green. Stop: 5 consecutive no-improve trials or 15 trials.
Mutate: Go code and build flags. Frozen: public API, DB schema, plugin manifest.

| id | mutation | total bytes | delta vs base | kept? |
|----|----------|-------------|---------------|-------|
| 01 | baseline | 102698416 | - | base |
| 02 | pkg/hooks: raw HTTP/1.0 client replaces net/http | 87603952 | -14.7% | KEEP |
