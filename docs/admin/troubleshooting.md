# Troubleshooting

## Common Issues

### Server won't start

**Symptom:** `sourcebridge serve` fails or exits immediately.

**Solutions:**
1. Check if port 8080 is already in use: `lsof -i :8080`
2. Try a different port: `sourcebridge serve --port 9090`
3. Check logs for errors with `--verbose` flag

### "Python worker required" error

**Symptom:** `sourcebridge review` or `sourcebridge ask` shows "Python worker required".

**Solutions:**
1. Install Python 3.12+ and uv: `curl -LsSf https://astral.sh/uv/install.sh | sh`
2. Install worker dependencies: `cd workers && uv sync`
3. Verify: `uv run python -c "import workers; print('OK')"`

### No symbols found after indexing

**Symptom:** `sourcebridge index` completes but queries return no symbols.

**Solutions:**
1. Verify the repository path exists and contains source files
2. Check the language is supported (Go, Python, TypeScript, JavaScript, Java, Rust, C, C++, C#)
3. Ensure tree-sitter grammars are available

### VS Code extension not working

**Symptom:** No CodeLens or hover information in VS Code.

**Solutions:**
1. Verify the SourceBridge.ai server is running: `curl http://localhost:8080/api/v1/health`
2. Check VS Code settings for correct `sourcebridge.apiUrl`
3. Reload VS Code window (`Cmd+Shift+P` > "Reload Window")
4. Check the Output panel > SourceBridge.ai for errors

### Docker Compose issues

**Symptom:** Services fail to start with Docker Compose.

**Solutions:**
1. Ensure Docker Desktop is running
2. Pull latest images: `docker compose pull`
3. Remove old containers: `docker compose down -v && docker compose up -d`
4. Check container logs: `docker compose logs api`

### GraphQL errors

**Symptom:** Web UI shows errors or no data.

**Solutions:**
1. Verify the API server is running and healthy
2. Check browser console for CORS or network errors
3. Verify the API URL in web settings matches the running server
4. Test the GraphQL endpoint: `curl -X POST http://localhost:8080/api/v1/graphql -H 'Content-Type: application/json' -d '{"query":"{ health { status } }"}'`

## Getting Help

- [GitHub Issues](https://github.com/sourcebridge/sourcebridge/issues)
- [GitHub Discussions](https://github.com/sourcebridge/sourcebridge/discussions)
