# Runner fleet

Forgejo Actions runners for `git.rezus.cloud`. The **server needs no changes for
non-linux runners** — labels are free-form and the `host` label schema (jobs run
directly on the machine) is even the protocol default. What upstream *doesn't*
ship is darwin runner **binaries**: `code.forgejo.org/forgejo/runner`'s
build-release.yml passes `platforms: linux/amd64,linux/arm64` to the shared
build action, so no macOS assets exist at any upstream version.

## Platform matrix

| Platform | Deployment | Labels (schema) | Owner |
|---|---|---|---|
| linux (k8s) | `k8s-iac/modules/codeberg-runner` — wrenix chart + DinD | `<name>:docker://<image>` | k8s-iac repo |
| **macOS (host)** | **`runners/macos/`** — this dir, user LaunchAgent | `<os>-<arch>:host` | this monorepo |

## Binary source

The runner source is **vendored pristine upstream** at `runner/` (pinned tag in
`runners/RUNNER_UPSTREAM`, bumped by the vendor-bump workflow — the
`charts/forgejo-monorepo` pattern). Upstream ships linux-only release binaries, so the
monorepo's `release.yml` builds the darwin pair on every `v*-rezus.*` tag and
attaches versionless assets (`forgejo-runner-{goos}-{goarch}.tar.gz` + bare-name
`.sha256` + `checksums.txt`) next to the fj binaries. The binary stamps its own
lineage `<upstream>-rezus.<N>` (e.g. `v12.7.3-rezus.4`); the linux fleet keeps
consuming the upstream OCI image. The standalone fork
`rezuscloud/forgejo-runner` is retired (archived) — this monorepo is the single
release point.

## Labels contract

- Format `name[:schema[:args]]`; schema `docker` (container from image) or
  `host` (steps run directly on the machine), **`host` is the default**.
- Host runners use distinct names — `macos-arm64:host`, never `ubuntu-latest` —
  so GitHub-flavored workflows cannot accidentally run unsandboxed on a laptop.
- Jobs land via `runs-on: macos-arm64` matching by name.

## Security model

Host mode executes workflow code **with the privileges of the LaunchAgent
user**, no isolation. Treat any repo whose workflow can target the label as
trusted on that machine. Future isolation tiers if untrusted jobs ever need
macOS:

- **Tart / Cirrus-style** — ephemeral macOS VMs on Apple Silicon
  (Virtualization.framework), per-job throwaway instances.
- **Colima/Lima** — Linux containers on the Mac via a Lima VM; lets the same
  host additionally serve `docker://` labels (Woodpecker community pattern).

## macOS host layout (per `runners/macos/`)

| Path | Contents |
|---|---|
| `~/.local/bin/forgejo-runner` | binary (version-pinned, sha256-verified) |
| `~/.local/share/forgejo-runner/` | `config.yml`, `.runner` (registration), cache, logs |
| `~/Library/LaunchAgents/com.rezuscloud.forgejo-runner.plist` | user agent: RunAtLoad + KeepAlive |

Registration uses a one-shot instance-level registration token
(`fj api admin get-registration-token`); the resulting `.runner` file
holds the per-runner secret. The registration token is never stored.

## Bump-class matrix (in-repo contract)

The vendored `runner/` tree is maintained by the fork-maintenance engine
(k8s-config `forks/forgejo-runner.yaml`, `mode: subtree`; agent-side
operating knowledge lives in the fork-maintenance skill — vendored-tree
mode section, incl. the four maintenance shapes and the onboarding
recipe). The pin lives in
`runners/RUNNER_UPSTREAM`; propagation is delegated to the vendor-bump workflow
and byte-verified against the upstream archive (pristine gate). How a newer
upstream tag is handled depends on its class **relative to the pin**:

| Class | Example (pin v12.7.3) | Engine action | Merge | Validation |
|-------|----------------------|---------------|-------|------------|
| patch | v12.7.4 | auto PR (branch `rezus/sync-subtree-forgejo-runner-*`) | **auto**, only after PR CI is green (never red) | monorepo-ci, stamp |
| minor | v12.13.2 | auto PR (prepared) | **manual** — changelog/CVE review required | monorepo-ci, stamp, **smoke-macos** |
| major | v13.0.0 | advisory only | never (upstream majors are evaluations) | — |

Notes:

- Propagation prefers the pin's own `major.minor` lane (patch first), then the
  newest minor of the pin's major. A newer overall major is an advisory, never
  the bump.
- Only the patch class may auto-merge. Minor PRs wait for a human — review the
  upstream changelog + govulncheck delta, then merge.
- **smoke-macos** (minor and above): after the release built from the merged
  pin (`v*-rezus.*`), dispatch the smoke workflow on `tibrez/runner-macos-smoke`
  (git.rezus.cloud) against the new binary on the macOS runner host.
- After a merge the vendored-guards lag check resets automatically (it reads
  this pin file); releases ride the monorepo tag cycle — the engine never tags
  for a subtree, it only reports unreleased-pending.

## References

- Gitea labels & host mode: https://docs.gitea.com/runner/labels/
- Gitea runner releases (darwin assets): https://gitea.com/gitea/runner/releases
- GitHub self-hosted macOS runners: https://github.com/actions/runner/releases
  (osx-x64/osx-arm64 binaries; host execution; launchd service)
- Upstream linux-only gap: `build-release.yml` →
  `forgejo-build-publish/build` action, `platforms: linux/amd64,linux/arm64`
