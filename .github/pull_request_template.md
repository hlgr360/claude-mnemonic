Closes #

## What and why

<!-- The problem, and what this changes. -->

## Tested

<!-- What was run, with the result. Say what was NOT validated. -->

## Checklist (see CONTRIBUTING.md)

- [ ] `gofmt -l`, `go vet -tags fts5 ./...` and fieldalignment are clean
- [ ] `go test -tags fts5 -race ./...` passes (apart from the known baseline listed in CONTRIBUTING.md)
- [ ] UI changes: `npx vue-tsc --noEmit` and `npm test` in `ui/`; script changes: `make test-scripts`
- [ ] End-to-end run for changes that cross the worker, MCP server or dashboard (`scripts/e2e/run.sh`); a new check was seen to fail against the old code
- [ ] `gosec` run locally; findings fixed or justified inline with `#nosec`
- [ ] The staged diff was scanned for private content; no files rewritten by `make install` are committed
- [ ] Docs updated (README, DESKTOP.md) where behaviour changed

## Before merging

- [ ] CI finished and `mergeStateStatus` is `CLEAN` (not `UNSTABLE`)
- [ ] The bots' comments, reviews and inline alerts were read and dealt with
