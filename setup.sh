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

# ---------------------------------------------------------------------------
# 2. Go toolchain (>= $GO_VERSION; apt is usually too old for this fork)
# ---------------------------------------------------------------------------
step "Ensuring Go $GO_VERSION"
need_go=1
if command -v go >/dev/null 2>&1; then
  have="$(go version | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1)"
  # crude semver compare good enough for major.minor
  if [ "$(printf '%s\n%s\n' "$GO_VERSION" "$have" | sort -V | head -1)" = "$GO_VERSION" ]; then
    ok "Go $have already installed"; need_go=0
  else
    warn "Go $have is older than required $GO_VERSION; installing newer Go."
  fi
fi
if [ "$need_go" -eq 1 ]; then
  arch="$(uname -m)"; case "$arch" in x86_64) garch=amd64;; aarch64|arm64) garch=arm64;; *) die "Unsupported arch: $arch";; esac
  tarball="go${GO_VERSION}.linux-${garch}.tar.gz"
  info "Downloading https://go.dev/dl/${tarball}"
  curl -fsSL "https://go.dev/dl/${tarball}" -o "/tmp/${tarball}"
  $SUDO rm -rf /usr/local/go
  $SUDO tar -C /usr/local -xzf "/tmp/${tarball}"
  export PATH="/usr/local/go/bin:$PATH"
  if ! grep -q '/usr/local/go/bin' "$HOME/.profile" 2>/dev/null; then
    echo 'export PATH=$PATH:/usr/local/go/bin' >> "$HOME/.profile"
  fi
  ok "Installed $(go version)"
fi
export PATH="/usr/local/go/bin:$PATH"

# ---------------------------------------------------------------------------
# 3. Node.js + pnpm
#
# Node is required for BOTH web UIs: Navidrome's own admin UI at /app (ui/,
# built with npm) and the Feishin client at / (feishin/, built with pnpm).
# ---------------------------------------------------------------------------
step "Ensuring Node.js >= v$NODE_MIN_VERSION (pnpm needs the node:sqlite builtin)"
node_satisfies_min() {
  command -v node >/dev/null 2>&1 || return 1
  local have; have="$(node -v | sed 's/^v//')"
  [ "$(printf '%s\n%s\n' "$NODE_MIN_VERSION" "$have" | sort -V | head -1)" = "$NODE_MIN_VERSION" ]
}
if ! node_satisfies_min; then
  if command -v node >/dev/null 2>&1; then
    warn "Node $(node -v) is older than required v$NODE_MIN_VERSION; installing Node $NODE_MAJOR.x."
  fi
  if [ -n "$APT" ]; then
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | $SUDO bash -
    pkg_install nodejs
  else
    warn "Install Node.js >= v$NODE_MIN_VERSION manually, then re-run."
  fi
fi
if node_satisfies_min; then
  ok "Node $(node -v)"
else
  die "Node is still older than v$NODE_MIN_VERSION. Both web UIs need it to build;
    install Node >= v$NODE_MIN_VERSION manually and re-run."
fi

# pnpm via corepack (bundled with modern Node). Let corepack read the
# version pinned in feishin/package.json ("packageManager") rather than
# forcing "latest", so the build uses exactly the version the project
# expects.
if [ -d "$REPO_DIR/feishin" ]; then
  if command -v corepack >/dev/null 2>&1; then
    $SUDO corepack enable >/dev/null 2>&1 || corepack enable >/dev/null 2>&1 || true
  fi
  command -v pnpm >/dev/null 2>&1 || $SUDO npm install -g pnpm >/dev/null 2>&1 || true
  if command -v pnpm >/dev/null 2>&1 && pnpm_v="$(pnpm --version 2>/dev/null)" && [ -n "$pnpm_v" ]; then
    ok "pnpm $pnpm_v"
  else
    warn "pnpm did not report a version. If Node was just upgraded above, this" \
         "should resolve itself on the build step below; otherwise install" \
         "pnpm manually (https://pnpm.io/installation) and re-run."
  fi
fi

# ---------------------------------------------------------------------------
# 4. Build Navidrome's admin UI (served at /app)
#
# This MUST run before the Go build: ui/embed.go does `//go:embed build/*`, so
# the compiled binary bakes in whatever is in ui/build at build time. ui/build
# is gitignored, so a fresh clone has nothing there — skipping this step yields
# a server whose /app returns "404 page not found".
# ---------------------------------------------------------------------------
step "Building Navidrome's admin UI (ui/ -> ui/build, served at /app)"
(
  cd "$REPO_DIR/ui"
  if [ -f package-lock.json ]; then
    npm ci --no-audit --no-fund || npm install --no-audit --no-fund
  else
    npm install --no-audit --no-fund
  fi
  npm run build
) || die "Admin UI build failed. /app would return 404 without it."
[ -f "$REPO_DIR/ui/build/index.html" ] \
  || die "Expected ui/build/index.html after the admin UI build, but it is missing."
ok "Admin UI built (ui/build/index.html)"

# ---------------------------------------------------------------------------
# 5. Build the Feishin web UI (served at /)
# ---------------------------------------------------------------------------
if [ -d "$REPO_DIR/feishin" ]; then
  command -v pnpm >/dev/null 2>&1 \
    || die "pnpm not found, but feishin/ is present and needs it to build.
    Install pnpm (https://pnpm.io/installation) and re-run, or remove feishin/
    to serve Navidrome's own UI at / instead."
  step "Building the Feishin web UI"
  ( cd feishin && pnpm install --frozen-lockfile 2>/dev/null || pnpm install )
  ( cd feishin && pnpm build:web ) || die "Feishin web build failed."
  [ -f "$REPO_DIR/feishin/out/web/index.html" ] \
    || die "Expected feishin/out/web/index.html after the Feishin build, but it is missing."
  ok "Feishin web build at feishin/out/web"
else
  warn "feishin/ not found — Navidrome's own UI will be served at / instead."
fi

# ---------------------------------------------------------------------------
# 6. Build the Navidrome server
# ---------------------------------------------------------------------------
step "Building the Navidrome server (tags: $BUILD_TAGS)"
info "This can take a few minutes on first build (downloads Go modules)."
go build -tags="$BUILD_TAGS" -o navidrome . || die "Server build failed."
ok "Built ./navidrome"

# ---------------------------------------------------------------------------
# 7. hifi-api (Tidal audio backend)
# ---------------------------------------------------------------------------
step "Setting up hifi-api (Tidal audio backend, port $HIFI_API_PORT)"
if [ ! -d "$REPO_DIR/hifi-api/.git" ]; then
  info "Cloning $HIFI_API_REPO"
  git clone "$HIFI_API_REPO" "$REPO_DIR/hifi-api"
else
  ok "hifi-api already cloned"
fi
(
  cd "$REPO_DIR/hifi-api"
  [ -d venv ] || python3 -m venv venv
  # shellcheck disable=SC1091
  . venv/bin/activate
  pip install -q --upgrade pip
  info "Installing hifi-api dependencies"
  pip install -q -r requirements.txt
  [ -f tidal_auth/requirements.txt ] && pip install -q -r tidal_auth/requirements.txt || true
)
ok "hifi-api dependencies installed (venv at hifi-api/venv)"

echo
echo "    ${BOLD}${YLW}── HiFi API: Tidal token ─────────────────────────────────${RST}"
echo "    hifi-api needs a Tidal token (token.json) tied to YOUR Tidal account."
echo "    It is created by an interactive login helper. You need a paid Tidal"
echo "    plan (HiFi/HiFi Plus for lossless)."
echo
if [ -f "$REPO_DIR/hifi-api/token.json" ]; then
  ok "hifi-api/token.json already exists — leaving it as-is."
elif confirm "Run the Tidal login helper now to create token.json?"; then
  (
    cd "$REPO_DIR/hifi-api"
    # shellcheck disable=SC1091
    . venv/bin/activate
    info "Follow the on-screen instructions (usually: open a link, authorize the device)."
    python3 tidal_auth/tidal_auth.py || warn "Login helper exited non-zero; you can re-run it later."
  )
  [ -f "$REPO_DIR/hifi-api/token.json" ] && ok "token.json created" \
    || warn "token.json not found yet — re-run: cd hifi-api && . venv/bin/activate && python3 tidal_auth/tidal_auth.py"
else
  warn "Skipped. Create it later with:"
  echo "        cd hifi-api && . venv/bin/activate && python3 tidal_auth/tidal_auth.py"
fi
warn "Security: hifi-api binds 0.0.0.0:$HIFI_API_PORT. Firewall this port or bind it"
warn "to localhost — Navidrome only needs to reach it locally. Never expose it publicly."

# ---------------------------------------------------------------------------
# 8. Playlist transferer (transferer/, playlist import microservice)
# ---------------------------------------------------------------------------
transferer_admin_configured=0
if [ -d "$REPO_DIR/transferer" ]; then
  step "Setting up the playlist transferer (port $TRANSFERER_PORT)"
  (
    cd "$REPO_DIR/transferer"
    [ -d venv ] || python3 -m venv venv
    # shellcheck disable=SC1091
    . venv/bin/activate
    pip install -q --upgrade pip
    info "Installing transferer dependencies"
    pip install -q -r requirements.txt
  )
  ok "transferer dependencies installed (venv at transferer/venv)"

  echo
  echo "    ${BOLD}${YLW}── Playlist transferer: admin registration (optional) ────${RST}"
  echo "    The importer's /register endpoint (creating new Navidrome users from"
  echo "    the importer UI) needs a Navidrome admin account's credentials. The"
  echo "    core playlist-import feature does NOT need this — it only uses each"
  echo "    end user's own Subsonic login/token. Navidrome's first admin is"
  echo "    created interactively on its first web visit, so this script has no"
  echo "    way to know it in advance."
  echo
  if [ -f "$REPO_DIR/transferer/.env" ]; then
    ok "transferer/.env already exists — leaving it as-is."
    transferer_admin_configured=1
  elif confirm "Set ND_ADMIN_USER / ND_ADMIN_PASS now for the /register endpoint?"; then
    nd_admin_user="$(ask 'Navidrome admin username' '')"
    read -r -s -p "    Navidrome admin password: " nd_admin_pass || true
    echo
    if [ -n "$nd_admin_user" ] && [ -n "$nd_admin_pass" ]; then
      ( umask 177; cat > "$REPO_DIR/transferer/.env" <<ENVFILE
ND_ADMIN_USER=$nd_admin_user
ND_ADMIN_PASS=$nd_admin_pass
ENVFILE
      )
      ok "Wrote transferer/.env (mode 600, gitignored)."
      transferer_admin_configured=1
    else
      warn "Empty username/password — skipping. Set them later in transferer/.env."
    fi
  else
    warn "Skipped. Playlist import still works fine without this — only"
    warn "self-serve /register needs it. Set it later in transferer/.env."
  fi
  unset nd_admin_user nd_admin_pass 2>/dev/null || true
else
  warn "transferer/ not found — skipping the playlist importer."
fi

# ---------------------------------------------------------------------------
# 9. Configuration (navidrome.toml + .env.local from templates)
# ---------------------------------------------------------------------------
step "Configuration"
if [ ! -f "$REPO_DIR/navidrome.toml" ]; then
  cp navidrome.toml.example navidrome.toml
  server_name="$(ask 'Public name for your instance' 'Monochroma')"
  # Point the Feishin path at the freshly built UI, and set the hifi-api and
  # transferer URLs to match the ports this script is actually using.
  sed -i \
    -e "s#\"./feishin/out/web\"#\"$REPO_DIR/feishin/out/web\"#" \
    -e "s#http://localhost:8001#http://localhost:$HIFI_API_PORT#" \
    -e "s#http://localhost:8080/transfer#http://localhost:$TRANSFERER_PORT/transfer#" \
    navidrome.toml
  ok "Wrote navidrome.toml (edit it to enable LastFM, etc.)"
else
  ok "navidrome.toml already exists — leaving it as-is."
fi
if [ ! -f "$REPO_DIR/.env.local" ]; then
  cp .env.example .env.local
  sed -i "s#http://localhost:8001#http://localhost:$HIFI_API_PORT#" .env.local
  ok "Wrote .env.local"
else
  ok ".env.local already exists — leaving it as-is."
fi

# ---------------------------------------------------------------------------
# 10. Optional: systemd services
# ---------------------------------------------------------------------------
if [ -n "$APT" ] && command -v systemctl >/dev/null 2>&1 && confirm "Install systemd services (Monochroma + hifi-api + transferer) to run on boot?"; then
  step "Installing systemd services"
  run_user="${SUDO_USER:-$(id -un)}"
  cat <<UNIT | $SUDO tee /etc/systemd/system/hifi.service >/dev/null
[Unit]
Description=Monochroma HiFi API (Tidal)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$run_user
WorkingDirectory=$REPO_DIR/hifi-api
ExecStart=$REPO_DIR/hifi-api/venv/bin/python3 main.py
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  cat <<UNIT | $SUDO tee /etc/systemd/system/monochroma.service >/dev/null
[Unit]
Description=Monochroma (Navidrome) Service
After=network-online.target hifi.service
Wants=network-online.target

[Service]
Type=simple
User=$run_user
WorkingDirectory=$REPO_DIR
ExecStart=$REPO_DIR/navidrome
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  units_installed="hifi.service monochroma.service"
  if [ -d "$REPO_DIR/transferer" ]; then
    # Mirrors transferer/Dockerfile's CMD, binding 0.0.0.0 like Navidrome's own
    # port — the Feishin web UI calls /transfer directly from the end user's
    # browser (cross-origin), so this must NOT be loopback-only like hifi-api.
    cat <<UNIT | $SUDO tee /etc/systemd/system/transferer.service >/dev/null
[Unit]
Description=Monochroma Playlist Transferer
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$run_user
WorkingDirectory=$REPO_DIR/transferer
EnvironmentFile=-$REPO_DIR/transferer/.env
ExecStart=$REPO_DIR/transferer/venv/bin/gunicorn -b 0.0.0.0:$TRANSFERER_PORT -w 1 --worker-class gthread --threads 16 --timeout 600 app:app
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
    units_installed="$units_installed transferer.service"
  fi
  $SUDO systemctl daemon-reload
  # shellcheck disable=SC2086
  $SUDO systemctl enable --now $units_installed
  ok "Services installed and started ($units_installed)."
  info "Logs:  journalctl -u monochroma -f   |   journalctl -u hifi -f   |   journalctl -u transferer -f"
else
  info "Skipping systemd services."
fi

# ---------------------------------------------------------------------------
# 11. Done
# ---------------------------------------------------------------------------
step "Done"

transferer_run_line=""
transferer_tip="      - The playlist importer lives in transferer/ (see its app.py for how to run)."
if [ -d "$REPO_DIR/transferer" ]; then
  transferer_run_line="      3) ${BOLD}cd transferer && . venv/bin/activate && gunicorn -b 0.0.0.0:$TRANSFERER_PORT -w 1 --worker-class gthread --threads 16 --timeout 600 app:app${RST}
                                                             # playlist importer on :$TRANSFERER_PORT
"
  transferer_tip="      - Playlist importer running as transferer.service on :$TRANSFERER_PORT. Unlike
        hifi-api, it must stay reachable from end users' browsers (Feishin calls
        /transfer directly, cross-origin) — give it the same reverse-proxy /
        firewall treatment as Navidrome's own port, not loopback-only."
  if [ "$transferer_admin_configured" != "1" ]; then
    transferer_tip="$transferer_tip
      - Importer self-serve /register (new-user signup from the importer UI)
        needs ND_ADMIN_USER/ND_ADMIN_PASS, which you skipped. The core playlist
        transfer feature works fine without it; to enable /register later:
        edit $REPO_DIR/transferer/.env then run: systemctl restart transferer"
  fi
fi

cat <<EOF
    ${GRN}Monochroma is set up.${RST}

    To run manually (skip this if the systemd services are installed):
      1) ${BOLD}cd hifi-api && . venv/bin/activate && python3 main.py${RST}    # Tidal API on :$HIFI_API_PORT
      2) ${BOLD}./navidrome${RST}                                              # server on :$NAVIDROME_PORT
${transferer_run_line}
    Then open:  ${BOLD}http://localhost:$NAVIDROME_PORT${RST}   (Feishin UI; /app for the admin UI)

    Next steps / tips:
      - If you skipped the Tidal login, create token.json before playback works.
      - Put Navidrome behind a reverse proxy (nginx/Caddy) with TLS for remote use.
      - Keep hifi-api's port firewalled to localhost.
$transferer_tip
EOF
