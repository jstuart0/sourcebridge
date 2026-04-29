# Installation

The fastest way to install SourceBridge and connect it to your server is the
one-line installer:

```bash
curl -fsSL https://<your-server>/install.sh | sh -s -- --server https://<your-server>
```

This:

1. Downloads the latest `sourcebridge` binary for your platform from the
   upstream GitHub releases.
2. Verifies the SHA-256 checksum against the release-published `SHA256SUMS`.
3. Installs to `~/.local/bin/sourcebridge` (no `sudo` needed).
4. Runs `sourcebridge login --server <url>` so you're authenticated and ready
   to run `sourcebridge setup claude` in any indexed repository.

Once installed, point Claude Code at a repo:

```bash
cd /path/to/your/repo
sourcebridge setup claude
```

That writes `.mcp.json` and `.claude/CLAUDE.md`. **Restart Claude Code** to
pick up the new MCP server.

---

## Installer flags

```text
--prefix <path>    Install dir (default: $HOME/.local).
                   Binary lands at <path>/bin/sourcebridge.
--server <url>     Run `sourcebridge login --server <url>` after install.
--version <tag>    Pin a specific release version (default: latest).
--no-upgrade       Refuse to replace an existing different-version install.
--force-install    Used with --no-upgrade to allow replacement anyway.
--help, -h         Print help.
```

## Alternative install paths

### Homebrew

```bash
brew install sourcebridge-ai/tap/sourcebridge
```

The Homebrew tap is auto-updated on every release. Use this if you prefer
package managers and don't mind missing the chained `sourcebridge login` step
(run it manually after `brew install`).

### Manual download

If you want to inspect everything before running it, the installer's job is
mechanical:

```bash
# 1. Download the archive for your platform.
TAG=$(curl -fsSL https://api.github.com/repos/sourcebridge-ai/sourcebridge/releases/latest \
      | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
ARCHIVE="sourcebridge-${TAG}-darwin-arm64.tar.gz"   # adjust for your OS/arch
curl -fsSL "https://github.com/sourcebridge-ai/sourcebridge/releases/download/${TAG}/${ARCHIVE}" -o /tmp/${ARCHIVE}

# 2. Verify the checksum.
curl -fsSL "https://github.com/sourcebridge-ai/sourcebridge/releases/download/${TAG}/SHA256SUMS" -o /tmp/SHA256SUMS
shasum -a 256 -c /tmp/SHA256SUMS 2>&1 | grep "${ARCHIVE}"
# expect: <archive>: OK

# 3. Extract and install.
tar -xzf /tmp/${ARCHIVE} -C /tmp
mv /tmp/sourcebridge ~/.local/bin/sourcebridge
chmod 755 ~/.local/bin/sourcebridge

# 4. Authenticate.
sourcebridge login --server https://<your-server>
```

### Build from source

Requires Go 1.24+ and a working C toolchain (tree-sitter has CGo deps).

```bash
git clone https://github.com/sourcebridge-ai/sourcebridge
cd sourcebridge
make build
./build/sourcebridge --version
```

---

## Trust model

The one-line installer is a `curl | sh` pipe. That trust model has limits;
we want them stated up front.

**The installer trusts:**

1. **The host serving `install.sh`** — your SourceBridge server (or eventually
   `sourcebridge.ai`) over TLS. The script itself is short, committed, and
   code-reviewed in [`internal/installassets/install.sh`](https://github.com/sourcebridge-ai/sourcebridge/blob/main/internal/installassets/install.sh)
   so you can read it before piping to `sh`.
2. **GitHub.com TLS** — the binary archive is downloaded from
   `https://github.com/sourcebridge-ai/sourcebridge/releases/...` over TLS.
3. **The release process that produced `SHA256SUMS`.** The installer verifies
   the archive against the SHA list. `SHA256SUMS` itself is **not** signed.
   This protects against:
   - Network-induced corruption.
   - An attacker who can replace the archive but not also republish
     `SHA256SUMS` with a matching hash.

   It does **not** protect against:
   - A compromised GitHub release workflow (an attacker who can rewrite both
     the archive and `SHA256SUMS` in the same release).
   - A compromised upstream account.
   - A compromised SourceBridge server serving a malicious `install.sh`.

If you need authenticity-grade verification, follow the
[manual download](#manual-download) path and verify against your own pinned
checksums, or wait for our future signed-release work
([sigstore/cosign](https://www.sigstore.dev/) — tracked, not yet shipped).

---

## Upgrading

Re-run the installer:

```bash
curl -fsSL https://<your-server>/install.sh | sh
```

Same-version reruns are idempotent. Different-version reruns replace the
binary atomically and print `Upgraded sourcebridge: <old> → <new>`.

To pin a specific version:

```bash
curl -fsSL https://<your-server>/install.sh | sh -s -- --version v0.7.3
```

To refuse upgrades and exit if a different version is already installed:

```bash
curl -fsSL https://<your-server>/install.sh | sh -s -- --no-upgrade
```

---

## Uninstalling

The installer writes one file. To remove it:

```bash
rm ~/.local/bin/sourcebridge
rm -rf ~/.sourcebridge   # token + server URL
```

`.mcp.json` and `.claude/CLAUDE.md` written by `sourcebridge setup claude` in
your repos are not touched by uninstall — remove them manually if you want
Claude Code to forget about SourceBridge.
