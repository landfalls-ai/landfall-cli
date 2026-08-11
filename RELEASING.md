# Releasing `landfall-cli`

Cutting a version is still a human decision (steps 1-3); publishing it to Homebrew is
not (steps 4-6, automated by `.github/workflows/bump-homebrew-tap.yml` since 2026-08-11
— this repo previously required doing those by hand every time, which is exactly the
kind of step that silently falls behind: it once left `brew install landfall` two
releases stale before anyone noticed). Follow steps 1-3 in order for every release;
steps 4-6 happen on their own within a minute or two of step 3's `git push`.

1. Merge the change into `main`.
2. Bump `version` in `package.json` to match the tag you're about to cut (semantic
   versioning: `MAJOR.MINOR.PATCH`).
3. Tag and push:
   ```
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
4. **(automated)** The push above triggers `bump-homebrew-tap.yml`, which computes the
   checksum of GitHub's auto-generated source tarball for that tag (no separate build
   artifact is needed — there's no compile step).
5. **(automated)** In [`landfalls-ai/homebrew-landfall`](https://github.com/landfalls-ai/homebrew-landfall),
   the workflow edits `Formula/landfall.rb` (`url` → the new tag's tarball URL, `sha256`
   → step 4's output) via [`mislav/bump-homebrew-formula-action`](https://github.com/mislav/bump-homebrew-formula-action).
6. **(automated)** The workflow commits and pushes the Formula change directly to the
   tap's `main` (no branch protection there, so no PR step is needed).
7. Verify: `brew update` first (Homebrew caches the tap locally and won't see the new
   commit until this runs), then `brew upgrade landfall` (or a fresh `brew install
   landfall` in a container) — confirm it picks up the new version.

**One-time setup this automation depends on**: a fine-grained GitHub PAT scoped to
`landfalls-ai/homebrew-landfall` only, `Contents: Read and write`, stored as this repo's
`HOMEBREW_TAP_TOKEN` Actions secret. If that secret is missing or expired, step 4
fails visibly in the Actions tab (workflow run on the `vX.Y.Z` tag) rather than silently
— fall back to the old manual steps 4-6 above for that one release, then fix the token
before the next.

If any step above needed something not written down here, add it before closing out the
release — this file is the whole process, not a summary of it.
