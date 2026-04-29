# Homebrew Tap Setup Checklist

One-time setup Jay must complete before the first production tag push.
The release workflow (`oss-release.yml`, `tap-update` job) will fail
loudly if any step is missing.

---

## Step 1 — Create the tap repo

1. Go to https://github.com/organizations/sourcebridge-ai/repositories/new
   (or https://github.com/new if creating under a personal account, then
   transfer ownership).
2. Name: `homebrew-tap`
3. Visibility: **Public** (Homebrew requires public tap repos).
4. Do NOT initialize with a README — you'll push from this directory.

Then push the bootstrap files:

```bash
cd /path/to/sourcebridge/_homebrew-tap-bootstrap

git init
git remote add origin git@github.com:sourcebridge-ai/homebrew-tap.git
git add .
git commit -m "bootstrap tap repo"
git branch -M main
git push -u origin main
```

---

## Step 2 — Generate a deploy key

Run this locally (not on the server):

```bash
ssh-keygen -t ed25519 -f tap-deploy-key -N "" -C "sourcebridge-release-bot"
```

This creates two files: `tap-deploy-key` (private) and `tap-deploy-key.pub` (public).

---

## Step 3 — Add the public key to the tap repo

1. Open https://github.com/sourcebridge-ai/homebrew-tap/settings/keys
2. Click "Add deploy key".
3. Title: `sourcebridge-release-bot`
4. Key: paste the contents of `tap-deploy-key.pub`.
5. Check "Allow write access".
6. Click "Add key".

---

## Step 4 — Add the private key to the main repo's Actions secrets

1. Open https://github.com/sourcebridge-ai/sourcebridge/settings/secrets/actions
2. Click "New repository secret".
3. Name: `HOMEBREW_TAP_DEPLOY_KEY`
4. Value: paste the full contents of `tap-deploy-key` (the private key,
   including the `-----BEGIN OPENSSH PRIVATE KEY-----` header and footer).
5. Click "Add secret".

---

## Step 5 — Delete the local key files

```bash
rm tap-deploy-key tap-deploy-key.pub
```

Never commit the private key.

---

---

## Step 5b — Understand blast radius and rotation

**Critical:** the deploy key you just generated is a high-blast-radius
credential. Treat it like a production code-signing key.

### What a leak does

A leak of `tap-deploy-key` (or the `HOMEBREW_TAP_DEPLOY_KEY` GitHub
Actions secret) lets the holder push to `Formula/sourcebridge.rb` in
`sourcebridge-ai/homebrew-tap`. Every Mac running

```bash
brew install sourcebridge-ai/tap/sourcebridge
brew upgrade sourcebridge
```

after such a push executes whatever binary the modified formula points
at, with the user's privileges. This is an arbitrary code execution
vector for every macOS user of SourceBridge.

### Rotation procedure

If you suspect the key has leaked (incident, accidental commit, misplaced
backup), rotate immediately:

1. **Revoke.** Open
   <https://github.com/sourcebridge-ai/homebrew-tap/settings/keys>
   and delete the entry titled `sourcebridge-release-bot`. This
   invalidates the old key; any pending push using it will fail.
2. **Regenerate and re-install.** Re-run **Steps 2 → 3 → 4** of this
   checklist with a fresh keypair and a new title (e.g.
   `sourcebridge-release-bot-2`). Update the `HOMEBREW_TAP_DEPLOY_KEY`
   secret in the main repo with the new private key.
3. **Verify end-to-end.** Cut a no-op patch tag (e.g. bump the patch
   number on your next release) to confirm the workflow can push with
   the new key.

If the push workflow later fails with "no matching host key type found",
add `ecdsa-sha2-nistp256,ssh-ed25519` to `HostKeyAlgorithms` in the
workflow's SSH config and pin the Ed25519 key alongside the ECDSA one.

### Recommended posture upgrade — GitHub App

The deploy-key model is convenient but custodial: the long-lived private
key sits in a GitHub Actions secret indefinitely, and a leak compromises
every release until rotation. A narrower posture is a GitHub App installed
only on `sourcebridge-ai/homebrew-tap`, scoped to `contents:write` and
nothing else, generating short-lived (60-min) installation tokens per
workflow run.

Trade-off:
- **Pro:** no long-lived custody; in-flight token has bounded blast radius;
  revocation is instantaneous (uninstall the app).
- **Pro:** finer-grained audit log (per-installation request).
- **Con:** ~1 hour of additional setup (create app, install on tap repo,
  set `APP_ID` + `APP_PRIVATE_KEY` secrets, swap the workflow's SSH push
  for `actions/create-github-app-token` + HTTPS push).

This is recommended for production posture. It is **not** required to
ship a release; the deploy-key flow with the hardening already in place
is acceptable.

### See also

- xander's audit (finding M8) at
  `thoughts/shared/reviews/2026-04-28-cloud-install-security-review-xander.md`
- Security hardening plan (decision i) at
  `thoughts/shared/plans/2026-04-28-claude-code-security-hardening.md`

---

## Step 6 — (Optional) Smoke test with a pre-release tag

Cut a test tag to verify the workflow end-to-end before a real release:

```bash
git tag v0.0.0-test1
git push origin v0.0.0-test1
```

Then watch the `OSS Release` workflow in the Actions tab. After it completes:

- Verify 4 archives appear on the Releases page (linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64).
- Verify `Formula/sourcebridge.rb` in the tap repo references `v0.0.0-test1`
  with 4 real SHA256 checksums.
- On a Mac: `brew install sourcebridge-ai/tap/sourcebridge` and confirm
  `sourcebridge --version` prints `v0.0.0-test1`.

Clean up afterwards:

```bash
gh release delete v0.0.0-test1 --yes
git push --delete origin v0.0.0-test1
git tag -d v0.0.0-test1
```

---

## Troubleshooting

**`tap-update` job fails with "HOMEBREW_TAP_DEPLOY_KEY secret is not set"**
- The secret is missing. Repeat step 4.

**`git push` in the tap-update job fails with "Permission denied (publickey)"**
- The deploy key's public half isn't in the tap repo, or write access wasn't
  enabled. Repeat step 3.

**Formula file shows placeholder SHAs (all zeros)**
- You pushed the bootstrap placeholder directly. Wait for a real tag push to
  run the workflow, which overwrites the file.

**`ruby -c Formula/sourcebridge.rb` fails in the workflow**
- The `sed` substitution left an unresolved placeholder. Check the workflow
  logs for which secret or output was empty.
