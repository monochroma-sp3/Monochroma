#!/usr/bin/env bash
#
# Monochroma self-hosting setup.
#
# Prepares a fresh Ubuntu/Debian machine to build and run the full stack:
#   - Navidrome server (this repo, with the Tidal audio provider)
#   - Feishin web UI          (feishin/ -> built to feishin/out/web)
#   - hifi-api (Tidal audio)  (cloned from monochroma-sp3/hifi-api, port 8001)
#   - Playlist transferer     (transferer/, port 8080)
#
# It installs toolchains, builds each component, walks you through the HiFi API
# Tidal token, writes config from the *.example templates, and can install
# systemd services (Navidrome + hifi-api + transferer) so the whole stack runs
# on boot with no manual follow-up. Re-running is safe: existing pieces are
# detected and reused.
#
# Usage:
#   ./setup.sh            # interactive, from the repo root
#   ./setup.sh --help
#
# NOTE: this is a homelab tool. hifi-api talks to Tidal with YOUR account; keep
# it and its token.json private, and do not expose port 8001 to the internet.

set -euo pipefail

# ---------------------------------------------------------------------------
# Config (override via environment, e.g. GO_VERSION=1.26.1 ./setup.sh)
# ---------------------------------------------------------------------------
GO_VERSION="${GO_VERSION:-1.26.0}"
# pnpm 11.x reads/writes its store index via the Node "node:sqlite" builtin,
# which only exists from Node 22.5+ (stable since 22.13) — Node 20 will crash
# with "Error [ERR_UNKNOWN_BUILTIN_MODULE]: No such built-in module: node:sqlite".
NODE_MAJOR="${NODE_MAJOR:-22}"
NODE_MIN_VERSION="${NODE_MIN_VERSION:-22.13.0}"
HIFI_API_REPO="${HIFI_API_REPO:-https://github.com/monochroma-sp3/hifi-api.git}"
HIFI_API_PORT="${HIFI_API_PORT:-8001}"
NAVIDROME_PORT="${NAVIDROME_PORT:-4533}"
TRANSFERER_PORT="${TRANSFERER_PORT:-8080}"
BUILD_TAGS="netgo,sqlite_fts5"

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_DIR"

# ---------------------------------------------------------------------------
# Pretty logging
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'; GRN=$'\033[32m'
  YLW=$'\033[33m'; BLU=$'\033[34m'; RST=$'\033[0m'
else
  BOLD=""; DIM=""; RED=""; GRN=""; YLW=""; BLU=""; RST=""
fi
step()  { echo; echo "${BOLD}${BLU}==>${RST} ${BOLD}$*${RST}"; }
info()  { echo "    $*"; }
ok()    { echo "    ${GRN}✓${RST} $*"; }
warn()  { echo "    ${YLW}!${RST} $*"; }
die()   { echo "${RED}✗ $*${RST}" >&2; exit 1; }
ask()   { # ask "prompt" "default" -> echoes answer
  local p="$1" d="${2:-}" a
  if [ -n "$d" ]; then read -r -p "    ${p} [${d}]: " a || true; echo "${a:-$d}"
  else read -r -p "    ${p}: " a || true; echo "$a"; fi
}
confirm() { # confirm "prompt" -> returns 0 for yes
  local a; a="$(ask "$1 (y/N)" "")"; [[ "$a" =~ ^[Yy] ]]
}

[[ "${1:-}" == "--help" || "${1:-}" == "-h" ]] && { grep '^#' "$0" | sed 's/^# \{0,1\}//;1d'; exit 0; }

# ---------------------------------------------------------------------------
# 0. Sanity checks
# ---------------------------------------------------------------------------
step "Checking environment"
[ -f "$REPO_DIR/go.mod" ] || die "Run this from the Monochroma repo root (go.mod not found)."
[ -d "$REPO_DIR/feishin" ] || warn "feishin/ not found — the web UI build will be skipped."

OS="$(uname -s)"
if [ "$OS" != "Linux" ]; then
  warn "This script targets Ubuntu/Debian Linux. Detected: $OS."
  warn "It will still try to build if the toolchains are already installed."
fi
APT=""
command -v apt-get >/dev/null 2>&1 && APT="apt-get"
SUDO=""
[ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1 && SUDO="sudo"
ok "Repo: $REPO_DIR"

pkg_install() { # install system packages if apt is available
  [ -n "$APT" ] || { warn "No apt-get; please install manually: $*"; return 0; }
  $SUDO $APT update -qq
  $SUDO $APT install -y "$@"
}

# ---------------------------------------------------------------------------
# 1. System prerequisites
# ---------------------------------------------------------------------------
step "Installing system prerequisites"
pkg_install git curl ca-certificates build-essential ffmpeg \
            python3 python3-venv python3-pip
ok "Base packages present (git, curl, build tools, ffmpeg, python3)"
