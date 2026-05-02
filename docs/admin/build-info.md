# Build Info

SourceBridge versions are derived from git, never from a hand-edited
manifest. This page is the operator's reference for finding, interpreting,
and reporting the version of a running SourceBridge instance.

## Where to find your version

The version is exposed in five places, each suited to a different
audience:

| Surface | Where | Best for |
|---|---|---|
| Sidebar footer | Web UI, every page | Glance check |
| Admin → System status → Build info | Web UI | Support tickets, full payload |
| `GET /api/v1/version` | HTTP, public | Scripts, monitoring, CLI |
| `sourcebridge --version` | Server / dev shell | Operator inspection |
| OCI image label | `docker inspect` | Registry / supply-chain audit |

`/api/v1/version` is intentionally unauthenticated. The values it
returns (semver string, commit sha, build date, Go runtime, edition)
are not sensitive — the commit sha is already exposed via
`/api/v1/admin/status`, and a fingerprintable Go runtime is leaked
through HTTP headers anyway.

## Reading the version string

```
v0.9.0-rc.3-dev.216+g956607e.dirty
└──────┬──────┘ └────┬────┘ └─┬──┘ └─┬─┘
       │            │         │     └─ ".dirty" → working tree had
       │            │         │       uncommitted changes when built
       │            │         └─ "g<sha>" → first 7 hex chars of HEAD
       │            └─ "dev.N" → N commits past the base tag (main builds)
       │              "pr<N>"  → pull-request build for PR #N
       │              "local"  → local make build
       └─ Last reachable tag (often the most recent release candidate)
```

Other shapes you'll see:

- `v1.2.3` — clean release; checkout was exactly on tag `v1.2.3` with
  no working-tree changes.
- `v0.9.0-rc.3-local+g956607e` — local `make build` on a clean tree.
- `0.0.0-unknown` — there was no git context at build time. Treat as a
  build-system misconfiguration; the resulting binary is not traceable.
- `0.0.0-dev.42+g956607e` — repo with no tags yet (e.g. a fresh fork).

The full grammar lives in `scripts/version.sh`. The 9-case test suite at
`tests/scripts/version_test.sh` enforces it.

## Cutting a release

```bash
git tag v1.2.3
git push origin v1.2.3
```

That's it. The release workflow (`.github/workflows/oss-release.yml`)
takes over from here:

1. Builds darwin/linux × amd64/arm64 binaries with `-ldflags` injecting
   `Version=v1.2.3`, `Commit=<sha>`, `BuildDate=<utc>`, `Edition=oss`.
2. Builds the API Docker image with the same build-args, including the
   OCI labels (`org.opencontainers.image.version`, etc.).
3. Publishes the GitHub release with binaries + checksum manifest.
4. Updates the Homebrew tap formula at `sourcebridge-ai/homebrew-tap`.

Between releases, builds on `main` get a `-dev.N+g<sha>` version
automatically. There's no version file to bump.

## Rolling deploys: web bundle vs. API server

The web bundle and the API server can carry different versions during a
rolling deploy. The Build Info card surfaces both deliberately so an
operator can see exactly which edge is at which build.

- The **sidebar footer** shows the web bundle's compile-time version
  (baked into JavaScript via `NEXT_PUBLIC_VERSION`).
- The **Build Info card** shows both the web bundle (compile-time) and
  the API server (fetched at render via `/api/v1/version`). They will
  match in steady state; they will diverge during a deploy.

If you see persistent divergence, the rolling deploy didn't finish.
Common causes: pinned ImagePullPolicy, stale caches, registry retention.

## Reporting a problem

Use the **Copy build info** button in **Admin → System status**. The
clipboard payload looks like:

```
**SourceBridge build info**
- Web bundle: v0.9.0-rc.3-dev.216+g956607e (commit 956607e9, built 2026-05-01T10:23:14Z)
- API server: v0.9.0-rc.3-dev.216+g956607e (commit 956607e9, built 2026-05-01T10:23:14Z)
- Go runtime: go1.25.0
- Edition: oss
- Worker: v0.9.0-rc.3-dev.216+g956607e
```

Paste it into a GitHub issue or support ticket. Engineers can correlate
that block back to a specific commit and image, no log spelunking needed.

## When the worker is unreachable

The Build Info card shows `Worker: (unavailable)` when the API server
can't reach `VersionService.GetVersion` on the worker within 250 ms.
This is the same condition that affects the LLM/embedding services, so
treat it as one of the standard "worker down" signals, not as a
versioning problem.

The lookup is cached for 30 seconds — repeatedly opening the Admin
page won't flood the worker port.

## OCI labels

Every published image (api, web, worker) carries:

```
org.opencontainers.image.version    = v0.9.0-rc.3-dev.216+g956607e
org.opencontainers.image.revision   = 956607e9269fd31330950c2ce8a6b3b374acaa74
org.opencontainers.image.created    = 2026-05-01T10:23:14Z
org.opencontainers.image.source     = https://github.com/sourcebridge-ai/sourcebridge
org.opencontainers.image.title      = sourcebridge-oss-{api|web|worker}
org.opencontainers.image.description = …
```

Inspect via `docker inspect <image> | jq '.[0].Config.Labels'`. These
labels feed downstream supply-chain tooling (sigstore, syft, scout) and
make `crane manifest` queries informative.

## Trying to debug a "dev" version

If `/api/v1/version` reports `version: "dev"`:

1. The binary was built without `-ldflags`. `make build` always passes
   them; check whether you ran a raw `go build` instead.
2. Confirm with `docker inspect <image> | jq '.[0].Config.Labels'`. If
   the OCI labels are present and correct, only the binary is wrong —
   the Dockerfile's `RUN go build` step might have lost the build-args
   in a custom build path.
3. Check the GHA workflow's `prepare` job output (`Computed version: …`)
   — that's the value that should have flowed through.

## MCP server version surface (CA-137)

The SourceBridge MCP server (the streamable-HTTP endpoint at
`/api/v1/mcp`, served by `internal/api/rest/mcp.go`) reports the same
git-derived version as `/api/v1/version` in two places:

- `serverInfo.version` on the MCP `initialize` response.
- `experimental.sourcebridge.version` in the capabilities block.

To verify, send an `initialize` request and read both fields:

```bash
curl -fsS -X POST https://sourcebridge.xmojo.net/api/v1/mcp \
  -H "Authorization: Bearer $(cat ~/.sourcebridge/token)" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"verify","version":"1.0"}}}' \
  | jq '.result | {server: .serverInfo.version, capability: .capabilities.experimental.sourcebridge.version}'
```

Both values must equal whatever `curl /api/v1/version | jq -r .version`
returns.

The MCP **protocol** version (`mcpProtocolVersion = "2025-11-25"`) is a
separate negotiation surface and does not change with the build version.

## Verifying signed images (CA-139)

Every SourceBridge OSS image (api/web/worker) on `main`, `dev`, or a
tagged release is signed with [Sigstore cosign](https://www.sigstore.dev/)
keyless OIDC. The signature is published next to the image in GHCR (and
Docker Hub when configured). Pull-request builds are **not** signed —
fork PRs don't get an OIDC token, and signing them would clutter the
Sigstore Rekor log without adding trust.

Two recipes, depending on how strict the deployment policy is:

### Strict (production / tagged release only)

For deployments that must accept only tagged release images:

```bash
cosign verify \
  --certificate-identity-regexp '^https://github\.com/sourcebridge-ai/sourcebridge/\.github/workflows/(build-images|oss-release)\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z][0-9A-Za-z.-]*)?$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/sourcebridge-ai/sourcebridge-api:v0.9.0
```

The regex accepts only SemVer-shaped release tags (`vX.Y.Z`, plus
optional prerelease/build-metadata segments such as `-rc.1` or
`+sha.abc`). Images built from `main` or `dev` — including the homelab's
continuous deploys — fail this check by design.

### Permissive (development / continuous-deploy main+dev)

For homelab/staging deployments that pull `main` or `dev` images:

```bash
cosign verify \
  --certificate-identity-regexp '^https://github\.com/sourcebridge-ai/sourcebridge/\.github/workflows/(build-images|oss-release)\.yml@refs/(heads/(main|dev)|tags/v[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z][0-9A-Za-z.-]*)?)$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/sourcebridge-ai/sourcebridge-api:sha-abc1234
```

This regex matches every signed SourceBridge image. Use it for
non-production verification.

### What the regex enforces

Both recipes:

- **Lock the repository** to `sourcebridge-ai/sourcebridge`. A signature
  produced from a fork or a different repo never passes.
- **Lock the workflow file** to either `build-images.yml` (which signs
  the three component images `sourcebridge-{api,web,worker}` on push to
  main/dev or on a tag) or `oss-release.yml` (which signs the separate
  combined release image at `ghcr.io/<owner>/<repo>/sourcebridge:<tag>`
  on a tag). Both workflow identities are accepted because both produce
  legitimately-signed images.
- **Lock the OIDC issuer** to GitHub Actions
  (`token.actions.githubusercontent.com`). An attacker can't slot in a
  signature from a different federated identity provider.
- **Reject pull-request builds** by omitting `refs/pull/...` from the
  regex. PRs intentionally don't sign.

### Docker Hub mirror

If you pull from Docker Hub (`docker.io/sourcebridge/...`) instead of
GHCR, the same signature exists there. Substitute the registry in the
verify command:

```bash
cosign verify \
  --certificate-identity-regexp '...same as above...' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  docker.io/sourcebridge/sourcebridge-api:v0.9.0
```

If Docker Hub credentials weren't configured at the time the image was
built (forks, repos without `DOCKERHUB_USERNAME`), the image only exists
on GHCR. Verify against GHCR instead.

### When verification fails

Common causes:

- **Unsigned PR build.** Don't run PR images in production; they're not
  meant for it.
- **Stale cosign.** Upgrade to cosign >= 2.4.x. Earlier versions had
  Rekor / Fulcio compatibility quirks.
- **Wrong registry.** GHCR signatures don't automatically apply to
  Docker Hub manifests unless the dual-publish path was active when the
  image was built.
- **Tag drift.** The signature is bound to the digest, not the tag. If
  someone retagged the image manually, the digest changes and the sig
  no longer applies. Verify by digest (`@sha256:...`) when in doubt:
  `cosign verify ... ghcr.io/.../sourcebridge-api@sha256:abcdef...`

The signature is also human-inspectable through Sigstore's transparency
log:

```bash
cosign verify --output json ... \
  | jq '.[0].optional.Bundle.Payload.logIndex'
# look up that index at https://search.sigstore.dev/
```
