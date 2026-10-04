# Contributing

This describes how work is done in **this fork** (`hlgr360/claude-mnemonic`). It is the fork's own workflow, not upstream's: it mentions the fork's repository flag, a private-content check and the fork's disabled workflows, so keep it out of any change you send to the upstream repository (see [Sending something upstream](#sending-something-upstream)).

## The flow

1. **Ticket first.** Open an issue on the fork with the problem, the proposal and the acceptance. Bugs get the `bug` label.
2. **Branch** from `main`: `feat/…`, `fix/…`, `docs/…` or `chore/…`.
3. **Implement** in small commits. Unit tests go with the code; a feature that crosses the worker, the MCP server and the dashboard also gets an end-to-end check.
4. **Check locally** (below), then **open a PR** whose body says `Closes #N`, what changed and what was tested, including what was *not* validated.
5. **Review what the bots said** before merging (below). Merge with a merge commit.
6. **Clean up:** pull `main`, delete the branch (and any worktree), and after `make install` restart Claude Code.

Always pass the repository to `gh`. The checkout also has an `upstream` remote, and without `--repo` a bare `gh issue create` or `gh pr create` can land on the upstream repository:

```bash
gh issue create --repo hlgr360/claude-mnemonic …
gh pr create   --repo hlgr360/claude-mnemonic --base main --head <branch> …
```

## Checks before a PR

All of these are run before pushing. The Go tests need the `fts5` build tag (`make test` sets it and the model directory for you).

| What | Command |
|---|---|
| Format | `gofmt -l internal pkg cmd` (prints nothing when clean) |
| Vet | `go vet -tags fts5 ./...` |
| Struct alignment (also on tests) | `GOFLAGS=-tags=fts5 go run golang.org/x/tools/go/analysis/passes/fieldalignment/cmd/fieldalignment@latest ./...` |
| Go tests with the race detector | `go test -tags fts5 -race -count=1 ./...` |
| UI types and tests | `cd ui && npx vue-tsc --noEmit && npm test` |
| Python script tests | `make test-scripts` |
| End-to-end | `scripts/e2e/run.sh` |

Known baseline, so you do not chase it: `internal/update` `TestExtractTarGz_FilePermissionsPreserved` fails on an untouched `main` (shell umask), `internal/vector/sqlitevec` can abort at exit with an ONNX `recursive_mutex lock failed` although every test passed, and `TestRunBriefPass_RewritesInPlace…` compares millisecond timestamps and fails now and then in a full `internal/worker` run (it passes alone). Anything else failing is yours.

**The end-to-end run** (`scripts/e2e/run.sh`) builds the worker, MCP server and hooks, runs them in an isolated `HOME` on private ports (your own `~/.claude-mnemonic` and worker are never touched), and drives them the way Claude Code and Claude Desktop do. The dashboard suite needs `ui/dist` (`cd ui && npm run build`) and Chrome.

```bash
scripts/e2e/run.sh                          # everything
scripts/e2e/run.sh --no-ui                  # without the dashboard suite
E2E_ONLY="Dashboard" scripts/e2e/run.sh     # only suites whose name contains the text
KEEP=1 scripts/e2e/run.sh                   # keep the work directory and logs
```

- Run **one e2e at a time** (the ports are fixed), and do not run the full Go test suite next to it: the load makes timing-based checks flaky.
- `E2E_ONLY` is a substring match, and a few suites rely on data an earlier suite seeds, so a filter such as `project` can run a suite that then fails for lack of data. If a filtered run fails oddly, run the full thing.
- Use `data-testid` attributes for dashboard checks. Clicking a button by its visible text can hit a different button that happens to contain the same word.
- When you add a check for a bug, make sure it fails against the old code.

## Which repository releases come from

The in-app updater, the install and uninstall scripts and the release scripts take the release repository and the marketplace name from one place each, so a fork or a rename needs no code change:

| What | Default | Override |
|---|---|---|
| Updater (Go) | `hlgr360/claude-mnemonic` | `go build -ldflags "-X github.com/lukaszraczylo/claude-mnemonic/internal/update.GitHubRepo=owner/name"` |
| Signing identity the updater accepts | any workflow of that repository | `-X …/internal/update.CertificateIdentityRegexp=<regexp>` (for releases signed by a reusable workflow in another repository) |
| `install.sh`, `install.ps1`, `register-plugin.sh`, `update-marketplace.sh` | `hlgr360/claude-mnemonic` | `MNEMONIC_REPO=owner/name` |
| Marketplace name the install, register and uninstall scripts use | `claude-mnemonic` | `MNEMONIC_MARKETPLACE=name` |

## Security scanning

Every PR runs `gosec` and CodeQL, and both comment on the PR. Run `gosec` locally before pushing:

```bash
go run github.com/securego/gosec/v2/cmd/gosec@latest -quiet -fmt text ./...
```

Fix a finding when you can: owner-only permissions for files and directories (`0o600`, `0o750`), and never put a value that came from a request on a command line (give a child process its working directory with `cmd.Dir`, and keep every argument a constant). If a finding is a false positive, justify it on the same line, in the style already used in the code:

```go
cmd := exec.Command(workerPath) // #nosec G204 -- workerPath is from internal findWorkerBinary
```

## Before you commit

- **Scan the staged diff for private content** and let the scan stop the commit. Test data and fixtures use neutral names (`alpha`, `beta`, `shop`, `rates`) and `.org` example addresses, never real project or customer names, real addresses or home-directory paths. A scan that only prints is not a gate; make it fail:

  ```bash
  if git diff --cached | grep -E '^\+' | grep -v '^+++' | grep -inE '<your private words>|@[a-z0-9-]+\.(com|de)'; then
    echo "private content found"; git reset -q; exit 1
  fi
  ```

  The pattern also catches harmless text such as a git URL with a user, so prefer another fixture over loosening the pattern.
- **Do not commit what the build rewrites.** `make install` and `npm run build` rewrite version stamps (`.claude-plugin/plugin.json`, `marketplace.json`, `ui/package.json`, `ui/package-lock.json`, `ui/tsconfig.tsbuildinfo`). Restore them with `git checkout -- <file>` before you commit.
- **Use the identity configured for this repository**, not a work address.
- End commit messages with the `Co-Authored-By:` line when an assistant wrote the change.

## Before you merge

Read everything the bots said about the PR, and wait for CI to finish. Do not merge on a green summary alone.

```bash
gh pr view N --repo hlgr360/claude-mnemonic --json comments,reviews,statusCheckRollup
gh api repos/hlgr360/claude-mnemonic/pulls/N/comments      # inline review comments
gh pr checks N --repo hlgr360/claude-mnemonic
```

- There is a `github-actions` **PR checks** report (vet and staticcheck, TruffleHog and govulncheck, gosec, GoReleaser config, CodeQL, the race tests, total coverage) and `github-advanced-security` reviews with inline CodeQL and gosec alerts.
- `mergeStateStatus` must be `CLEAN`. **`UNSTABLE` means a check failed or is still running**: find out which (`gh api repos/hlgr360/claude-mnemonic/check-runs/<id>/annotations`) before merging.
- Fix or justify every finding, push, wait for the rerun and read the new report. A PR that has no checks and no comments is reported as that, not assumed fine.
- Say what the bots said, including "nothing", in the merge report.

## After merging

```bash
git checkout main && git pull origin main
git branch -d <branch>            # and: git worktree remove <path> if you used one
make install                      # then restart Claude Code
```

`make install` builds, installs the binaries and the plugin, and restarts the worker. **Restart Claude Code afterwards**: open sessions keep working but do not register hook events added by the new version.

## Sending something upstream

Upstream (`lukaszraczylo/claude-mnemonic`) is a separate project with its own habits. A change for it comes from a **new branch off `upstream/main` containing only that change**, never from a branch of this fork, so nothing fork-specific travels with it: not this document, not the PR template, not the fork's disabled workflows (`Release` and `Update dependencies` are switched off in the fork's settings, which is not part of the repository). Ask before opening anything on the upstream repository.

## Where things are

- `README.md`: features, install, configuration. `DESKTOP.md`: Claude Desktop (tools, instructions, project management, duplicates).
- `design/`: design notes. `docs/`: the project website.
- `scripts/e2e/`: the end-to-end suites (`drive_*.py`, `ui_e2e.mjs`, `seed_ui.py`, `run.sh`). `scripts/llm-eval/`: the evaluation of local models.
- `.golangci.yml`: the lint configuration (`gosec`, `govet` with `fieldalignment`, `staticcheck`, `errcheck`, `gofmt`).
