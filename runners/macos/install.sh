#!/usr/bin/env bash
# Install a Forgejo Actions runner as a macOS user LaunchAgent.
#
# Idempotent: safe to re-run (skips download when the pinned binary is already
# in place; registration is only performed when no .runner file exists).
#
# Usage (on the Mac):
#   ./install.sh                          # interactive-safe: requires RUNNER_TOKEN below
#   RUNNER_TOKEN=… ./install.sh           # registration token (one-shot)
#   RUNNER_NAME=tib-mbp ./install.sh      # defaults to hostname -s
#
# Registration token (instance-level, admin):
#   fj api admin get-registration-token -H git.rezus.cloud
# … or the Forgejo admin UI (Site administration → Actions → Runners).
set -euo pipefail

# ── pinned inputs ────────────────────────────────────────────────────────────
# Binaries ship with the monorepo release (rezuscloud/forgejo): asset
# forgejo-runner-darwin-arm64.tar.gz on tag v<release>, binary stamped
# <upstream>-rezus.<N>. Bump the vendored tree via the vendor-bump workflow.
RELEASE_TAG="v16.0.2-rezus.1"     # monorepo release tag carrying the assets
RUNNER_UPSTREAM="v12.7.3"         # expected runner lineage; see runners/RUNNER_UPSTREAM
FORGEJO_URL="https://git.rezus.cloud"
LABELS="macos-arm64:host"         # name:schema — see runners/README.md contract
CAPACITY="1"                      # a laptop: one job at a time
# ─────────────────────────────────────────────────────────────────────────────

STATE_DIR="${STATE_DIR:-$HOME/.local/share/forgejo-runner}"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
PLIST="$HOME/Library/LaunchAgents/com.rezuscloud.forgejo-runner.plist"
LABEL_ID="com.rezuscloud.forgejo-runner"
RUNNER_NAME="${RUNNER_NAME:-$(hostname -s)}"

die() { echo "install.sh: $*" >&2; exit 1; }

uname -s | grep -q Darwin || die "this installer is for macOS hosts"
[ -d "$(dirname "$0")" ] # sanity

mkdir -p "$STATE_DIR" "$BIN_DIR" "$STATE_DIR/logs" "$STATE_DIR/cache" "$STATE_DIR/work"

# ── 1. binary: download pinned release, sha256-verify ───────────────────────
ASSET="forgejo-runner-darwin-arm64.tar.gz"
URL="https://github.com/rezuscloud/forgejo/releases/download/${RELEASE_TAG}/${ASSET}"
INSTALLED_UPSTREAM="$("$BIN_DIR"/forgejo-runner --version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"

if [ "$INSTALLED_UPSTREAM" = "$RUNNER_UPSTREAM" ]; then
  echo "· forgejo-runner already installed at $RUNNER_UPSTREAM lineage (bump RELEASE_TAG when the release moves)"
else
  echo "· fetching $ASSET from $RELEASE_TAG"
  curl -fsSL -o "$STATE_DIR/$ASSET" "$URL"
  curl -fsSL -o "$STATE_DIR/$ASSET.sha256" "$URL.sha256"
  # normalize checksum entry to the bare filename (some releases embed CI paths)
  ( cd "$STATE_DIR" && sed -i '' -E 's|(  ).*/|\1|' "$ASSET.sha256" && shasum -a 256 -c "$ASSET.sha256" )
  tar -xzf "$STATE_DIR/$ASSET" -C "$STATE_DIR"
  # the archive carries the platform-suffixed binary (fj convention)
  install -m755 "$STATE_DIR/${ASSET%.tar.gz}" "$BIN_DIR/forgejo-runner"
  rm -f "$STATE_DIR/$ASSET" "$STATE_DIR/$ASSET.sha256" "$STATE_DIR/${ASSET%.tar.gz}"
fi

# ── 2. config.yml from template ─────────────────────────────────────────────
TMPL="$(dirname "$0")/config.yml.tmpl"
[ -f "$TMPL" ] || die "config.yml.tmpl not found next to install.sh"
sed -e "s|@STATE_DIR@|$STATE_DIR|g" \
    -e "s|@LABELS@|$LABELS|g" \
    -e "s|@CAPACITY@|$CAPACITY|g" \
    "$TMPL" > "$STATE_DIR/config.yml"
echo "· wrote $STATE_DIR/config.yml (labels: $LABELS, capacity: $CAPACITY)"

# ── 3. register (only when no .runner file) ─────────────────────────────────
if [ ! -f "$STATE_DIR/.runner" ]; then
  [ -n "${RUNNER_TOKEN:-}" ] || die "no $STATE_DIR/.runner and RUNNER_TOKEN unset.
  Get one:  fj api admin get-registration-token -H $FORGEJO_URL"
  ( cd "$STATE_DIR" && "$BIN_DIR/forgejo-runner" register \
      --no-interactive \
      --instance "$FORGEJO_URL" \
      --token "$RUNNER_TOKEN" \
      --name "$RUNNER_NAME" \
      --labels "$LABELS" )
fi

# ── 4. LaunchAgent ──────────────────────────────────────────────────────────
TMPL="$(dirname "$0")/launchd.plist.tmpl"
[ -f "$TMPL" ] || die "launchd.plist.tmpl not found next to install.sh"
sed -e "s|@BIN@|$BIN_DIR/forgejo-runner|g" \
    -e "s|@STATE_DIR@|$STATE_DIR|g" \
    -e "s|@HOME@|$HOME|g" \
    -e "s|@LABEL_ID@|$LABEL_ID|g" \
    "$TMPL" > "$PLIST"
echo "· wrote $PLIST"

# bootout first if loaded (idempotent reload), then bootstrap
launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"
launchctl kickstart -k "gui/$(id -u)/$LABEL_ID"

echo "✓ runner '$RUNNER_NAME' installed as $LABEL_ID"
echo "  logs:   $STATE_DIR/logs/runner.err.log"
echo "  status: launchctl print gui/$(id -u)/$LABEL_ID | head -5"
echo "  server: $FORGEJO_URL (runner appears under Site administration → Actions)"
