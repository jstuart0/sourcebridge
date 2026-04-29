#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# SourceBridge installer.
#
# Source of truth: internal/installassets/install.sh in the
# sourcebridge-ai/sourcebridge repo (also reachable as scripts/install.sh
# via a symlink).
#
# Usage:
#   curl -fsSL https://<your-server>/install.sh | sh -s -- [flags]
#
# Flags:
#   --prefix <path>    Install dir (default: $HOME/.local). Binary goes to <path>/bin/sourcebridge.
#   --server <url>     Run `sourcebridge login --server <url>` after install.
#   --version <tag>    Pin a specific release version (default: latest).
#   --no-upgrade       Refuse to replace an existing different-version install.
#   --force-install    Used with --no-upgrade to allow replacement anyway.
#   --help, -h         Print this help.
#
# This script is POSIX-2017 compatible (tested against dash, bash, zsh).
# We do NOT use `set -e` — every command is checked explicitly so error
# messages can be specific.

set -u

OWNER_REPO="sourcebridge-ai/sourcebridge"
PREFIX="${HOME}/.local"
SERVER=""
VERSION=""
NO_UPGRADE=0
FORCE_INSTALL=0

print_help() {
    cat <<USAGE
Usage:
  curl -fsSL https://<your-server>/install.sh | sh -s -- [flags]

Flags:
  --prefix <path>    Install dir (default: \$HOME/.local).
                     Binary goes to <path>/bin/sourcebridge.
  --server <url>     Run \`sourcebridge login --server <url>\` after install.
  --version <tag>    Pin a specific release version (default: latest).
  --no-upgrade       Refuse to replace an existing different-version install.
  --force-install    Used with --no-upgrade to allow replacement anyway.
  --help, -h         Print this help.
USAGE
}

# --- arg parsing -------------------------------------------------------------

while [ $# -gt 0 ]; do
    case "$1" in
        --prefix)        PREFIX="$2"; shift 2 ;;
        --prefix=*)      PREFIX="${1#--prefix=}"; shift ;;
        --server)        SERVER="$2"; shift 2 ;;
        --server=*)      SERVER="${1#--server=}"; shift ;;
        --version)       VERSION="$2"; shift 2 ;;
        --version=*)     VERSION="${1#--version=}"; shift ;;
        --no-upgrade)    NO_UPGRADE=1; shift ;;
        --force-install) FORCE_INSTALL=1; shift ;;
        --help|-h)       print_help; exit 0 ;;
        *)               echo "sourcebridge install: unknown flag $1" >&2; exit 2 ;;
    esac
done

# --- platform detection ------------------------------------------------------

if ! command -v uname >/dev/null 2>&1; then
    echo "sourcebridge install: uname not found; cannot detect platform" >&2
    exit 2
fi

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$OS" in
    linux)  ;;
    darwin) ;;
    *)
        echo "sourcebridge install: $OS is not supported. Supported: linux, darwin." >&2
        exit 2
        ;;
esac
case "$ARCH" in
    x86_64|amd64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    *)
        echo "sourcebridge install: ${OS}/${ARCH} is not supported." >&2
        echo "Supported: linux/{amd64,arm64}, darwin/{amd64,arm64}." >&2
        exit 2
        ;;
esac
PLATFORM="${OS}-${ARCH}"

# --- download tool detection -------------------------------------------------

if command -v curl >/dev/null 2>&1; then
    HAVE_CURL=1
elif command -v wget >/dev/null 2>&1; then
    HAVE_CURL=0
else
    echo "sourcebridge install: neither curl nor wget is available; install one and retry." >&2
    exit 2
fi

# fetch URL into FILE. Returns 0 on success; non-zero on failure.
fetch() {
    if [ "$HAVE_CURL" = "1" ]; then
        curl -fsSL "$1" -o "$2"
    else
        wget -qO "$2" "$1"
    fi
}

# fetch_to_stdout URL — for the GitHub releases JSON.
fetch_to_stdout() {
    if [ "$HAVE_CURL" = "1" ]; then
        curl -fsSL "$1"
    else
        wget -qO- "$1"
    fi
}

# --- resolve version ---------------------------------------------------------

if [ -z "$VERSION" ]; then
    JSON=$(fetch_to_stdout "https://api.github.com/repos/${OWNER_REPO}/releases/latest")
    if [ -z "$JSON" ]; then
        echo "sourcebridge install: cannot reach GitHub API. Check your network and retry." >&2
        exit 4
    fi
    VERSION=$(printf '%s' "$JSON" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
    if [ -z "$VERSION" ]; then
        echo "sourcebridge install: GitHub repo ${OWNER_REPO} has no releases yet." >&2
        exit 3
    fi
fi

# --- check existing install --------------------------------------------------

INSTALL_PATH="${PREFIX}/bin/sourcebridge"
EXISTING=""
if [ -x "$INSTALL_PATH" ]; then
    EXISTING=$("$INSTALL_PATH" --version 2>/dev/null | awk '{print $NF}')
    if [ -z "$EXISTING" ]; then EXISTING="unknown"; fi

    if [ "$EXISTING" = "$VERSION" ]; then
        if [ "$NO_UPGRADE" = "1" ]; then
            echo "sourcebridge $VERSION already installed at $INSTALL_PATH"
            exit 0
        fi
        # Same version — proceed (idempotent re-install). Falls through.
    elif [ "$NO_UPGRADE" = "1" ] && [ "$FORCE_INSTALL" != "1" ]; then
        echo "sourcebridge install: existing $EXISTING would be replaced by $VERSION." >&2
        echo "Pass --force-install to upgrade or remove --no-upgrade." >&2
        exit 1
    fi
fi

# --- download archive --------------------------------------------------------

ARCHIVE="sourcebridge-${VERSION}-${PLATFORM}.tar.gz"
URL_BASE="https://github.com/${OWNER_REPO}/releases/download/${VERSION}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

if ! fetch "$URL_BASE/$ARCHIVE" "$TMPDIR/$ARCHIVE"; then
    echo "sourcebridge install: download of $URL_BASE/$ARCHIVE failed." >&2
    echo "Release ${VERSION} may not have an asset for ${PLATFORM}." >&2
    exit 5
fi

# --- checksum verification (best-effort) -------------------------------------

# Older releases (pre-this-work) won't publish SHA256SUMS. We warn and
# continue; this is documented in docs/user/installation.md#trust-model.
SHA_FILE="$TMPDIR/SHA256SUMS"
if fetch "$URL_BASE/SHA256SUMS" "$SHA_FILE" 2>/dev/null; then
    EXPECTED=$(grep " ${ARCHIVE}$" "$SHA_FILE" 2>/dev/null | awk '{print $1}')
    if [ -n "$EXPECTED" ]; then
        # Detect hash tool with explicit branches — a fallthrough chain
        # via `cmd1 || cmd2 | awk` would silently produce empty output if
        # cmd1 was missing, since awk doesn't fail on no-input.
        if command -v shasum >/dev/null 2>&1; then
            ACTUAL=$(shasum -a 256 "$TMPDIR/$ARCHIVE" | awk '{print $1}')
        elif command -v sha256sum >/dev/null 2>&1; then
            ACTUAL=$(sha256sum "$TMPDIR/$ARCHIVE" | awk '{print $1}')
        else
            ACTUAL=""
            echo "sourcebridge install: neither shasum nor sha256sum available; cannot verify checksum." >&2
        fi
        if [ -n "$ACTUAL" ]; then
            if [ "$ACTUAL" != "$EXPECTED" ]; then
                echo "sourcebridge install: checksum mismatch on $ARCHIVE." >&2
                echo "  expected: $EXPECTED" >&2
                echo "  actual:   $ACTUAL" >&2
                echo "Refusing to install." >&2
                exit 7
            fi
        fi
    else
        echo "sourcebridge install: SHA256SUMS does not list $ARCHIVE; skipping verification." >&2
    fi
else
    echo "sourcebridge install: SHA256SUMS not available for $VERSION; skipping verification (older release)." >&2
fi

# --- extract + install (constrained extraction + atomic replace) -------------

if ! mkdir -p "${PREFIX}/bin"; then
    echo "sourcebridge install: cannot create ${PREFIX}/bin" >&2
    exit 8
fi

# Verify the tarball contains exactly one safe member: "sourcebridge".
# Reject anything else (absolute paths, "..", multiple files).
# Command substitution strips trailing newlines, so a single-member archive
# yields exactly "sourcebridge" with no further whitespace.
MEMBERS=$(tar -tzf "$TMPDIR/$ARCHIVE" 2>/dev/null)
if [ -z "$MEMBERS" ]; then
    echo "sourcebridge install: archive listing failed" >&2
    exit 6
fi
if [ "$MEMBERS" != "sourcebridge" ]; then
    echo "sourcebridge install: unexpected archive contents (refusing to extract):" >&2
    echo "$MEMBERS" >&2
    exit 6
fi

if ! tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR" sourcebridge; then
    echo "sourcebridge install: archive extract failed" >&2
    exit 6
fi

# Atomic replace via temp-in-target-dir + rename.
TARGET_TMP="${PREFIX}/bin/.sourcebridge.installtmp.$$"
if ! mv "$TMPDIR/sourcebridge" "$TARGET_TMP"; then
    echo "sourcebridge install: cannot stage $TARGET_TMP" >&2
    exit 9
fi
chmod 755 "$TARGET_TMP"
if ! mv "$TARGET_TMP" "$INSTALL_PATH"; then
    rm -f "$TARGET_TMP"
    echo "sourcebridge install: cannot write $INSTALL_PATH (permission denied?)." >&2
    echo "Pass --prefix to choose a writable location, or use sudo." >&2
    exit 9
fi

# --- result ------------------------------------------------------------------

if [ -n "$EXISTING" ] && [ "$EXISTING" != "$VERSION" ]; then
    echo "Upgraded sourcebridge: $EXISTING → $VERSION (at $INSTALL_PATH)"
else
    echo "Installed sourcebridge $VERSION to $INSTALL_PATH"
fi

# Tip when ~/.local/bin isn't on PATH — surface the absolute path the user
# needs to invoke their next command (codex r1b L2).
PATH_OK=0
case ":$PATH:" in
    *":${PREFIX}/bin:"*) PATH_OK=1 ;;
esac
if [ "$PATH_OK" = "0" ]; then
    echo "" >&2
    echo "Note: ${PREFIX}/bin is not in your PATH." >&2
    echo "Run: $INSTALL_PATH setup claude" >&2
    echo "Or add ${PREFIX}/bin to your shell profile and re-open your shell." >&2
fi

# --- after-install login -----------------------------------------------------

if [ -n "$SERVER" ]; then
    # The script may itself be running under "curl | sh", so its stdin is
    # the download pipe. Reroute the chained login's stdin to /dev/tty when
    # present. Without this, term.ReadPassword() in `sourcebridge login`
    # fails for local-password installs (codex r1 C2 — there is also a Go
    # /dev/tty fallback, but redirecting stdin here is belt-and-braces).
    if [ -r /dev/tty ]; then
        if ! "$INSTALL_PATH" login --server "$SERVER" < /dev/tty; then
            echo "sourcebridge install: installed binary, but \`sourcebridge login\` failed." >&2
            echo "Run: $INSTALL_PATH login --server $SERVER" >&2
            exit 10
        fi
    else
        # No /dev/tty (Windows MSYS, some containers) — try OIDC-only.
        # If the server is local-password-only, login will fail with a
        # clear message; the user runs login manually from a real shell.
        if ! "$INSTALL_PATH" login --server "$SERVER" --method oidc; then
            echo "sourcebridge install: installed binary, but \`sourcebridge login\` failed." >&2
            echo "/dev/tty is not available, so we tried OIDC and it failed too." >&2
            echo "Run: $INSTALL_PATH login --server $SERVER" >&2
            exit 10
        fi
    fi
fi
