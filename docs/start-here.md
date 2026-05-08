# Start Here

You just ran SourceBridge. Here's what happened and what to do next.

## What Just Happened

1. **Services started** — Docker Compose launched four services: the Go API server (port 8080), Python AI worker (port 50051), SurrealDB database, and the Next.js web UI (port 3000).
2. **Sample repo imported** — The demo imported `acme-api`, a TypeScript/Next.js sample codebase with authentication, team management, and Stripe billing.
3. **Code indexed** — SourceBridge parsed every file using tree-sitter, extracted symbols (functions, types, imports), and built a dependency graph.
4. **Field guide generated** — If an LLM was available (Ollama or cloud API key), the AI worker generated cliff notes, a code tour, a learning path, and workflow stories.

## What to Do Next

### Explore the Demo

Open [http://localhost:3300](http://localhost:3300) and click on **acme-api**:

- **Cliff Notes** — Read the AI-generated summary of the system
- **Code Tour** — Walk through the codebase with guided stops
- **Learning Path** — Follow a structured onboarding path
- **File Browser** — Navigate the file tree and symbol index
- **Architecture** — View auto-generated Mermaid diagrams

### Try Your Own Repository

1. Click **Add Repository** in the web UI
2. Enter a **local path** (e.g., `/path/to/your/project`) or a **Git URL**
3. For private repos, provide a personal access token
4. SourceBridge indexes the repo and starts generating a field guide

### Connect Your LLM

SourceBridge needs an LLM to generate AI artifacts. Pick one:

**Ollama (easiest local setup):**
```bash
# Install Ollama: https://ollama.com
ollama pull qwen3:32b
# SourceBridge auto-detects Ollama — just restart the demo
./demo.sh
```

**Anthropic (best quality):**
```bash
SOURCEBRIDGE_LLM_API_KEY=sk-ant-... ./demo.sh
```

**Other providers:** Set `SOURCEBRIDGE_LLM_PROVIDER` to `openai`, `gemini`, or `openrouter` along with the API key.

## Common First Issues

See [Troubleshooting](troubleshooting.md) for detailed fixes. Quick answers:

| Problem | Fix |
|---|---|
| "Docker not found" | Install [Docker Desktop](https://docker.com) |
| Port 3000 or 8080 in use | Stop the conflicting process, or set `SOURCEBRIDGE_WEB_PORT` / `SOURCEBRIDGE_API_PORT` |
| No cliff notes appearing | An LLM is required — install Ollama or provide an API key |
| Indexing stuck | Check `docker compose logs sourcebridge` for errors |
| Web UI blank | Wait 15-30 seconds for the Next.js build to complete |

## Where to Get Help

- [Troubleshooting Guide](troubleshooting.md) — Top issues and fixes
- [GitHub Discussions](https://github.com/sourcebridge/sourcebridge/discussions) — Ask questions, share feedback
- [GitHub Issues](https://github.com/sourcebridge/sourcebridge/issues) — Report bugs

## Stopping the Demo

```bash
# Stop services (preserves data)
docker compose -f docker-compose.yml -f docker-compose.demo.yml down

# Stop and remove all data
docker compose -f docker-compose.yml -f docker-compose.demo.yml down -v
```
