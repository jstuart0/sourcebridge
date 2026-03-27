# Speculative Decoding Guide

Speculative decoding is a server-side inference optimization that can improve LLM throughput 1.5-3x with no change to output quality. This guide covers how to enable it with SourceBridge.

## How It Works

A smaller "draft" model generates candidate tokens, then the target model verifies them in a single GPU forward pass. Accepted tokens are returned immediately; rejected tokens fall back to normal generation. The output is mathematically identical to running the target model alone — only faster.

**Key metrics:**
- **Tokens/sec** — generation throughput (higher is better)
- **Acceptance rate** — ratio of accepted draft tokens (≥60% means speculation is helping)

## Choosing an Inference Server

| Engine | Speculation Method | Per-Request Control | Best For |
|---|---|---|---|
| **llama.cpp** | Draft model, n-gram | No (server-level) | CPU/Mac/Jetson, single-user |
| **vLLM** | EAGLE3 | No (server-level) | Multi-GPU, high concurrency |
| **SGLang** | EAGLE3 | No (server-level) | Multi-GPU, high concurrency |
| **LM Studio** | Draft model | Yes (`draft_model` per request) | Desktop development |
| **Ollama** | Not supported | N/A | Simple setup (no speculation) |

## Hardware Requirements

Draft models add VRAM overhead. Recommended pairings:

| Target Model Size | Draft Model Size | Additional VRAM |
|---|---|---|
| 7-8B | 0.5-1.5B | ~1-2 GB |
| 13B | 1-3B | ~2-4 GB |
| 70B | 7-8B | ~8-10 GB |

**N-gram speculation** (llama.cpp only) requires zero additional VRAM — it uses the target model's own predictions as the draft. Works well for structured output (JSON, code).

## Server Configuration

### llama.cpp (llama-server)

```bash
# With draft model
llama-server \
  --model target-model.gguf \
  --model-draft draft-model.gguf \
  --draft-max 8 \
  --draft-p-min 0.9 \
  --host 0.0.0.0 --port 8080

# With n-gram speculation (no draft model needed)
llama-server \
  --model target-model.gguf \
  --spec-type ngram-simple \
  --spec-ngram-min 2 \
  --host 0.0.0.0 --port 8080
```

### vLLM

```bash
VLLM_USE_V1=1 vllm serve meta-llama/Llama-3.1-8B-Instruct \
  --speculative-config '{"model":"meta-llama/Llama-3.2-1B-Instruct","num_speculative_tokens":5}' \
  --host 0.0.0.0 --port 8000
```

### SGLang

```bash
python -m sglang.launch_server \
  --model meta-llama/Llama-3.1-8B-Instruct \
  --speculative-algorithm EAGLE \
  --speculative-eagle-model meta-llama/Llama-3.2-1B-Instruct \
  --num-speculative-tokens 5 \
  --host 0.0.0.0 --port 30000
```

### LM Studio

1. Load the target model in LM Studio
2. In SourceBridge admin UI, set provider to "LM Studio"
3. Enter the draft model identifier in the "Draft Model" field
4. LM Studio handles the rest per-request

## Connecting SourceBridge

1. Go to **Admin → LLM Configuration**
2. Select the appropriate provider (`llama-cpp`, `vllm`, `sglang`, or `lmstudio`)
3. Set **Base URL** to your inference server endpoint
4. For LM Studio, optionally set the **Draft Model** field
5. Click **Save**, then **Test Connection** to verify

## Kubernetes Deployment

Example manifests are provided in `deploy/kubernetes/`:

- `llama-server-speculative.yaml` — llama-server with draft model
- `vllm-speculative.yaml` — vLLM with EAGLE3

## Verifying It Works

1. Run a test connection from the admin UI — check the reported tokens/sec
2. Compare tokens/sec with and without speculative decoding enabled on your server
3. Check the **acceptance rate** if available:
   - ≥80%: Excellent — draft model is a great match
   - 60-80%: Good — speculation is helping
   - <60%: Poor — try a different draft model or disable speculation

## Troubleshooting

**Low acceptance rate (<60%):**
- The draft model is a poor match for the target. Try a model from the same family but smaller (e.g., Llama-3.2-1B for Llama-3.1-8B).

**Slower than without speculation:**
- Speculation adds overhead. If VRAM is tight, the system may be swapping. Check GPU memory usage.
- At high concurrency, speculation may not help. It works best for single/few concurrent requests.

**No performance metrics showing:**
- Metrics depend on the inference server including timing data in responses. Ollama and some older server versions don't include this.
- Check that you're using the latest version of your inference server.

**Model not loading:**
- Ensure your GGUF files (llama.cpp) or HuggingFace model IDs (vLLM/SGLang) are accessible from the server.
- For Kubernetes, verify the PVC is mounted and contains the model files.
