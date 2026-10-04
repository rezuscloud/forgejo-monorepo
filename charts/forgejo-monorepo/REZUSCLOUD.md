# Rezuscloud Forgejo distribution

This chart is a vendored copy of the upstream [forgejo-helm](https://code.forgejo.org/forgejo-helm/forgejo-helm) chart, kept structurally identical so it is a **drop-in replacement**: every value the official chart accepts works here unchanged. The only differences are the default image repository, the chart `name`, and the `Chart.appVersion`, which track the Rezuscloud Forgejo builds published to GHCR.

It is published as an OCI chart, exactly like upstream:

```console
helm pull oci://ghcr.io/rezuscloud/charts/forgejo-monorepo --version <chart-version>
helm install forgejo oci://ghcr.io/rezuscloud/charts/forgejo-monorepo \
  --version <chart-version> -n forgejo -f values.yaml
```

Everything in the upstream `README.md` below (parameters, persistence, ingress, OAuth2, commit signing, …) applies verbatim.

## Versioning model (stable vs RC)

The chart version mirrors the upstream `forgejo-helm` numbering scheme (chart `N+2` → app `N`):

| Chart version | Forgejo appVersion | Image source | Channel |
|---------------|--------------------|--------------|---------|
| `18.x.y-rezus.N` | `16.x.y-rezus.N` | `ghcr.io/rezuscloud/forgejo` | stable |

Releases are produced by `.github/workflows/release.yml`, driven by `v*-rezus.*` tags. Each release publishes:

- `ghcr.io/rezuscloud/forgejo:<version>` (rootful)
- `ghcr.io/rezuscloud/forgejo:<version>-rootless`
- `oci://ghcr.io/rezuscloud/charts/forgejo-monorepo:<chart-version>`

Selecting which channel you deploy is just selecting the chart `--version`.

## Deploying an RC build for testing

Because the chart is a drop-in, testing an RC build is the standard Helm workflow: pick the chart version (and optionally pin the exact image). Two supported patterns:

### 1. Test a whole chart release (recommended)

Point your release at the chart version. This is identical to how you would test an upstream RC chart:

```console
helm install forgejo-test \
  oci://ghcr.io/rezuscloud/charts/forgejo-monorepo \
  --version <chart-version> \
  -n forgejo-test --create-namespace \
  -f values-rc-testing.yaml
```

### 2. Pin a specific image against any chart

To test an immutable image build (e.g. `16.0.5-dev.000087`) without changing the chart, override `image.tag`. Remember the chart appends the `-rootless` suffix when `image.rootless: true`, so pass a tag that already carries it or set `image.rootless: false`:

```yaml
image:
  rootless: true
  tag: "16.0.5-rezus.1-a6d4037af2a2-rootless"
```

See `values-rc-testing.yaml` for a complete non-production test overlay (single replica, a distinct `ROOT_URL`/namespace, relaxed logging).

## Production

`k8s-config/apps/forgejo/` consumes this chart via a Flux `HelmRelease` pinned to the production channel. The chart and image advance together as one SemVer unit so Flux rollback operates on a versioned chart release, not an ad-hoc image tag.

## Vendoring contract (upstream re-sync)

The chart is vendored from the upstream [forgejo-helm](https://code.forgejo.org/forgejo-helm/forgejo)
chart by the **vendor-bump workflow** (`.github/workflows/vendor-bump.yml`, component `chart`).
This directory holds the whole contract:

| File | Role |
|------|------|
| `UPSTREAM_CHART` | the upstream pin (one semver line, comments allowed) |
| `PATCHES.yaml` | the allowed delta: `preserve:` (locally-added paths) + `patches:` (differing files, each with a `signature` that must be present) |
| `REZUSCLOUD.md`, `values-rc-testing.yaml` | this distribution's docs + RC overlay (preserved across re-vendors) |

Rule: **every difference between this tree and the upstream chart at the pin
must be declared in PATCHES.yaml** — an undeclared diff, or a declared patch
whose signature vanished, is RED (unaccounted drift; re-vendoring is
mechanical). A declared patch that no longer differs is a WARN (stale entry).
The guard runs inline in `.github/workflows/vendored-guards.yml` (job `chart`).
The local chart version + appVersion are preserved across re-vendoring (chart
N+2 → app N, set at release time), so an upstream bump never bumps the local
release line. Bump classes follow the same matrix as `runner/` (see
`runners/README.md`): patch → auto PR + auto-merge after CI; minor → prepared
PR + manual merge; major → advisory only.
