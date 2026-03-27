#!/usr/bin/env bash
set -euo pipefail

# SourceBridge.ai — One-command setup
# https://github.com/sourcebridge/sourcebridge

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

readonly BOLD='\033[1m'
readonly DIM='\033[2m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[0;33m'
readonly RED='\033[0;31m'
readonly CYAN='\033[0;36m'
readonly RESET='\033[0m'

info()    { printf "${CYAN}[info]${RESET}  %s\n" "$*"; }
ok()      { printf "${GREEN}[ok]${RESET}    %s\n" "$*"; }
warn()    { printf "${YELLOW}[warn]${RESET}  %s\n" "$*"; }
err()     { printf "${RED}[error]${RESET} %s\n" "$*" >&2; }
step()    { printf "\n${BOLD}--- %s${RESET}\n" "$*"; }
prompt()  { printf "${BOLD}%s${RESET}" "$*"; }

die() { err "$@"; exit 1; }

# ---------------------------------------------------------------------------
# Banner
# ---------------------------------------------------------------------------

print_banner() {
    cat <<'BANNER'

  SourceBridge.ai
  ===============
  Requirement-aware code comprehension platform

  This script will walk you through first-time setup.
  It is safe to run multiple times.

BANNER
}

# ---------------------------------------------------------------------------
# Prerequisite checks
# ---------------------------------------------------------------------------

HAS_DOCKER=false
HAS_COMPOSE=false
HAS_GO=false
HAS_PYTHON=false
HAS_UV=false
HAS_NODE=false

check_command() {
    command -v "$1" >/dev/null 2>&1
}

version_ge() {
    # Returns 0 if $1 >= $2 (dot-separated versions)
    printf '%s\n%s' "$2" "$1" | sort -t '.' -k 1,1 -k 2,2 -k 3,3 -g | head -n1 | grep -qx "$2"
}

check_prerequisites() {
    step "Checking prerequisites"

    # Docker
    if check_command docker; then
        local docker_version
        docker_version=$(docker version --format '{{.Client.Version}}' 2>/dev/null || echo "0")
        ok "Docker ${docker_version}"
        HAS_DOCKER=true
    else
        warn "Docker not found (required for Docker Compose mode)"
        info "Install: https://docs.docker.com/get-docker/"
    fi

    # Docker Compose (v2 plugin or standalone)
    if docker compose version >/dev/null 2>&1; then
        local compose_version
        compose_version=$(docker compose version --short 2>/dev/null || echo "unknown")
        ok "Docker Compose ${compose_version}"
        HAS_COMPOSE=true
    elif check_command docker-compose; then
        ok "Docker Compose (standalone)"
        HAS_COMPOSE=true
    else
        warn "Docker Compose not found (required for Docker Compose mode)"
        info "Install: https://docs.docker.com/compose/install/"
    fi

    # Go
    if check_command go; then
        local go_version
        go_version=$(go version | sed -E 's/.*go([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/')
        if version_ge "${go_version}" "1.25"; then
            ok "Go ${go_version}"
            HAS_GO=true
        else
            warn "Go ${go_version} found, but 1.25+ recommended for source builds"
        fi
    else
        info "Go not found (optional, needed for source builds)"
    fi

    # Python
    if check_command python3; then
        local py_version
        py_version=$(python3 --version 2>/dev/null | sed -E 's/Python //')
        if version_ge "${py_version}" "3.12"; then
            ok "Python ${py_version}"
            HAS_PYTHON=true
        else
            warn "Python ${py_version} found, but 3.12+ required for worker"
        fi
    else
        info "Python 3 not found (optional, needed for worker in source mode)"
    fi

    # uv
    if check_command uv; then
        local uv_version
        uv_version=$(uv --version 2>/dev/null | sed -E 's/uv //')
        ok "uv ${uv_version}"
        HAS_UV=true
    else
        if [ "$HAS_PYTHON" = true ]; then
            info "uv not found (recommended for Python dependency management)"
            info "Install: curl -LsSf https://astral.sh/uv/install.sh | sh"
        fi
    fi

    # Node.js
    if check_command node; then
        local node_version
        node_version=$(node --version 2>/dev/null | sed 's/^v//')
        local node_major
        node_major=$(echo "$node_version" | cut -d. -f1)
        if [ "$node_major" -ge 22 ] 2>/dev/null; then
            ok "Node.js ${node_version}"
            HAS_NODE=true
        else
            warn "Node.js ${node_version} found, but 22+ recommended for web UI"
        fi
    else
        info "Node.js not found (optional, needed for web UI in source mode)"
    fi
}

# ---------------------------------------------------------------------------
# Mode selection
# ---------------------------------------------------------------------------

ask_mode() {
    step "Setup mode"
    echo ""
    echo "  1) Docker Compose  (recommended -- quickest way to try SourceBridge)"
    echo "  2) Local development  (for contributing and development)"
    echo ""
    prompt "Choose [1/2] (default: 1): "
    read -r mode_choice
    mode_choice="${mode_choice:-1}"

    case "$mode_choice" in
        1) SETUP_MODE="docker" ;;
        2) SETUP_MODE="local" ;;
        *) warn "Invalid choice, defaulting to Docker Compose"; SETUP_MODE="docker" ;;
    esac
}

# ---------------------------------------------------------------------------
# LLM provider selection
# ---------------------------------------------------------------------------

LLM_PROVIDER=""
LLM_API_KEY=""
LLM_BASE_URL=""
LLM_MODEL=""

ask_llm_provider() {
    step "LLM provider"
    echo ""
    echo "  SourceBridge uses an LLM for code reasoning, review, and discussion."
    echo ""
    echo "  1) Anthropic  (cloud -- requires API key)"
    echo "  2) OpenAI     (cloud -- requires API key)"
    echo "  3) Ollama     (local -- free, runs on your machine)"
    echo "  4) Other      (configure manually later)"
    echo ""
    prompt "Choose [1/2/3/4] (default: 1): "
    read -r llm_choice
    llm_choice="${llm_choice:-1}"

    case "$llm_choice" in
        1) configure_anthropic ;;
        2) configure_openai ;;
        3) configure_ollama ;;
        4) configure_other ;;
        *) warn "Invalid choice, defaulting to Anthropic"; configure_anthropic ;;
    esac
}

configure_anthropic() {
    LLM_PROVIDER="anthropic"
    LLM_MODEL="claude-sonnet-4-20250514"
    LLM_BASE_URL=""

    echo ""
    prompt "Anthropic API key: "
    read -r api_key
    if [ -z "$api_key" ]; then
        warn "No API key provided. You can set ANTHROPIC_API_KEY later in .env or config.toml."
        warn "LLM-powered features (review, discussion, explanation) will not work without it."
    fi
    LLM_API_KEY="$api_key"
    ok "Configured Anthropic (model: ${LLM_MODEL})"
}

configure_openai() {
    LLM_PROVIDER="openai"
    LLM_MODEL="gpt-4o"
    LLM_BASE_URL=""

    echo ""
    prompt "OpenAI API key: "
    read -r api_key
    if [ -z "$api_key" ]; then
        warn "No API key provided. You can set OPENAI_API_KEY later in .env or config.toml."
        warn "LLM-powered features (review, discussion, explanation) will not work without it."
    fi
    LLM_API_KEY="$api_key"
    ok "Configured OpenAI (model: ${LLM_MODEL})"
}

configure_ollama() {
    LLM_PROVIDER="ollama"
    LLM_MODEL="qwen3:32b"
    LLM_API_KEY="not-needed"

    # Determine the Ollama base URL based on setup mode.
    # In Docker Compose mode, the worker container cannot reach the host via
    # "localhost".  On Docker Desktop for macOS/Windows the special hostname
    # "host.docker.internal" resolves to the host machine.  On native Linux
    # we fall back to the docker bridge gateway (172.17.0.1).
    local default_url="http://localhost:11434/v1"
    if [ "${SETUP_MODE:-}" = "docker" ]; then
        if [ "$(uname -s)" = "Linux" ]; then
            default_url="http://172.17.0.1:11434/v1"
        else
            default_url="http://host.docker.internal:11434/v1"
        fi
    fi

    echo ""
    info "Checking if Ollama is running..."
    # Determine the correct health-check URL.  We test the host-local endpoint
    # (localhost) regardless of the Docker URL we will write to the config.
    local check_url="http://localhost:11434/v1/models"
    if curl -sf "$check_url" >/dev/null 2>&1; then
        ok "Ollama is running at localhost:11434"
    else
        warn "Ollama does not appear to be running at localhost:11434."
        info "Install Ollama: https://ollama.ai/download"
        info "Then run: ollama serve"
    fi

    echo ""
    prompt "Ollama URL (default: ${default_url}): "
    read -r custom_url
    LLM_BASE_URL="${custom_url:-$default_url}"

    echo ""
    prompt "Model name (default: ${LLM_MODEL}): "
    read -r custom_model
    LLM_MODEL="${custom_model:-$LLM_MODEL}"

    # Offer to pull the model
    if check_command ollama; then
        echo ""
        prompt "Pull model '${LLM_MODEL}' now? [y/N]: "
        read -r pull_choice
        if [ "$pull_choice" = "y" ] || [ "$pull_choice" = "Y" ]; then
            info "Pulling ${LLM_MODEL} (this may take a while)..."
            ollama pull "$LLM_MODEL" || warn "Failed to pull model. You can try later: ollama pull ${LLM_MODEL}"
        fi
    fi

    ok "Configured Ollama (model: ${LLM_MODEL}, url: ${LLM_BASE_URL})"
}

configure_other() {
    LLM_PROVIDER=""
    LLM_MODEL=""
    LLM_BASE_URL=""
    LLM_API_KEY=""
    echo ""
    info "Skipping LLM configuration."
    info "Edit .env (Docker mode) or config.toml (local mode) to configure your provider."
    info "See docs/installation.md for supported providers and configuration options."
}

# ---------------------------------------------------------------------------
# Docker Compose setup
# ---------------------------------------------------------------------------

setup_docker() {
    step "Setting up Docker Compose"

    if [ "$HAS_DOCKER" = false ] || [ "$HAS_COMPOSE" = false ]; then
        die "Docker and Docker Compose are required for this mode. Please install them first."
    fi

    generate_env_file
    build_and_start_containers
    wait_for_healthy
    print_docker_success
}

generate_env_file() {
    local env_file=".env"

    info "Generating ${env_file}..."

    # Preserve existing values if .env already exists
    local existing_grpc_secret=""
    local existing_jwt_secret=""
    if [ -f "$env_file" ]; then
        existing_grpc_secret=$(grep -E '^SOURCEBRIDGE_GRPC_SECRET=' "$env_file" 2>/dev/null | cut -d= -f2- || true)
        existing_jwt_secret=$(grep -E '^SOURCEBRIDGE_JWT_SECRET=' "$env_file" 2>/dev/null | cut -d= -f2- || true)
    fi

    local grpc_secret="${existing_grpc_secret:-$(openssl rand -hex 16 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
    local jwt_secret="${existing_jwt_secret:-$(openssl rand -hex 32 2>/dev/null || head -c 64 /dev/urandom | od -An -tx1 | tr -d ' \n')}"

    cat > "$env_file" <<EOF
# SourceBridge.ai — Docker Compose environment
# Generated by setup.sh on $(date -u +"%Y-%m-%dT%H:%M:%SZ")
#
# Re-run ./setup.sh to regenerate, or edit this file directly.

# --- Security ---
SOURCEBRIDGE_GRPC_SECRET=${grpc_secret}
SOURCEBRIDGE_JWT_SECRET=${jwt_secret}

# --- LLM Provider ---
SOURCEBRIDGE_LLM_PROVIDER=${LLM_PROVIDER}
SOURCEBRIDGE_LLM_MODEL=${LLM_MODEL}
SOURCEBRIDGE_LLM_BASE_URL=${LLM_BASE_URL}
SOURCEBRIDGE_LLM_API_KEY=${LLM_API_KEY}

# --- Embedding Provider ---
# Options: voyage, openai, ollama
# For Ollama embeddings, set:
#   SOURCEBRIDGE_EMBEDDING_PROVIDER=ollama
#   SOURCEBRIDGE_EMBEDDING_BASE_URL=http://host.docker.internal:11434
# For Voyage AI (recommended for production):
#   SOURCEBRIDGE_EMBEDDING_PROVIDER=voyage
#   VOYAGE_API_KEY=your-voyage-api-key
SOURCEBRIDGE_EMBEDDING_PROVIDER=${SOURCEBRIDGE_EMBEDDING_PROVIDER:-}
VOYAGE_API_KEY=${VOYAGE_API_KEY:-}
EOF

    ok "Created ${env_file}"
}

build_and_start_containers() {
    info "Building and starting containers..."
    info "This may take several minutes on first run."
    echo ""

    docker compose up -d --build 2>&1 | while IFS= read -r line; do
        printf "  %s\n" "$line"
    done

    echo ""
}

wait_for_healthy() {
    local url="http://localhost:8080/healthz"
    local timeout=120
    local interval=3
    local elapsed=0

    info "Waiting for API server to become healthy (timeout: ${timeout}s)..."

    while [ "$elapsed" -lt "$timeout" ]; do
        if curl -sf "$url" >/dev/null 2>&1; then
            ok "API server is healthy"
            return 0
        fi
        sleep "$interval"
        elapsed=$((elapsed + interval))
        printf "  ... waiting (%ds / %ds)\n" "$elapsed" "$timeout"
    done

    warn "API server did not become healthy within ${timeout}s."
    warn "This might be normal if images are still building."
    echo ""
    info "Check container status:  docker compose ps"
    info "Check API logs:          docker compose logs sourcebridge"
    info "Check worker logs:       docker compose logs worker"
    return 1
}

print_docker_success() {
    cat <<EOF

${BOLD}=======================================${RESET}
  SourceBridge.ai is running.
${BOLD}=======================================${RESET}

  Web UI:      ${GREEN}http://localhost:3000${RESET}
  API server:  http://localhost:8080
  Health:      http://localhost:8080/healthz

  Useful commands:
    docker compose ps          Show container status
    docker compose logs -f     Follow all logs
    docker compose down        Stop all services
    docker compose up -d       Start all services

  Configuration:
    .env                       Environment variables

EOF

    if [ -z "$LLM_PROVIDER" ]; then
        warn "No LLM provider configured. Edit .env to enable AI features."
    fi
}

# ---------------------------------------------------------------------------
# Local development setup
# ---------------------------------------------------------------------------

setup_local() {
    step "Setting up for local development"

    local missing=false

    if [ "$HAS_GO" = false ]; then
        err "Go 1.25+ is required for source builds."
        info "Install: https://go.dev/dl/"
        missing=true
    fi

    if [ "$HAS_PYTHON" = false ] || [ "$HAS_UV" = false ]; then
        warn "Python 3.12+ and uv are needed for the worker."
        if [ "$HAS_PYTHON" = false ]; then
            info "Install Python: https://www.python.org/downloads/"
        fi
        if [ "$HAS_UV" = false ]; then
            info "Install uv: curl -LsSf https://astral.sh/uv/install.sh | sh"
        fi
    fi

    if [ "$HAS_NODE" = false ]; then
        warn "Node.js 22+ is needed for the web UI."
        info "Install: https://nodejs.org/"
    fi

    if [ "$missing" = true ]; then
        die "Required prerequisites are missing. Install them and re-run this script."
    fi

    build_local
    generate_config_toml
    print_local_success
}

build_local() {
    info "Building Go API server..."
    make build-go 2>&1 | while IFS= read -r line; do printf "  %s\n" "$line"; done
    ok "Built bin/sourcebridge"

    if [ "$HAS_PYTHON" = true ] && [ "$HAS_UV" = true ]; then
        info "Installing Python worker dependencies..."
        make build-worker 2>&1 | while IFS= read -r line; do printf "  %s\n" "$line"; done
        ok "Worker dependencies installed"
    fi

    if [ "$HAS_NODE" = true ]; then
        info "Installing web UI dependencies..."
        (cd web && npm ci) 2>&1 | while IFS= read -r line; do printf "  %s\n" "$line"; done
        ok "Web UI dependencies installed"
    fi
}

generate_config_toml() {
    local config_file="config.toml"

    if [ -f "$config_file" ]; then
        info "${config_file} already exists. Leaving it unchanged."
        return
    fi

    info "Generating ${config_file}..."

    cat > "$config_file" <<EOF
# SourceBridge.ai configuration
# Generated by setup.sh on $(date -u +"%Y-%m-%dT%H:%M:%SZ")
#
# Environment variables override file values. The env prefix is SOURCEBRIDGE_
# with nested keys joined by underscores, e.g. SOURCEBRIDGE_LLM_BASE_URL.

[llm]
EOF

    if [ "$LLM_PROVIDER" = "anthropic" ]; then
        cat >> "$config_file" <<EOF
provider = "anthropic"
base_url = ""
summary_model = "${LLM_MODEL}"
review_model  = "${LLM_MODEL}"
ask_model     = "${LLM_MODEL}"
# Set ANTHROPIC_API_KEY in your environment.
EOF
    elif [ "$LLM_PROVIDER" = "openai" ]; then
        cat >> "$config_file" <<EOF
provider = "openai"
base_url = ""
summary_model = "${LLM_MODEL}"
review_model  = "${LLM_MODEL}"
ask_model     = "${LLM_MODEL}"
# Set OPENAI_API_KEY in your environment.
EOF
    elif [ "$LLM_PROVIDER" = "ollama" ]; then
        cat >> "$config_file" <<EOF
provider = "ollama"
base_url = "${LLM_BASE_URL}"
summary_model = "${LLM_MODEL}"
review_model  = "${LLM_MODEL}"
ask_model     = "${LLM_MODEL}"
EOF
    else
        cat >> "$config_file" <<EOF
# Uncomment and configure your provider:
# provider = "anthropic"
# base_url = ""
# summary_model = "claude-sonnet-4-20250514"
# review_model  = "claude-sonnet-4-20250514"
# ask_model     = "claude-sonnet-4-20250514"
EOF
    fi

    cat >> "$config_file" <<EOF

[server]
http_port = 8080
grpc_port = 50051

[storage]
surreal_mode = "embedded"

[security]
mode = "oss"
EOF

    ok "Created ${config_file}"
}

print_local_success() {
    cat <<EOF

${BOLD}=======================================${RESET}
  SourceBridge.ai is ready for development.
${BOLD}=======================================${RESET}

  Start each component in a separate terminal:

  ${BOLD}Terminal 1 -- API server:${RESET}
    make dev

  ${BOLD}Terminal 2 -- Web UI:${RESET}
    make dev-web

  ${BOLD}Terminal 3 -- Worker:${RESET}
    cd workers && uv run python -m workers

  Once running:
    Web UI:      ${GREEN}http://localhost:3000${RESET}
    API server:  http://localhost:8080
    Health:      http://localhost:8080/healthz

  Configuration:
    config.toml                Local configuration

  Useful commands:
    make test                  Run all tests
    make lint                  Run all linters
    make build                 Build all components
    make help                  Show all make targets

EOF

    if [ -z "$LLM_PROVIDER" ]; then
        warn "No LLM provider configured. Edit config.toml to enable AI features."
    fi

    if [ "$LLM_PROVIDER" = "anthropic" ] && [ -z "$LLM_API_KEY" ]; then
        warn "Remember to export ANTHROPIC_API_KEY before starting the worker."
    fi

    if [ "$LLM_PROVIDER" = "openai" ] && [ -z "$LLM_API_KEY" ]; then
        warn "Remember to export OPENAI_API_KEY before starting the worker."
    fi
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
    print_banner
    check_prerequisites
    ask_mode

    # Validate prerequisites for chosen mode
    if [ "$SETUP_MODE" = "docker" ] && { [ "$HAS_DOCKER" = false ] || [ "$HAS_COMPOSE" = false ]; }; then
        err "Docker and Docker Compose are required for Docker Compose mode."
        echo ""
        info "Install Docker:         https://docs.docker.com/get-docker/"
        info "Install Docker Compose: https://docs.docker.com/compose/install/"
        echo ""
        prompt "Switch to local development mode instead? [y/N]: "
        read -r switch_choice
        if [ "$switch_choice" = "y" ] || [ "$switch_choice" = "Y" ]; then
            SETUP_MODE="local"
        else
            die "Cannot proceed without Docker. Install Docker and re-run this script."
        fi
    fi

    ask_llm_provider

    case "$SETUP_MODE" in
        docker) setup_docker ;;
        local)  setup_local ;;
    esac
}

main "$@"
