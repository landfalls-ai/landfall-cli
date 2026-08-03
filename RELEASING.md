# Releasing `landfall-cli`

This is a manual process by design (no CI release pipeline exists for this repo yet).
Follow these steps in order for every release.

1. Merge the change into `main`.
2. Bump `version` in `package.json` to match the tag you're about to cut (semantic
   versioning: `MAJOR.MINOR.PATCH`).
3. Tag and push:
   ```
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
4. Compute the checksum of GitHub's auto-generated source tarball for that tag (no
   separate build artifact is needed — there's no compile step):
   ```
   curl -sL https://github.com/landfalls-ai/landfall-cli/archive/refs/tags/vX.Y.Z.tar.gz | shasum -a 256
   ```
5. In [`landfalls-ai/homebrew-landfall`](https://github.com/landfalls-ai/homebrew-landfall),
   edit `Formula/landfall.rb`:
   - `url` → the new tag's tarball URL
   - `sha256` → step 4's output
6. Commit and push the Formula change.
7. Verify: `brew update` first (Homebrew caches the tap locally and won't see the new
   commit until this runs), then `brew upgrade landfall` (or a fresh `brew install
   landfall` in a container) — confirm it picks up the new version.

If any step above needed something not written down here, add it before closing out the
release — this file is the whole process, not a summary of it.
