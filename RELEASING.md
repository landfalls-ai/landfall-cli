# Releasing `landfall-cli`

Cutting a version is still a human decision (steps 1-2); building, publishing to GitHub
Releases, and updating Homebrew are not (step 3 onward, automated by
`.github/workflows/release.yml` via [goreleaser](https://goreleaser.com) since the
Go/Cobra/Viper rewrite — `specs/20260822-golang-cli-rewrite` in the `landfalls-ai/landfall`
monorepo). This replaced the prior Node-era process (`bump-homebrew-tap.yml`, which only
ever bumped a formula pointing at this repo's own auto-generated source tarball — there was
no compile step to produce anything else). A compiled Go binary needs real per-platform
build+release+tap steps; goreleaser does all of them as one pipeline. Follow steps 1-2 in
order for every release; everything after happens on its own within a few minutes of step
2's `git push`.

1. Merge the change into `main`.
2. Tag and push:
   ```
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
   There is no `package.json` version to bump for the Go binary itself — `main.version`
   (what `landfall serve`'s MCP `serverInfo.version` reports, and what `landfall --version`
   would show if that flag existed) comes from the git tag via goreleaser's `{{.Version}}`
   templating and `-ldflags -X main.version=...`, not from a file anyone edits by hand. (If
   `package.json` still exists at release time — e.g. during the Node/Go coexistence window —
   its own `version` field is a separate, cosmetic concern unrelated to what actually ships.)
3. **(automated)** The push above triggers `.github/workflows/release.yml`, which runs
   `goreleaser release --clean`:
   - Cross-compiles `landfall` for `darwin/amd64`, `darwin/arm64`, `linux/amd64`,
     `linux/arm64` (Windows is out of scope for this rewrite — spec FR-012), each with
     `main.version` stamped in.
   - Archives each binary (with `LICENSE`/`README.md`) as a `.tar.gz`, computes checksums.
   - Publishes a GitHub Release on this repo with the tag, changelog, and all four archives
     attached.
   - Generates and pushes a Homebrew Cask (`Casks/landfall.rb`, not the old
     `Formula/landfall.rb`) to
     [`landfalls-ai/homebrew-landfall`](https://github.com/landfalls-ai/homebrew-landfall)'s
     `main` branch directly (no branch protection there, so no PR step needed) — see
     `.goreleaser.yml`'s `homebrew_casks` section for exactly what it writes.
4. Verify: `brew update` first (Homebrew caches the tap locally and won't see the new commit
   until this runs), then `brew upgrade landfall` (or a fresh `brew install landfall` in a
   container) — confirm it picks up the new version and `landfall serve`'s MCP `initialize`
   response reports the right `serverInfo.version`.

**One-time setup this automation depends on**: a fine-grained GitHub PAT scoped to
`landfalls-ai/homebrew-landfall` only, `Contents: Read and write`, stored as this repo's
`HOMEBREW_TAP_TOKEN` Actions secret — the same credential the prior workflow used, still
valid, no rotation needed for this migration. If that secret is missing or expired, the
`goreleaser` job fails visibly in the Actions tab (workflow run on the `vX.Y.Z` tag) at the
Homebrew-publish step specifically — GitHub Release publishing (which only needs the
built-in `GITHUB_TOKEN`) will have already succeeded, so a failed run here means "the
binaries exist, the tap wasn't updated," not "nothing shipped."

**Migration note (one-time, this release only):** the tap's existing `Formula/landfall.rb`
(the old Node source-tarball formula) is left in place by this change — it is not deleted or
migrated automatically. Once the first Go release has shipped and been verified via step 4
above, delete `Formula/landfall.rb` from the tap by hand (a Cask and a Formula with the same
`name` can't coexist safely) — see goreleaser's own guidance on cask migrations for whether a
`tap_migrations.json` entry is worth adding so existing Formula users upgrade cleanly.

If any step above needed something not written down here, add it before closing out the
release — this file is the whole process, not a summary of it.
