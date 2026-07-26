#!/usr/bin/env python3
"""
nd-tools — Lightweight Navidrome utility service for Railway.

Routes:
  /           Menu showing available options
  /register   Create a new Navidrome user
  /transfer   Import playlists from external services
"""

import csv
import hashlib
import io
import json
import os
import re
import secrets
import subprocess
import tempfile
import threading
import time
import unicodedata
from difflib import SequenceMatcher
from itertools import product, zip_longest
from urllib.parse import urljoin, urlparse

import requests
from flask import Flask, request, jsonify, Response, stream_with_context

app = Flask(__name__)

# Reject oversized request bodies before they are read into memory (Flask
# answers with 413). Comfortably fits even a very large CSV playlist export.
app.config["MAX_CONTENT_LENGTH"] = 50 * 1024 * 1024  # 50MB

# Hosts accepted for YouTube playlist imports. Compared against the parsed
# hostname (never a substring match, which "evil.com/youtube.com" would pass).
_YOUTUBE_HOSTS = frozenset({
    "youtube.com",
    "www.youtube.com",
    "m.youtube.com",
    "music.youtube.com",
    "youtu.be",
    "www.youtu.be",
})


def _is_valid_youtube_url(url: str) -> bool:
    """True only for http(s) URLs whose host is a known YouTube host."""
    try:
        parsed = urlparse(url)
    except ValueError:
        return False
    if parsed.scheme not in ("http", "https"):
        return False
    return (parsed.hostname or "").lower() in _YOUTUBE_HOSTS


@app.after_request
def _transfer_cors(response):
    # The Monochroma (Feishin) web UI calls the transfer endpoints from the
    # Navidrome origin. Credentials travel in the form body (Subsonic
    # token+salt), never in cookies, so a wildcard origin is safe here.
    if request.path == "/transfer" or request.path.startswith("/transfer/job/"):
        response.headers["Access-Control-Allow-Origin"] = "*"
    return response

# ---------------------------------------------------------------------------
# Background job infrastructure — decouples long-running work from the
# HTTP connection so Railway's per-request timeout doesn't abort transfers.
# ---------------------------------------------------------------------------

_JOBS: dict = {}
_JOBS_LOCK = threading.Lock()
_JOB_TTL = 7200  # seconds before a completed/abandoned job is purged


class _Job:
    def __init__(self):
        self.events: list = []      # replay buffer for reconnecting clients
        self.done: bool = False
        self.created: float = time.time()

    def emit(self, line: str):
        with _JOBS_LOCK:
            self.events.append(line)

    def finish(self):
        with _JOBS_LOCK:
            self.done = True


# ---------------------------------------------------------------------------
# Config – set via Railway env vars
# ---------------------------------------------------------------------------
NAVIDROME_URL = os.getenv("NAVIDROME_URL", "http://localhost:4533")
ND_ADMIN_USER = os.getenv("ND_ADMIN_USER")
ND_ADMIN_PASS = os.getenv("ND_ADMIN_PASS")

# ---------------------------------------------------------------------------
# Helpers — Navidrome API
# ---------------------------------------------------------------------------

def _nd_api(path):
    """Build full URL for Navidrome's native REST API."""
    base = NAVIDROME_URL.rstrip("/")
    return f"{base}/api{path}"


def _nd_subsonic(endpoint, params=None):
    """Build full URL for Navidrome's Subsonic-compatible API."""
    base = NAVIDROME_URL.rstrip("/")
    return f"{base}/rest/{endpoint}"


def _admin_token():
    """Login as admin and return JWT token."""
    if not ND_ADMIN_USER or not ND_ADMIN_PASS:
        raise RuntimeError(
            "ND_ADMIN_USER / ND_ADMIN_PASS are not configured on this server."
        )
    base = NAVIDROME_URL.rstrip("/")
    r = requests.post(
        f"{base}/auth/login",
        json={"username": ND_ADMIN_USER, "password": ND_ADMIN_PASS},
        timeout=10,
    )
    r.raise_for_status()
    return r.json()["token"]


def _subsonic_params(username, password):
    """Build Subsonic auth query params using the salt+token method.

    ``password`` may be either:
      * a plain password string — a fresh salt+token pair is derived (the
        standalone page / registration flow), or
      * a ``{"token": ..., "salt": ...}`` dict — the fixed pair the Monochroma
        (Feishin) UI already holds from its own login, reused verbatim so the
        user never has to re-enter a password.
    """
    if isinstance(password, dict):
        salt = password.get("salt", "")
        token = password.get("token", "")
    else:
        salt = secrets.token_hex(6)
        token = hashlib.md5((password + salt).encode()).hexdigest()
    return {
        "u": username,
        "t": token,
        "s": salt,
        "v": "1.16.1",
        "c": "nd-tools",
        "f": "json",
    }


def _subsonic_search(username, password, query, song_count=5):
    """Search for songs via Subsonic API. Returns list of song entries."""
    params = _subsonic_params(username, password)
    params["query"] = query
    params["songCount"] = song_count
    params["artistCount"] = 0
    params["albumCount"] = 0
    r = requests.get(_nd_subsonic("search3"), params=params, timeout=15)
    r.raise_for_status()
    data = r.json()
    sr = data.get("subsonic-response", {}).get("searchResult3", {})
    return sr.get("song", [])


def _subsonic_create_playlist(username, password, name, song_ids):
    """Create a playlist via Subsonic API."""
    params = _subsonic_params(username, password)
    params["name"] = name
    r = requests.get(
        _nd_subsonic("createPlaylist"),
        params={**params, "songId": song_ids},
        timeout=15,
    )
    r.raise_for_status()
    return r.json()


# ---------------------------------------------------------------------------
# Shared HTML components
# ---------------------------------------------------------------------------

SHARED_CSS = """
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&display=swap');

* { margin: 0; padding: 0; box-sizing: border-box; }

:root {
  --bg: #0a0a0f;
  --surface: #12121a;
  --surface-hover: #1a1a25;
  --border: #1e1e2e;
  --text: #e4e4ec;
  --text-muted: #8888a0;
  --accent: #7c5cfc;
  --accent-light: #9b82ff;
  --success: #34d399;
  --error: #f87171;
  --warning: #fbbf24;
  --radius: 12px;
}

body {
  font-family: 'Inter', -apple-system, sans-serif;
  background: var(--bg);
  color: var(--text);
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}

.card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 16px;
  padding: 36px;
  width: 100%;
  max-width: 480px;
  animation: fadeUp 0.4s ease;
}

@keyframes fadeUp {
  from { opacity: 0; transform: translateY(12px); }
  to   { opacity: 1; transform: translateY(0); }
}

h1 {
  font-size: 1.6rem;
  font-weight: 700;
  margin-bottom: 4px;
  background: linear-gradient(135deg, var(--accent-light), var(--accent));
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
}

.subtitle {
  color: var(--text-muted);
  font-size: 0.85rem;
  margin-bottom: 28px;
}

label {
  display: block;
  font-size: 0.8rem;
  font-weight: 500;
  color: var(--text-muted);
  margin-bottom: 6px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

input[type="text"],
input[type="email"],
input[type="password"],
input[type="url"],
input[type="file"],
select {
  width: 100%;
  padding: 11px 14px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  font-family: inherit;
  font-size: 0.9rem;
  transition: border-color 0.2s;
  outline: none;
}

input:focus, select:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgba(124,92,252,0.15);
}

.field { margin-bottom: 18px; }

button, .btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  width: 100%;
  padding: 12px 20px;
  border: none;
  border-radius: var(--radius);
  background: linear-gradient(135deg, var(--accent), var(--accent-light));
  color: #fff;
  font-family: inherit;
  font-size: 0.95rem;
  font-weight: 600;
  cursor: pointer;
  transition: transform 0.15s, opacity 0.2s;
  text-decoration: none;
  text-align: center;
}

button:hover, .btn:hover {
  transform: translateY(-1px);
  opacity: 0.92;
}

button:active, .btn:active { transform: translateY(0); }

.btn-outline {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text);
}

.btn-outline:hover {
  background: var(--surface-hover);
  border-color: var(--accent);
}

.msg {
  padding: 12px 16px;
  border-radius: var(--radius);
  font-size: 0.85rem;
  margin-bottom: 18px;
  line-height: 1.5;
}

.msg-success { background: rgba(52,211,153,0.1); border: 1px solid rgba(52,211,153,0.3); color: var(--success); }
.msg-error   { background: rgba(248,113,113,0.1); border: 1px solid rgba(248,113,113,0.3); color: var(--error); }
.msg-info    { background: rgba(124,92,252,0.1);  border: 1px solid rgba(124,92,252,0.3);  color: var(--accent-light); }
.msg-warn    { background: rgba(251,191,36,0.1);  border: 1px solid rgba(251,191,36,0.3);  color: var(--warning); }

.nav-links {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 24px;
}

.nav-link {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 16px 18px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  text-decoration: none;
  transition: all 0.2s;
}

.nav-link:hover {
  border-color: var(--accent);
  background: var(--surface-hover);
  transform: translateX(4px);
}

.nav-icon {
  font-size: 1.4rem;
  width: 36px;
  height: 36px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(124,92,252,0.12);
  border-radius: 10px;
}

.nav-label { font-weight: 600; font-size: 0.95rem; }
.nav-desc  { font-size: 0.78rem; color: var(--text-muted); margin-top: 2px; }

hr {
  border: none;
  border-top: 1px solid var(--border);
  margin: 20px 0;
}

.back-link {
  display: inline-block;
  margin-top: 16px;
  font-size: 0.82rem;
  color: var(--text-muted);
  text-decoration: none;
}
.back-link:hover { color: var(--accent-light); }

/* Transfer-specific */
.source-tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 20px;
  flex-wrap: wrap;
}

.source-tab {
  flex: 1;
  min-width: 60px;
  padding: 9px 6px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text-muted);
  font-size: 0.72rem;
  font-weight: 600;
  text-align: center;
  cursor: pointer;
  transition: all 0.2s;
  text-transform: uppercase;
  letter-spacing: 0.3px;
}

.source-tab:hover { border-color: var(--accent); color: var(--text); }
.source-tab.active {
  background: rgba(124,92,252,0.15);
  border-color: var(--accent);
  color: var(--accent-light);
}

.panel { display: none; }
.panel.active { display: block; }

.progress {
  margin-top: 16px;
  padding: 14px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 0.82rem;
  color: var(--text-muted);
  max-height: 200px;
  overflow-y: auto;
}

.progress-bar-container {
  margin-top: 16px;
  height: 6px;
  background: var(--surface-hover);
  border-radius: 4px;
  overflow: hidden;
  display: none;
}
.progress-bar-fill {
  height: 100%;
  width: 0%;
  background: linear-gradient(90deg, var(--accent-light), var(--accent));
  transition: width 0.3s ease;
}

.progress-line { padding: 3px 0; }
.progress-line.ok   { color: var(--success); }
.progress-line.fail { color: var(--error); }
.progress-line.skip { color: var(--warning); }

.spinner {
  display: inline-block;
  width: 14px; height: 14px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
  margin-right: 6px;
}

@keyframes spin { to { transform: rotate(360deg); } }

.file-input-wrapper {
  position: relative;
}

input[type="file"] {
  padding: 10px 14px;
}

input[type="file"]::file-selector-button {
  padding: 6px 14px;
  margin-right: 12px;
  background: rgba(124,92,252,0.2);
  border: 1px solid var(--accent);
  border-radius: 6px;
  color: var(--accent-light);
  font-family: inherit;
  font-size: 0.8rem;
  font-weight: 500;
  cursor: pointer;
}

/* Spotify2 section */
.s2-fields { display: flex; flex-direction: column; gap: 12px; }
.s2-row    { display: flex; gap: 10px; }
.s2-row .field { flex: 1; margin-bottom: 0; }
"""

def _page(title, body_html):
    return f"""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{title} — nd-tools</title>
  <style>{SHARED_CSS}</style>
</head>
<body>
{body_html}
</body>
</html>"""


# ============================================================================
#  /  — Home menu
# ============================================================================

@app.route("/")
def home():
    return _page("Home", """
<div class="card">
  <h1>Monochroma</h1>
  <p class="subtitle">Tools for Monochroma</p>

  <div class="nav-links">
    <a class="nav-link" href="/register">
      <span class="nav-icon">👤</span>
      <div>
        <div class="nav-label">Registration</div>
        <div class="nav-desc">Create a new account</div>
      </div>
    </a>
    <a class="nav-link" href="/transfer">
      <span class="nav-icon">📥</span>
      <div>
        <div class="nav-label">Transfer Playlist</div>
        <div class="nav-desc">Import playlists from Spotify, Apple Music, YouTube and more</div>
      </div>
    </a>
  </div>
</div>
""")


# ============================================================================
#  /register  — User registration
# ============================================================================

@app.route("/register", methods=["GET"])
def register_form():
    return _page("Registration", """
<div class="card">
  <h1>Create Account</h1>
  <p class="subtitle">Register a new user on Monochroma</p>

  <form id="regForm" onsubmit="return doRegister(event)">
    <div class="field">
      <label for="username">Username</label>
      <input type="text" id="username" name="username" required
             pattern="\\S+"
             title="Username cannot contain spaces"
             placeholder="e.g. shinji">
    </div>
    <div class="field">
      <label for="email">Email</label>
      <input type="email" id="email" name="email"
             placeholder="e.g. shinjigetin.therobot@evamail.com">
    </div>
    <div class="field">
      <label for="password">Password</label>
      <input type="password" id="password" name="password" required minlength="4"
             placeholder="At least 4 characters">
    </div>
    <div class="field">
      <label for="password2">Confirm Password</label>
      <input type="password" id="password2" required minlength="4"
             placeholder="'Yep, that\\'s my password!'">
    </div>
    <div id="regMsg"></div>
    <button type="submit" id="regBtn">Create Account</button>
  </form>
  <a href="/" class="back-link">← Back to menu</a>
</div>
<script>
async function doRegister(e) {
  e.preventDefault();
  const btn = document.getElementById('regBtn');
  const msg = document.getElementById('regMsg');
  const u = document.getElementById('username').value.trim();
  const em = document.getElementById('email').value.trim();
  const p1 = document.getElementById('password').value;
  const p2 = document.getElementById('password2').value;

  if (/\\s/.test(u)) {
    msg.className = 'msg msg-error';
    msg.textContent = 'Username cannot contain spaces.';
    return false;
  }
  if (p1 !== p2) {
    msg.className = 'msg msg-error';
    msg.textContent = 'Passwords do not match.';
    return false;
  }

  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> Creating…';
  msg.className = ''; msg.textContent = '';

  try {
    const r = await fetch('/register', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({username: u, email: em, password: p1})
    });
    const d = await r.json();
    if (r.ok) {
      msg.className = 'msg msg-success';
      msg.textContent = '✓ Account created successfully! You can now log in with your credentials.';
      document.getElementById('regForm').reset();
    } else {
      msg.className = 'msg msg-error';
      msg.textContent = d.error || 'Error during creation.';
    }
  } catch(err) {
    msg.className = 'msg msg-error';
    msg.textContent = 'Connection error. Please try again.';
  }
  btn.disabled = false;
  btn.innerHTML = 'Create Account';
  return false;
}
</script>
""")


@app.route("/register", methods=["POST"])
def register_action():
    data = request.get_json(force=True, silent=True)
    if not isinstance(data, dict):
        return jsonify(error="Invalid request body."), 400
    username = (data.get("username") or "").strip()
    email = (data.get("email") or "").strip()
    password = data.get("password") or ""

    # Validation
    if not username:
        return jsonify(error="Username is required."), 400
    if " " in username:
        return jsonify(error="Username cannot contain spaces."), 400
    if len(password) < 4:
        return jsonify(error="Password must be at least 4 characters long."), 400

    try:
        token = _admin_token()
    except Exception as e:
        return jsonify(error=f"Cannot connect to Monochroma: {e}"), 502

    # Create user via Navidrome native REST API
    try:
        r = requests.post(
            _nd_api("/user"),
            json={
                "userName": username,
                "name": username,
                "email": email,
                "password": password,
                "isAdmin": False,
            },
            headers={"x-nd-authorization": f"Bearer {token}"},
            timeout=10,
        )

        if r.status_code == 200 or r.status_code == 201:
            return jsonify(ok=True, message="User created successfully.")
        else:
            # Try to extract error message
            try:
                err_data = r.json()
                # Handle Navidrome field-level validation errors (react-admin format)
                validation = err_data.get("errors")
                if isinstance(validation, dict) and validation:
                    if validation.get("userName") == "ra.validation.unique":
                        return jsonify(error="Username is already taken!"), 409
                    if validation.get("email") == "ra.validation.unique":
                        return jsonify(error="Email is already in use!"), 409
                    # Generic fallback for other validation errors
                    field, code = next(iter(validation.items()))
                    return jsonify(
                        error=f"Invalid field: {field} ({code})"
                    ), 400
                err_msg = err_data.get("error") or err_data.get("message") or r.text
            except Exception:
                err_msg = r.text
            return jsonify(error=f"Error from Monochroma: {err_msg}"), r.status_code
    except Exception as e:
        return jsonify(error=f"Error during creation: {e}"), 500


# ============================================================================
#  /transfer  — Playlist import
# ============================================================================

@app.route("/transfer", methods=["GET"])
def transfer_form():
    return _page("Transfer Playlist", """
<div class="card" style="max-width:540px">
  <h1>Transfer Playlist</h1>
  <p class="subtitle">Import your playlists into your Monochroma account</p>

  <!-- Navidrome credentials -->
  <div class="msg msg-info" style="margin-bottom:20px">
    Enter your Monochroma credentials to link the playlists to your account.
  </div>
  <div class="s2-row">
    <div class="field">
      <label for="nd_user">Monochroma Username</label>
      <input type="text" id="nd_user" placeholder="Your username">
    </div>
    <div class="field">
      <label for="nd_pass">Password</label>
      <input type="password" id="nd_pass" placeholder="Your password">
    </div>
  </div>
  <hr>

  <!-- Source tabs -->
  <div class="source-tabs">
    <div class="source-tab active" onclick="switchTab('spotify', this)">Spotify</div>
    <div class="source-tab" onclick="switchTab('apple', this)">Apple Music</div>
    <div class="source-tab" onclick="switchTab('youtube', this)">YouTube</div>
  </div>

  <!-- Spotify (Exportify CSV) -->
  <div class="panel active" id="panel-spotify">
    <div class="msg msg-info">
      Export your playlists from Spotify using
      <a href="https://exportify.net" target="_blank" style="color:var(--accent-light)">Exportify</a>,
      then upload the CSV file below.
    </div>
    <div class="field">
      <label for="spotify_file">CSV File (Exportify)</label>
      <input type="file" id="spotify_file" accept=".csv">
    </div>
    <div class="field">
      <label for="spotify_name">Playlist Name</label>
      <input type="text" id="spotify_name" placeholder="e.g. Svmmer Vibes✨🤍">
    </div>
  </div>

  <!-- Apple Music (TuneMyMusic CSV) -->
  <div class="panel" id="panel-apple">
    <div class="msg msg-info">
      Export your playlists from Apple Music using
      <a href="https://www.tunemymusic.com" target="_blank" style="color:var(--accent-light)">TuneMyMusic</a>,
      then upload the CSV file below.
    </div>
    <div class="field">
      <label for="apple_file">CSV File (TuneMyMusic)</label>
      <input type="file" id="apple_file" accept=".csv">
    </div>
    <div class="field">
      <label for="apple_name">Playlist Name</label>
      <input type="text" id="apple_name" placeholder="e.g. Favs 2026">
    </div>
  </div>

  <!-- YouTube / YouTube Music -->
  <div class="panel" id="panel-youtube">
    <div class="msg msg-info">
      Paste the link of a playlist from YouTube or YouTube Music. The songs will be searched
      in your Monochroma account.
    </div>
    <div class="field">
      <label for="yt_url">Playlist Link</label>
      <input type="url" id="yt_url" placeholder="https://www.youtube.com/playlist?list=…">
    </div>
    <div class="field">
      <label for="yt_name">Playlist Name (optional)</label>
      <input type="text" id="yt_name" placeholder="Original name will be used if left empty">
    </div>
  </div>

  <div id="transferMsg"></div>
  <button id="transferBtn" onclick="doTransfer()">Import Playlist</button>
  <div id="progressBarContainer" class="progress-bar-container">
    <div id="progressBarFill" class="progress-bar-fill"></div>
  </div>
  <div id="progressBox" class="progress" style="display:none"></div>
  <a href="/" class="back-link">← Back to menu</a>
</div>

<script>
let activeTab = 'spotify';

function switchTab(tab, el) {
  activeTab = tab;
  document.querySelectorAll('.source-tab').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
  el.classList.add('active');
  document.getElementById('panel-' + tab).classList.add('active');
}

// ----- Main transfer -----
async function doTransfer() {
  const btn = document.getElementById('transferBtn');
  const msg = document.getElementById('transferMsg');
  const progress = document.getElementById('progressBox');
  const ndUser = document.getElementById('nd_user').value.trim();
  const ndPass = document.getElementById('nd_pass').value;

  msg.className = ''; msg.textContent = '';
  progress.style.display = 'none'; progress.innerHTML = '';

  if (!ndUser || !ndPass) {
    msg.className = 'msg msg-error';
    msg.textContent = 'Enter your Monochroma credentials.';
    return;
  }

  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> Importing…';

  const formData = new FormData();
  formData.append('nd_user', ndUser);
  formData.append('nd_pass', ndPass);
  formData.append('source', activeTab);

  if (activeTab === 'spotify') {
    const file = document.getElementById('spotify_file').files[0];
    const name = document.getElementById('spotify_name').value.trim();
    if (!file) { showErr('Select a CSV file.'); return; }
    if (!name) { showErr('Enter a name for the playlist.'); return; }
    formData.append('file', file);
    formData.append('playlist_name', name);
  } else if (activeTab === 'apple') {
    const file = document.getElementById('apple_file').files[0];
    const name = document.getElementById('apple_name').value.trim();
    if (!file) { showErr('Select a CSV file.'); return; }
    if (!name) { showErr('Enter a name for the playlist.'); return; }
    formData.append('file', file);
    formData.append('playlist_name', name);
  } else if (activeTab === 'youtube') {
    const url = document.getElementById('yt_url').value.trim();
    if (!url) { showErr('Enter the playlist link.'); return; }
    formData.append('yt_url', url);
    formData.append('playlist_name', document.getElementById('yt_name').value.trim());
  }

  function showErr(text) {
    msg.className = 'msg msg-error'; msg.textContent = text;
    btn.disabled = false; btn.innerHTML = 'Import Playlist';
  }

  try {
    const r = await fetch('/transfer', { method: 'POST', body: formData });
    if (!r.ok) {
      try {
        const d = await r.json();
        showErr(d.error || "Error during import.");
      } catch (e) {
        showErr("Error during import.");
      }
      return;
    }
    const { job_id } = await r.json();
    await streamJob(job_id);
  } catch(e) {
    msg.className = 'msg msg-error';
    msg.textContent = 'Connection error. Please try again.';
  }

  btn.disabled = false;
  btn.innerHTML = 'Import Playlist';
}

async function streamJob(job_id) {
  const msg = document.getElementById('transferMsg');
  const progress = document.getElementById('progressBox');
  const pbContainer = document.getElementById('progressBarContainer');
  const pbFill = document.getElementById('progressBarFill');

  pbContainer.style.display = 'block';
  pbFill.style.width = '0%';
  progress.style.display = 'block';

  let offset = 0;
  let finished = false;

  while (!finished) {
    try {
      const r = await fetch('/transfer/job/' + job_id + '?from=' + offset);
      if (!r.ok) {
        msg.className = 'msg msg-error';
        msg.textContent = 'Job not found or expired. Please try again.';
        return;
      }

      const reader = r.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        const lines = buffer.split('\\n');
        buffer = lines.pop();

        for (const line of lines) {
          if (!line.trim()) continue;
          try {
            const data = JSON.parse(line);

            if (data.type === 'ping') continue; // keepalive — not counted in offset

            offset++;

            if (data.type === 'start') {
              pbFill.style.width = '0%';
            } else if (data.type === 'progress') {
              const pct = Math.min(100, Math.round((data.current / data.total) * 100));
              pbFill.style.width = pct + '%';
              if (data.detail) {
                const cls = data.detail.startsWith('✓') ? 'ok' : data.detail.startsWith('✗') ? 'fail' : data.detail.startsWith('⚠') ? 'skip' : '';
                progress.innerHTML += '<div class="progress-line ' + cls + '">' + data.detail + '</div>';
                progress.scrollTop = progress.scrollHeight;
              }
            } else if (data.type === 'info') {
              progress.innerHTML += '<div class="progress-line" style="color:var(--accent-light)">' + data.message + '</div>';
              progress.scrollTop = progress.scrollHeight;
            } else if (data.type === 'done') {
              msg.className = 'msg msg-success';
              msg.textContent = data.message || 'Import completed!';
              finished = true;
            } else if (data.type === 'error') {
              msg.className = 'msg msg-error';
              msg.textContent = data.error || "Error.";
              pbContainer.style.display = 'none';
              finished = true;
            }
          } catch(e) {
            console.error("JSON parse error on line:", line);
          }
        }

        if (finished) { reader.cancel(); break; }
      }
    } catch(e) {
      if (!finished) {
        // Connection dropped — reconnect and resume from last offset
        progress.innerHTML += '<div class="progress-line skip">⚠ Connection interrupted — reconnecting…</div>';
        progress.scrollTop = progress.scrollHeight;
        await new Promise(res => setTimeout(res, 3000));
      }
    }
  }
}
</script>
""")


def _json_line(data):
    return json.dumps(data) + "\n"

# ----- Main transfer endpoint — creates a background job and returns job_id -----
@app.route("/transfer", methods=["POST"])
def transfer_action():
    nd_user = request.form.get("nd_user", "").strip()
    nd_pass = request.form.get("nd_pass", "")
    # Subsonic token+salt pair, sent by the Monochroma (Feishin) UI in place of
    # the password: the user is already logged in there, so we reuse that
    # session's credentials instead of asking again. When present, we thread the
    # fixed pair (as a dict) through in place of the password.
    nd_token = request.form.get("nd_token", "").strip()
    nd_salt = request.form.get("nd_salt", "").strip()
    if nd_token and nd_salt:
        nd_pass = {"token": nd_token, "salt": nd_salt}
    source = request.form.get("source", "")

    # Cheap non-empty credential check BEFORE any job object or thread is
    # created, so unauthenticated requests can't spawn background work. The
    # authoritative check (a Subsonic ping) still runs inside run_job(), where
    # its latency doesn't block the request handler.
    if not nd_user or not nd_pass:
        return jsonify(error="Missing credentials."), 400

    playlist_name = request.form.get("playlist_name", "").strip()
    yt_url = request.form.get("yt_url", "").strip()

    if source in ["spotify", "apple"]:
        f_content = None
        f = request.files.get("file")
        if f:
            try:
                f_content = f.read().decode("utf-8-sig")
            except Exception:
                pass
    else:
        f_content = None

    # Create job and launch background worker thread
    job_id = secrets.token_urlsafe(16)
    job = _Job()
    with _JOBS_LOCK:
        _JOBS[job_id] = job
        # Purge stale jobs
        now = time.time()
        stale = [k for k, v in _JOBS.items() if now - v.created > _JOB_TTL]
        for k in stale:
            del _JOBS[k]

    def run_job():
        try:
            if not nd_user or not nd_pass:
                job.emit(_json_line({"type": "error", "error": "Missing credentials."}))
                return

            try:
                params = _subsonic_params(nd_user, nd_pass)
                r = requests.get(_nd_subsonic("ping"), params=params, timeout=10)
                r.raise_for_status()
                resp = r.json()
                if resp.get("subsonic-response", {}).get("status") != "ok":
                    job.emit(_json_line({"type": "error", "error": "Invalid Monochroma credentials."}))
                    return
            except Exception:
                job.emit(_json_line({"type": "error", "error": "Cannot verify Monochroma credentials."}))
                return

            if source == "spotify":
                gen = _handle_csv_stream(nd_user, nd_pass, "exportify", f_content, playlist_name)
            elif source == "apple":
                gen = _handle_csv_stream(nd_user, nd_pass, "tunemymusic", f_content, playlist_name)
            elif source == "youtube":
                gen = _handle_youtube_stream(nd_user, nd_pass, yt_url, playlist_name)
            else:
                job.emit(_json_line({"type": "error", "error": "Invalid source."}))
                return

            for line in gen:
                job.emit(line)
        except Exception as e:
            # Without this, an uncaught exception would end the thread silently
            # and the client would just see progress freeze with no explanation.
            job.emit(_json_line({"type": "error", "error": f"Unexpected error: {e}"}))
        finally:
            job.finish()

    threading.Thread(target=run_job, daemon=True).start()
    return jsonify(job_id=job_id)


# ----- Job progress stream — client connects here and reconnects if dropped -----
@app.route("/transfer/job/<job_id>")
def transfer_job_stream(job_id):
    try:
        from_offset = int(request.args.get("from", 0))
    except (TypeError, ValueError):
        from_offset = 0
    if from_offset < 0:
        from_offset = 0

    with _JOBS_LOCK:
        job = _JOBS.get(job_id)

    if not job:
        return jsonify(error="Job not found or expired."), 404

    # Cap stream duration so the client periodically reconnects with a fresh
    # HTTP request — keeps us comfortably under any gunicorn/proxy timeout.
    MAX_STREAM_DURATION = 120

    @stream_with_context
    def generate():
        offset = from_offset
        started = time.time()
        while True:
            with _JOBS_LOCK:
                new_events = job.events[offset:]
                is_done = job.done

            if new_events:
                for ev in new_events:
                    yield ev
                offset += len(new_events)
                continue

            if is_done:
                return
            if time.time() - started > MAX_STREAM_DURATION:
                return  # client will reconnect with ?from=offset

            yield _json_line({"type": "ping"})
            time.sleep(1)

    return Response(
        generate(),
        mimetype="application/x-ndjson",
        headers={
            "X-Accel-Buffering": "no",
            "Cache-Control": "no-cache",
            "Connection": "keep-alive",
        },
    )


# ---------------------------------------------------------------------------
# Track matching — SpotDL-inspired scoring
# (slugify + token-set names, artist-set match, common-word gate, forbidden-
#  word penalty). Stdlib-only: SequenceMatcher.ratio() mirrors fuzz.ratio().
# ---------------------------------------------------------------------------

# Decorative suffixes Spotify/Apple/YouTube commonly append to titles
_NOISE_PATTERNS = [
    r"\(feat\.?[^)]*\)", r"\[feat\.?[^\]]*\]",
    r"\(ft\.?[^)]*\)", r"\[ft\.?[^\]]*\]",
    r"\(with [^)]*\)", r"\[with [^\]]*\]",
    r"\(featuring [^)]*\)", r"\[featuring [^\]]*\]",
    r"\bfeat\.?\s+[^-(\[]*",
    r"\bft\.?\s+[^-(\[]*",
    r"\bfeaturing\b\s+[^-(\[]*",
    # Movie / OST source tags: (From "Movie"), (From the OST), (From X)
    r"\(from\s+[^)]*\)",
    r"\[from\s+[^\]]*\]",
    r"\(ost\)", r"\[ost\]",
    # Any parenthetical containing soundtrack/ost — catches things like
    # "(VIVINOS - ALNST Original Soundtrack Part.4)"
    r"\([^)]*\bsoundtrack\b[^)]*\)",
    r"\([^)]*\bost\b[^)]*\)",
    # Any parenthetical whose only purpose is a version marker
    r"\([^)]*\b(?:live|acoustic|demo|cover|instrumental|remix|"
    r"unplugged|reprise|karaoke|sped\s*up|slowed)\b[^)]*\)",
    # Parentheticals that contain only non-ASCII chars — Korean/Japanese
    # member-name annotations like "(츄)" or "(チェウォン)"
    r"\([^\x00-\x7f]+\)",
    # Remasters / mixes
    r"-\s*\d{4}\s*remaster(ed)?(\s*version)?",
    r"-\s*remaster(ed)?(\s*\d{4})?(\s*version)?",
    r"\(\d{4}\s*remaster(ed)?\)",
    r"\(remaster(ed)?(\s*\d{4})?\)",
    r"\(remaster(ed)?\s*version\)",
    r"-\s*\d{4}\s*(re)?mix",
    r"\(\d{4}\s*(re)?mix\)",
    # Video / quality tags
    r"\(official\s*(music\s*)?(video|audio|lyric\s*video|visualizer)\)",
    r"\[official\s*(music\s*)?(video|audio|lyric\s*video|visualizer)\]",
    r"\(lyric[s]?\s*video\)", r"\[lyric[s]?\s*video\]",
    r"\(audio\)", r"\[audio\]",
    r"\(video\)", r"\[video\]",
    r"\(visualizer\)", r"\[visualizer\]",
    r"\(hd\)", r"\[hd\]", r"\(4k\)", r"\[4k\]",
    r"\(explicit\)", r"\[explicit\]",
    r"\(clean\)", r"\[clean\]",
    r"\(radio\s*edit\)",
    r"\(extended\s*mix\)",
    r"\(bonus\s*track\)", r"\[bonus\s*track\]",
    r"\(deluxe(\s*edition)?\)",
    r"\(single\s*version\)",
    r"\(album\s*version\)",
]
_NOISE_RE = re.compile("|".join(_NOISE_PATTERNS), re.IGNORECASE)

# Aggressive trailing dash: strip everything from " - " onwards if the suffix
# contains any version-marker word. Catches:
#   - "Lover, You Should've Come Over - Live At Columbia Records Radio Hour…"
#   - "Hopelessly Devoted To You - From "Grease""
#   - "Cupid - Twin Version"
#   - "Don't Look Back In Anger - Remastered"
# (The previous narrow _TRAIL_RE only matched a single word; this also handles
# everything trailing it.)
_TRAIL_RE = re.compile(
    r"\s+-\s+[^-]*\b(?:"
    r"live|remix|remaster(?:ed)?|version|mix|edit|from|"
    r"soundtrack|ost|cover|acoustic|instrumental|demo|"
    r"original|mono|stereo|deluxe|extended|radio|"
    r"single|album|reprise|unplugged|karaoke|sped|slowed"
    r")\b.*$",
    re.IGNORECASE,
)

# Splits artist strings into individual performers
_ARTIST_SPLIT_RE = re.compile(
    r"\s*(?:,|;|&|\bfeat\.?\b|\bft\.?\b|\bfeaturing\b|\bwith\b|\bvs\.?\b|\bx\b|\+|/)\s*",
    re.IGNORECASE,
)

# Words that signal a different VERSION of the song. A mismatch on either side
# (present on candidate but not in source, or vice versa) costs 15 points.
_FORBIDDEN_WORDS = {
    "remix", "remastered", "remaster", "live", "acoustic",
    "instrumental", "cover", "karaoke", "slowed", "reverbed",
    "reverb", "bassboost", "bassboosted", "concert", "acapella",
    "sped", "spedup", "8d",
}


def _strip_accents(s):
    return "".join(c for c in unicodedata.normalize("NFKD", s) if not unicodedata.combining(c))


def _slugify(text):
    """Lowercase ASCII slug with `-` between alphanumeric runs (mirrors spotDL)."""
    if not text:
        return ""
    text = _strip_accents(text).lower()
    text = re.sub(r"[^a-z0-9]+", "-", text)
    return text.strip("-")


def _normalize(s):
    """Spaces variant of slug — used for free-text Subsonic search queries."""
    if not s:
        return ""
    s = _strip_accents(s).lower()
    s = re.sub(r"[^\w\s]", " ", s, flags=re.UNICODE)
    s = re.sub(r"\s+", " ", s).strip()
    return s


def _ratio(a, b):
    """0–100 string similarity; mirrors rapidfuzz.fuzz.ratio() using stdlib."""
    if not a and not b:
        return 100.0
    if not a or not b:
        return 0.0
    return SequenceMatcher(None, a, b).ratio() * 100.0


def _clean_title(title):
    """Strip decorative parentheticals and trailing version annotations."""
    if not title:
        return ""
    # Normalize typographic quotes/apostrophes to ASCII before pattern matching
    title = title.translate(str.maketrans("\u2018\u2019\u201a\u201b", "''''"))
    title = title.translate(str.maketrans("\u201c\u201d\u201e\u201f", '""""'))
    cleaned = _NOISE_RE.sub(" ", title)
    cleaned = _TRAIL_RE.sub("", cleaned)
    cleaned = re.sub(r"\s+", " ", cleaned).strip(" -\u2013\u2014\t\"'")
    return cleaned or title


def _split_artists(artist):
    """Split an artist string into individual performers (Spotify uses `;`)."""
    if not artist:
        return []
    parts = [p.strip() for p in _ARTIST_SPLIT_RE.split(artist) if p and p.strip()]
    seen, out = set(), []
    for p in parts:
        k = _slugify(p)
        if k and k not in seen:
            seen.add(k)
            out.append(p)
    return out


def _based_sort(strings, based_on):
    """Reorder `strings` so each entry aligns with its closest match in `based_on`.

    Mirrors spotDL's based_sort: sort both alphabetically, then re-key `strings`
    by the position of matching entries in `based_on`. Strings that aren't
    in `based_on` go to the end. Returns (strings_aligned, based_on_reversed).
    """
    a = sorted(strings)
    b = sorted(based_on)
    pos = {v: i for i, v in enumerate(b)}
    a.sort(key=lambda x: pos.get(x, -1), reverse=True)
    b.reverse()
    return a, b


def _check_common_word(want_title, cand_title):
    """Sanity gate: at least one slug-token from want_title appears in cand_title."""
    want = _slugify(want_title)
    cand = _slugify(cand_title).replace("-", "")
    for word in want.split("-"):
        if word and word in cand:
            return True
    return False


def _calc_main_artist_match(song_artists, result_artists):
    """0–100: how well the song's main artist appears in the candidate's artists."""
    if not song_artists or not result_artists:
        return 0.0

    song_slugs = [s for s in (_slugify(a) for a in song_artists) if s]
    res_slugs = [s for s in (_slugify(a) for a in result_artists) if s]
    if not song_slugs or not res_slugs:
        return 0.0

    _, sorted_res = _based_sort(list(song_slugs), list(res_slugs))
    main_song = song_slugs[0]
    main_res = sorted_res[0]

    main_match = _ratio(main_song, main_res)

    # Cross-product of top-2 if main match is weak — handles swapped credits
    if main_match < 50 and len(song_slugs) > 1 and len(sorted_res) > 1:
        for s_a, r_a in product(song_slugs[:2], sorted_res[:2]):
            main_match = max(main_match, _ratio(s_a, r_a))

    # Catalog credits all artists in a single string ("A & B"): boost if
    # secondary artists appear inside the result's main artist token.
    if len(song_artists) > 1 and len(result_artists) == 1:
        boost = 0.0
        for a in song_slugs[1:]:
            if a and a in main_res:
                boost += 30.0 / max(1, len(song_slugs) - 1)
        main_match = min(100.0, main_match + boost)

    return main_match


def _calc_other_artists_match(song_artists, result_artists):
    """0–100 average match for non-main artists, or None if not applicable."""
    if len(song_artists) <= 1 or not result_artists:
        return None

    song_slugs = [s for s in (_slugify(a) for a in song_artists) if s]
    res_slugs = [s for s in (_slugify(a) for a in result_artists) if s]
    if len(song_slugs) <= 1:
        return None

    sorted_song, sorted_res = _based_sort(list(song_slugs), list(res_slugs))
    s_others = sorted_song[1:]
    r_others = sorted_res[1:]
    if not s_others:
        return None

    total = 0.0
    for a, b in zip_longest(s_others, r_others, fillvalue=""):
        total += _ratio(a, b)
    return total / len(s_others)


def _calc_name_match(want_title, cand_title):
    """0–100 title match via sorted slug tokens, with a non-tokenized fallback."""
    s = _slugify(want_title)
    r = _slugify(cand_title)
    if not s or not r:
        return 0.0

    s_tokens, r_tokens = _based_sort(s.split("-"), r.split("-"))
    name_match = _ratio("-".join(s_tokens), "-".join(r_tokens))
    if name_match < 75:
        name_match = max(name_match, _ratio(s, r))
    return name_match


def _forbidden_word_penalty(want_title, cand_title):
    """Each version-marker (remix/live/acoustic…) on only one side costs 15."""
    s_tokens = set(_slugify(want_title).split("-"))
    r_tokens = set(_slugify(cand_title).split("-"))
    penalty = 0.0
    for word in _FORBIDDEN_WORDS:
        if (word in s_tokens) != (word in r_tokens):
            penalty += 15.0
    return penalty


def _parse_duration_str(value):
    """Parse a duration into integer seconds, or None if unparseable.

    Accepts: int/float seconds, "mm:ss", "hh:mm:ss", or a string of digits
    (auto-detected as milliseconds if > 1500).
    """
    if value is None or value == "":
        return None
    if isinstance(value, (int, float)):
        v = float(value)
        if v <= 0:
            return None
        return int(v / 1000) if v > 1500 else int(v)
    s = str(value).strip()
    if not s:
        return None
    if ":" in s:
        try:
            parts = [int(p) for p in s.split(":")]
        except ValueError:
            return None
        if len(parts) == 2:
            return parts[0] * 60 + parts[1]
        if len(parts) == 3:
            return parts[0] * 3600 + parts[1] * 60 + parts[2]
        return None
    try:
        v = int(float(s))
    except ValueError:
        return None
    if v <= 0:
        return None
    return v // 1000 if v > 1500 else v


def _duration_penalty(want_duration, cand_duration):
    """Graduated penalty for duration mismatch. 0 if either is unknown.

    Tuned so that small drift (alt-master, transcoding) is free, modest gaps
    are a tiebreaker, and >20s shifts (live vs studio, edit vs full) push the
    candidate below MIN_KEEP even if title and artist match perfectly.
    """
    if not want_duration or not cand_duration:
        return 0.0
    diff = abs(int(want_duration) - int(cand_duration))
    if diff <= 2:
        return 0.0
    if diff <= 5:
        return 5.0
    if diff <= 10:
        return 15.0
    if diff <= 20:
        return 30.0
    return 50.0


def _score_candidate(want_title, want_artist, song, want_duration=None):
    """Return [0..1] match score for a Subsonic song candidate.

    `want_duration` is optional integer seconds. If unknown (None/0) the
    duration term contributes nothing — behaviour is identical to the
    duration-less scorer.
    """
    cand_title_raw = song.get("title") or ""
    cand_artist_raw = song.get("artist") or ""
    if not cand_title_raw:
        return 0.0

    # Sanity gate first — eliminates unrelated hits found via the artist
    if not _check_common_word(want_title, cand_title_raw):
        return 0.0

    want_title_clean = _clean_title(want_title)
    cand_title_clean = _clean_title(cand_title_raw)

    song_artists = _split_artists(want_artist)
    result_artists = _split_artists(cand_artist_raw) or (
        [cand_artist_raw] if cand_artist_raw else []
    )

    name = _calc_name_match(want_title_clean, cand_title_clean)
    # Forbidden-word penalty uses RAW titles so "(Live)" / "remix" still register
    name = max(0.0, name - _forbidden_word_penalty(want_title, cand_title_raw))

    if song_artists and result_artists:
        main = _calc_main_artist_match(song_artists, result_artists)
        other = _calc_other_artists_match(song_artists, result_artists)
        artist_score = main if other is None else (main * 0.6 + other * 0.4)
    elif not song_artists:
        # No expected artist info — neutral so the title score dominates
        artist_score = 60.0
    else:
        artist_score = 0.0

    average = (name + artist_score) / 2.0
    average -= _duration_penalty(want_duration, song.get("duration"))
    average = max(0.0, average)
    return average / 100.0


def _find_best_match(nd_user, nd_pass, track, artist, duration=None):
    """Try multiple search strategies; return (best_song, best_score) or (None, max).

    `duration` (optional, seconds) refines scoring as a tiebreaker. When not
    provided the matcher behaves exactly as before.

    Strategy order:
      1. title+artist combos (broad → narrow)
      2. [fallback-A] artist-only search when step-1 returns nothing (score==0)
         — useful for tracks whose titles contain special chars that Subsonic
         doesn't index well (e.g. "Che fastidio!", "HISS")
      3. [fallback-B] first-word of title + artist when artist-only still empty
    """
    cleaned_title = _clean_title(track)
    artists = _split_artists(artist)
    primary_artist = artists[0] if artists else ""
    norm_title = _normalize(cleaned_title or track)

    # Build queries from broad to narrow. Try every artist (catalog often
    # credits the song under a feature, not the lead) before falling back.
    queries = []
    if cleaned_title and primary_artist:
        queries.append(f"{cleaned_title} {primary_artist}")
    for extra in artists[1:3]:
        if cleaned_title:
            queries.append(f"{cleaned_title} {extra}")
    if cleaned_title:
        queries.append(cleaned_title)
    if track and track != cleaned_title:
        queries.append(track)
    if norm_title:
        queries.append(norm_title)
    # First 2 title words + artist (handles unusual title text)
    if primary_artist and norm_title:
        head = " ".join(norm_title.split()[:2])
        if head and head != norm_title:
            queries.append(f"{head} {primary_artist}")

    seen_q = set()
    best_song = None
    best_score = 0.0
    ACCEPT = 0.78   # stop early if we're confident
    MIN_KEEP = 0.55 # below this the candidate is likely the wrong track

    for q in queries:
        key = q.strip().lower()
        if not key or key in seen_q:
            continue
        seen_q.add(key)
        try:
            candidates = _subsonic_search(nd_user, nd_pass, q, song_count=10)
        except Exception:
            candidates = []
        for song in candidates:
            score = _score_candidate(track, artist, song, want_duration=duration)
            if score > best_score:
                best_score = score
                best_song = song
        if best_score >= ACCEPT:
            break

    if best_song and best_score >= MIN_KEEP:
        return best_song, best_score

    # ---- Fallback-A: artist-only search --------------------------------
    # When Subsonic fails to find anything via title (e.g. punctuation-heavy
    # titles, all-caps, short generics), search with just the artist and
    # re-score all candidates with the full scorer.
    if best_score < MIN_KEEP and primary_artist:
        artist_only_queries = [primary_artist]
        # Also try each extra artist credited on the track
        for extra in artists[1:3]:
            if extra and extra.lower() != primary_artist.lower():
                artist_only_queries.append(extra)

        for q in artist_only_queries:
            key = f"__artist__{q.strip().lower()}"
            if key in seen_q:
                continue
            seen_q.add(key)
            try:
                candidates = _subsonic_search(nd_user, nd_pass, q, song_count=20)
            except Exception:
                candidates = []
            for song in candidates:
                score = _score_candidate(track, artist, song, want_duration=duration)
                if score > best_score:
                    best_score = score
                    best_song = song
            if best_score >= ACCEPT:
                break

    if best_song and best_score >= MIN_KEEP:
        return best_song, best_score

    # ---- Fallback-B: single first word of title + artist ---------------
    # Catches very generic titles ("Kids", "Go Away") where even the
    # artist-only pool is large — we already have the best score from
    # Fallback-A so only run if we still have nothing.
    if best_score < MIN_KEEP and primary_artist and norm_title:
        first_word = norm_title.split()[0] if norm_title.split() else ""
        if first_word and len(first_word) > 2:  # skip 1-2 char words
            q = f"{first_word} {primary_artist}"
            key = q.strip().lower()
            if key not in seen_q:
                seen_q.add(key)
                try:
                    candidates = _subsonic_search(nd_user, nd_pass, q, song_count=15)
                except Exception:
                    candidates = []
                for song in candidates:
                    score = _score_candidate(track, artist, song, want_duration=duration)
                    if score > best_score:
                        best_score = score
                        best_song = song

    if best_song and best_score >= MIN_KEEP:
        return best_song, best_score
    return None, best_score


def _parse_exportify_csv(file_content):
    """Parse Exportify CSV. Returns list of (track, artist, duration_seconds_or_None)."""
    reader = csv.DictReader(io.StringIO(file_content))
    tracks = []
    for row in reader:
        track = row.get("Track Name") or row.get("track_name") or ""
        artist = row.get("Artist Name(s)") or row.get("artist_name") or ""
        # Exportify field is "Duration (ms)"
        dur_raw = row.get("Duration (ms)") or row.get("duration_ms") or ""
        duration = _parse_duration_str(dur_raw)
        if track:
            tracks.append((track.strip(), artist.strip(), duration))
    return tracks


def _parse_tunemymusic_csv(file_content):
    """Parse TuneMyMusic CSV. Returns list of (track, artist, duration_seconds_or_None)."""
    reader = csv.DictReader(io.StringIO(file_content))
    tracks = []
    for row in reader:
        track = row.get("Track Name") or row.get("Title") or row.get("track_name") or ""
        artist = row.get("Artist Name") or row.get("Artist") or row.get("artist_name") or ""
        dur_raw = (
            row.get("Duration")
            or row.get("Track Duration")
            or row.get("Length")
            or ""
        )
        duration = _parse_duration_str(dur_raw)
        if track:
            tracks.append((track.strip(), artist.strip(), duration))
    return tracks


def _handle_csv_stream(nd_user, nd_pass, format_type, f_content, playlist_name):
    if not f_content:
        yield _json_line({"type": "error", "error": "No file read or invalid file."})
        return
    if not playlist_name:
        yield _json_line({"type": "error", "error": "Missing playlist name."})
        return

    if format_type == "exportify":
        tracks = _parse_exportify_csv(f_content)
    else:
        tracks = _parse_tunemymusic_csv(f_content)

    if not tracks:
        yield _json_line({"type": "error", "error": "No songs found in the CSV file."})
        return

    yield from _search_and_create_playlist_stream(nd_user, nd_pass, playlist_name, tracks)


def _handle_youtube_stream(nd_user, nd_pass, yt_url, playlist_name):
    if not yt_url:
        yield _json_line({"type": "error", "error": "Missing playlist URL."})
        return

    if not _is_valid_youtube_url(yt_url):
        yield _json_line({"type": "error", "error": "Invalid YouTube playlist URL."})
        return

    yield _json_line({"type": "info", "message": "Extracting playlist information from YouTube (this may take a few seconds)..."})

    try:
        result = subprocess.run(
            [
                "yt-dlp",
                "--flat-playlist",
                "--dump-json",
                "--no-download",
                "--no-warnings",
                # Everything after "--" is a positional arg, so a URL starting
                # with "-" can never be parsed as a yt-dlp flag (e.g. --exec=).
                "--",
                yt_url,
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )

        if result.returncode != 0:
            yield _json_line({"type": "error", "error": "Cannot read the YouTube playlist. Check the link."})
            return

        entries = []
        for line in result.stdout.strip().split("\n"):
            if line.strip():
                try:
                    entry = json.loads(line)
                    title = entry.get("title") or entry.get("fulltitle") or ""
                    uploader = entry.get("uploader") or entry.get("channel") or ""
                    if title:
                        if " - " in title:
                            parts = title.split(" - ", 1)
                            artist = parts[0].strip()
                            track = parts[1].strip()
                        else:
                            track = title
                            artist = uploader
                        # YouTube uploader channels often end in " - Topic"
                        artist = re.sub(r"\s*-\s*topic\s*$", "", artist, flags=re.IGNORECASE).strip()
                        track = _clean_title(track)
                        # `--flat-playlist` returns duration in seconds when known;
                        # missing for some playlists — fall through as None.
                        duration = _parse_duration_str(entry.get("duration"))
                        entries.append((track, artist, duration))
                except json.JSONDecodeError:
                    continue

        if not entries:
            yield _json_line({"type": "error", "error": "No songs found in the YouTube playlist."})
            return

        if not playlist_name:
            try:
                first_entry = json.loads(result.stdout.strip().split("\n")[0])
                playlist_name = first_entry.get("playlist_title") or first_entry.get("playlist") or "YouTube Playlist"
            except Exception:
                playlist_name = "YouTube Playlist"

    except subprocess.TimeoutExpired:
        yield _json_line({"type": "error", "error": "Timeout during playlist extraction. Try again."})
        return
    except Exception as e:
        yield _json_line({"type": "error", "error": f"Error during extraction: {e}"})
        return

    yield from _search_and_create_playlist_stream(nd_user, nd_pass, playlist_name, entries)


def _search_and_create_playlist_stream(nd_user, nd_pass, playlist_name, tracks):
    """Search tracks and yield progress JSON lines (plain synchronous generator).

    Each entry in `tracks` is a (track, artist) or (track, artist, duration_seconds)
    tuple — the third element is optional and used only as a scoring tiebreaker.

    Keep-alive is handled externally by the job streaming endpoint.
    """
    total = len(tracks)
    found_ids = []
    yield _json_line({"type": "start", "total": total})

    for i, entry in enumerate(tracks):
        track, artist = entry[0], entry[1]
        duration = entry[2] if len(entry) > 2 else None
        try:
            best, score = _find_best_match(nd_user, nd_pass, track, artist, duration=duration)
            if best:
                found_ids.append(best["id"])
                detail = (
                    f"✓ {track} — {artist} → {best.get('title', '?')} by "
                    f"{best.get('artist', '?')} ({int(score*100)}%)"
                )
            else:
                if score > 0:
                    detail = f"✗ {track} — {artist}: no reliable match (max {int(score*100)}%)"
                else:
                    detail = f"✗ {track} — {artist}: not found"
        except Exception as e:
            detail = f"⚠ {track} — {artist}: error ({e})"

        yield _json_line({"type": "progress", "current": i + 1, "total": total, "detail": detail})

    if not found_ids:
        yield _json_line({"type": "error", "error": "No songs found in the catalog."})
        return

    try:
        _subsonic_create_playlist(nd_user, nd_pass, playlist_name, found_ids)
        yield _json_line({"type": "done", "message": f"Playlist '{playlist_name}' created with {len(found_ids)}/{total} songs found."})
    except Exception as e:
        yield _json_line({"type": "error", "error": f"Error creating playlist: {e}"})


# ============================================================================
#  Run
# ============================================================================

if __name__ == "__main__":
    # NEVER default to debug=True: the Werkzeug debugger exposes an interactive
    # Python console to anyone who can trigger an unhandled exception.
    _debug = os.getenv("FLASK_DEBUG", "").strip().lower() in ("1", "true", "yes", "on")
    app.run(host="0.0.0.0", port=int(os.getenv("PORT", 8080)), debug=_debug)
