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
| Python script tests (installers, release packer and workflow, plugin) | `make test-scripts` |
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

## Releasing

Releases are built by `.github/workflows/release-native.yaml` ("Release (fork)"). Upstream's `release.yaml` (a shared reusable workflow that needs a GoReleaser Pro key) is disabled in this fork's settings and left untouched so upstream merges stay clean; do not enable it.

- **What it does.** One job per native runner (macOS arm64, Linux amd64, Windows amd64: the build uses CGO) runs `scripts/build-release.sh <version>`, which builds the dashboard and the nine binaries and packs `claude-mnemonic_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) in the layout the updater and the install scripts unpack. A last job writes `checksums.txt`, signs it with cosign (keyless, so no key to keep), **verifies the signature with the same arguments the in-app updater uses**, and publishes the GitHub release with the archives, `checksums.txt` and `checksums.txt.sigstore.json`.
- **Only a `v*` tag publishes.** A pull request that touches the release files, and a manual run (`gh workflow run release-native.yaml --repo hlgr360/claude-mnemonic`), only build and keep the archives as workflow artifacts for seven days.
- **The version is upstream's.** A release carries the version of the upstream release merged into `main` (`v0.21.95` while that is the latest merged), not a number of its own: running it gives everything upstream has at that version plus this fork's additions. `scripts/release-version.sh` reads it from the merge state (`--tag` for the `vX.Y.Z` form). The tag must be plain `vX.Y.Z`: the in-app updater compares dotted numbers only and mis-reads a suffix such as `-fork.1`. Consequence: one release per upstream version; a fork-only fix made in between ships with the next upstream version you merge.
- **Cutting a release** (the maintainer's call, because it is public and cannot be taken back cleanly), on a merged and pulled `main`:

  ```bash
  git fetch upstream --tags
  scripts/release-version.sh --tag        # e.g. v0.21.95
  git push origin "$(git rev-parse HEAD)":refs/tags/v0.21.95
  ```

  Push the commit to the tag name instead of running `git tag`: the clone already has upstream's tag of that name (pointing at upstream's commit), and the release must be built from ours. The workflow then builds, signs and publishes. A tag that already has a release fails loudly rather than replacing it.
- **Build one locally** on a supported platform: `scripts/build-release.sh 0.0.1-local` (`DIST=<dir>` for the output, `SKIP_UI=1` to reuse an existing dashboard build). It rewrites `ui/package.json`, `ui/tsconfig.tsbuildinfo` and `internal/worker/static`; restore them with `git checkout --` before committing. Do not run the unpacked `worker` to read its version: it has no version flag and starts a real worker.
- **`.goreleaser.yaml`** is no longer the release path. It is kept valid because the pull request check runs `goreleaser check` on it.

## The plugin

The Claude Code plugin is **thin**: no binaries, one platform-independent zip (`claude-mnemonic-plugin_<version>.zip`) built by `scripts/build-plugin.sh <version>` and published by the release next to the platform archives, under the same signed `checksums.txt`. `claude --plugin-dir dist/plugin` loads the built tree without installing it (note that its hooks run for real: they start the worker and write to `~/.claude-mnemonic`).

- **What is in it.** The manifest (version stamped), `hooks/hooks.json` and the hook wrappers, the `mcp-server` wrapper, `commands/*.md`, the memory skill (`skills/project-memory/SKILL.md`, generated by `scripts/render_skill.py` from `scripts/desktop-instructions.txt`, the text pasted into Claude Desktop), and `lib/ensure-binaries.sh`. The wrappers are the same scripts `make install` uses (`hooks/*`, `mcp-server` at the repository root); the source of the manifest is `plugin/.claude-plugin/plugin.json.tpl`.
- **Who manages the binaries.** The plugin installs them, and the in-app updater keeps working after that. `lib/ensure-binaries.sh` fetches the archive of the plugin's own version from the release, checks it against `checksums.txt` (and against the cosign signature when cosign is installed, the check the updater makes) and installs into `~/.claude-mnemonic/bin`, where the hooks and the worker already look first. It installs when the worker or MCP server is missing or when its marker (`.plugin-version`) is older than the plugin; it **never replaces binaries that have no marker** (`make install`, `install.sh`) and never downgrades, so the updater can move forward in between. The plugin version is the floor.
- **First run.** The session-start hook starts the download in the background and returns at once (hooks must not wait); the MCP server waits for it. The first session may therefore run without memory until the download has finished. Supported: macOS arm64 and Linux amd64. Windows is not supported by the plugin yet; use `install.ps1`.
- **The name.** `claude plugin validate` rejects a third-party plugin name that starts with `claude-`. Claude Code installs and loads such a plugin all the same (only `validate`, `plugin init` and `plugin tag` check the name), and the name keeps `/claude-mnemonic:dashboard` and existing installs working, so it stays. The build runs `claude plugin validate --strict` and accepts exactly that one error; any other error or warning fails it.
- **Not both.** The plugin and an install from `install.sh` or `make install` are alternatives (both register the same hooks and commands).

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
