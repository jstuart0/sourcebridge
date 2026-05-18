# Start Here

Haven't installed SourceBridge yet? Start with [GETTING-STARTED.md](../GETTING-STARTED.md).

---

SourceBridge is running. Here's what's live and what to do next.

## What Just Happened

Docker Compose started four services:

1. **Go API server** — port 8080. Handles indexing, queries, and artifact generation.
2. **Python AI worker** — port 50051 (gRPC). Runs LLM reasoning, cliff notes, and knowledge generation.
3. **SurrealDB** — graph database. Stores the symbol graph, requirements, and artifacts.
4. **Next.js web UI** — port 3300. The browser dashboard.

All four must be running before you index a repository or ask questions. Check with `docker compose ps`.

## What to Do Next

### If you ran the demo (`./demo.sh`)

The demo imported `acme-api` — a TypeScript/Next.js sample codebase with authentication, team management, and Stripe billing — and generated a field guide if an LLM was available.

Open [http://localhost:3300](http://localhost:3300) and click on **acme-api**:

- **Cliff Notes** — AI-generated summary of the system
- **Code Tour** — Guided walk through the codebase with annotated stops
- **Learning Path** — Structured onboarding path for a new developer
- **File Browser** — File tree with symbol index
- **Architecture** — Auto-generated Mermaid diagrams

When you're ready to index your own codebase, click **Add Repository** (see below).

### If you started fresh (Docker Compose or from source)

No repository is indexed yet. To add one:

1. Open [http://localhost:3300](http://localhost:3300)
2. Click **Add Repository**
3. Enter a **local path** (e.g., `/path/to/your/project`) or a **Git URL**
4. For private repos, provide a personal access token

SourceBridge parses every source file using tree-sitter and builds a dependency graph. Indexing a medium-sized repository (10k–50k LOC) typically takes under a minute. Generating the full field guide (cliff notes, code tour, learning path, workflow stories) requires an LLM and runs as a background job — you can use the repo immediately while generation completes.

Alternatively, use the CLI:

```bash
sourcebridge index /path/to/your/repo
```

See [CLI Quickstart](user/cli-quickstart.md) for the full CLI workflow.

## Connect Your LLM

SourceBridge needs an LLM to generate AI artifacts. Pick one:

**Ollama (easiest local setup):**
```bash
# Install Ollama: https://ollama.com
ollama pull qwen3:32b
# SourceBridge auto-detects Ollama at http://localhost:11434 — restart to apply
docker compose restart sourcebridge-api sourcebridge-worker
```

**Anthropic (best quality):**
```bash
SOURCEBRIDGE_LLM_API_KEY=sk-ant-... docker compose up -d
```

**Other providers:** Set `SOURCEBRIDGE_LLM_PROVIDER` to `openai`, `gemini`, or `openrouter` along with `SOURCEBRIDGE_LLM_API_KEY`.

If you followed [GETTING-STARTED.md](../GETTING-STARTED.md), step 5 covers the LLM setup in detail, including the Admin UI path (`/admin/llm`) for post-install changes.

## Common First Issues

See [Troubleshooting](troubleshooting.md) for detailed fixes. Quick answers:

| Problem | Fix |
|---|---|
| "Docker not found" | Install [Docker Desktop](https://docker.com) |
| Port 3300 or 8080 in use | Stop the conflicting process, or set `SOURCEBRIDGE_WEB_PORT` / `SOURCEBRIDGE_API_PORT` |
| No cliff notes appearing | An LLM is required — install Ollama or provide an API key |
| Indexing stuck | Check `docker compose logs sourcebridge` for errors |
| Web UI blank | Wait 15–30 seconds for the Next.js build to complete |

Also see [GETTING-STARTED.md Troubleshooting](../GETTING-STARTED.md#troubleshooting) — the two guides share common failure modes.

## Where to Get Help

- [Troubleshooting Guide](troubleshooting.md) — Top issues and fixes
- [GitHub Discussions](https://github.com/sourcebridge/sourcebridge/discussions) — Ask questions, share feedback
- [GitHub Issues](https://github.com/sourcebridge/sourcebridge/issues) — Report bugs

## Stopping Services

```bash
# Stop services (preserves data)
docker compose down

# Stop and remove all data
docker compose down -v

# Demo-specific (if you ran ./demo.sh)
docker compose -f docker-compose.yml -f docker-compose.demo.yml down
docker compose -f docker-compose.yml -f docker-compose.demo.yml down -v
```
