# Local vs. Cloud LLMs for Deep Codebase Understanding

_Benchmark sweep of 11 models (+1 timeout) through an identical structured-fact
DEEP cliff-notes pipeline, tested against the SourceBridge repository._

## TL;DR

- A **20 GB local dense model** (`qwen3:32b`, Q4_K_M) and a **23 GB local MoE**
  (`qwen3.6:35b-a3b-q4_K_M`, 3B active) both produced **perfect-scoring**
  artifacts (middle-section quality 10.0/10), **beating every cloud model we
  tested** — including Claude Sonnet-4, Claude Haiku-4.5, and Gemini 2.5-Flash.
- **qwen3.6 MoE is a generational leap** over qwen3.5 MoE on the same task:
  middle-section quality jumped from 6.0 → 10.0 even though both use 3B
  active params.
- The smallest viable model is **qwen3:8b (5.2 GB)** — matches Gemini Flash
  quality at a fraction of the footprint, zero API cost.
- **Llama-3.3-70B underperformed qwen3:8b** — parameter count alone doesn't
  predict DEEP-synthesis quality.
- **Llama-3.2-3b hallucinated 23% of its file citations** — the 3B class is
  below the floor for this task.
- **Qwen3:122b-a10b-moe timed out** at 87 minutes (81 GB model in VRAM,
  10 B active; default gRPC deadline won the race).
- **Gemma 4 tested too**: `gemma4:26b-a4b-moe` DEEP = 8.25 / 0% halluc
  (well below the top but disciplined); `gemma4:31b` dense timed out on
  DEEP but runs MEDIUM at 8.00.
- **MoE ≠ MEDIUM-friendly**: both Qwen 3.6 MoE and Gemma 4 MoE collapse
  at MEDIUM depth (drops of 6–7 quality points). Dense models degrade
  gracefully. Use dense if MEDIUM is the real target.

## Methodology

The pipeline is SourceBridge's DEEP-from-understanding scenario: index the
repo → build a hierarchical understanding tree → emit 16 structured cliff-notes
sections with explicit evidence citations. The DEEP render path has been tuned
across 13 iterations to surface typed facts (symbol names, roles, external
dependencies) per section and to enforce mechanical confidence criteria when
the model self-underreports.

Each model runs against the same repository (SourceBridge, ~2100 indexed
symbols, 67 K tracked files). Local models speak to a Mac Studio 2 (M4 Max,
128 GB RAM) via Ollama on 192.168.10.108. Cloud models go through OpenRouter
from a Docker worker. Embedding provider is pinned to local Ollama in both
cases so retrieval costs don't skew timing.

### Scoring rubric (0–10 per section)

- `+3` HIGH confidence after post-render enforcement
- `+2` cites ≥3 unique real files
- `+2` names ≥3 backtick-wrapped identifiers
- `+1` content ≥600 bytes
- `+1` content ≥900 bytes
- `+1` no generic filler phrases
- `−2` per hallucinated citation (cited path not present in repo)

"middle_avg" = mean score of the four hardest sections: **Domain Model**,
**Key Abstractions**, **Testing Strategy**, **Complexity & Risk Areas**. These
are the cross-cutting sections; everything else tends to be easier for any
model to fake.

## Full ranking

| Rank | Model | Venue | Size | DEEP s | Middle avg | HIGH / MED / LOW | Halluc | Tok out | Tok/s |
|---:|---|---|---:|---:|---:|---|---:|---:|---:|
| 1 | `qwen3:32b` | local | 20.2 GB | 2874 | **10.00** | 15 / 0 / 1 | **0.0%** | 13 882 | 4.8 |
| 1 | `qwen3.6:35b-a3b-q4_K_M` (MoE) | local | 23.0 GB | 2527 | **10.00** | 14 / 0 / 2 | **0.0%** | 41 474 | 16.4 |
| 3 | `claude-haiku-4.5` | cloud | — | 370 | 9.50 | 13 / 0 / 3 | **0.0%** | 39 766 | 107.5 |
| 4 | `claude-sonnet-4` | cloud | — | 487 | 9.25 | 14 / 0 / 2 | 1.6% | 28 329 | 58.2 |
| 5 | `gemini-2.5-flash` (baseline) | cloud | — | 238 | 8.50 | 13 / 1 / 2 | 1.4% | — | — |
| 5 | `qwen3:14b` | local | 9.3 GB | 1286 | 8.50 | 13 / 0 / 3 | 5.7% | 30 667 | 23.9 |
| 7 | `gemma4:26b-a4b-it-q4_K_M` (MoE) | local | 15.5 GB | 1868 | 8.25 | 5 / 0 / 11 | **0.0%** | 21 722 | 11.6 |
| 8 | `qwen3:8b` | local | 5.2 GB | 894 | 8.00 | 11 / 4 / 1 | 2.1% | 34 748 | 38.9 |
| 9 | `qwen3.5:4b` | local | 3.4 GB | 2253 | 7.50 | 3 / 1 / 12 | **0.0%** | 7 993 | 3.5 |
| 10 | `meta-llama/llama-3.3-70b-instruct` | cloud | — | 1394 | 7.00 | 11 / 0 / 5 | **0.0%** | 25 304 | 18.1 |
| 11 | `qwen3.5:35b-a3b` (MoE) | local | 23.9 GB | 3314 | 6.00 | 8 / 0 / 8 | **0.0%** | 19 139 | 5.8 |
| 12 | `llama3.2:3b` | local | 2.0 GB | 223 | 2.00 | 0 / 0 / 16 | 23.1% | 15 100 | 67.7 |
| — | `gemma4:31b-it-q4_K_M` (dense) | local | 18.5 GB | ⏱ timeout at ~60 min | — | — | — | — | — |
| — | `qwen3.5:122b-a10b` (MoE) | local | 81.4 GB | ⏱ timeout at 87 min | — | — | — | — | — |

DEEP s = DEEP-render wall-clock only (after index + understanding). Middle avg
and confidence columns are the 16-section DEEP artifact.

## Six findings

### 1. A local 20-GB model can beat Gemini Flash, Haiku, and Sonnet on this task

`qwen3:32b` Q4_K_M running on an M4 Max hit a perfect 10.0/10 middle-section
average and only 1 low-confidence section in the whole artifact. Every cloud
model landed below it. For a one-shot "summarize this repo" workload where a
user is willing to wait ~50 minutes, a single-box local model is now the
quality ceiling.

### 2. qwen3.6 MoE is a dramatic upgrade over qwen3.5 MoE

The two MoEs are architecturally similar — 35B total, 3B active, Q4_K_M, same
Mac Studio — but:

| | qwen3.5:35b-a3b | qwen3.6:35b-a3b-q4_K_M |
|---|---:|---:|
| middle avg | 6.00 | **10.00** |
| HIGH sections | 8 | 14 |
| hallucinations | 0.0% | 0.0% |
| DEEP seconds | 3 314 | 2 527 |
| output tokens | 19 139 | **41 474** |

Same generation-skeleton, roughly half a year apart in release cadence, and
the newer model produces more than twice the grounded content per run while
landing in the top spot alongside the dense 32B. The article take: don't
judge "MoE at 3B active" by one generation's release — the pretraining recipe
matters more than the sparse design.

### 3. Smallest viable size is ~8 B dense (Qwen family)

The quality-vs-size curve bends hard between 3B and 8B:

```
llama3.2:3b  →  middle 2.0, 23.1% hallucinations, unusable
qwen3.5:4b   →  middle 7.5, 0%  hallucinations, short but grounded
qwen3:8b     →  middle 8.0, 2.1% hallucinations, matches Gemini Flash
qwen3:14b    →  middle 8.5, 5.7% hallucinations
qwen3:32b    →  middle 10.0 (perfect)
```

`qwen3:8b` is the sweet spot: 5.2 GB on disk, 38.9 tokens/s, zero API cost,
matches a cloud baseline. Good target for laptop-class inference.

### 4. 70 B dense is not a guarantee

`meta-llama/llama-3.3-70b-instruct` via OpenRouter turned in middle_avg 7.0
— below the 5 GB local `qwen3:8b` (8.0). The instruction-following matters
for this pipeline's strict JSON output + confidence rules, and the Llama-3.3
tuning appears less attuned to those constraints than the Qwen3 family.

### 5. Gemma 4 lands mid-pack; dense 31B can't finish DEEP in the hour budget

`gemma4:26b-a4b-it-q4_K_M` (MoE, 4B active) hit middle_avg 8.25 with
**0% hallucinations** — well-grounded but confidence-conservative (5
HIGH, 11 LOW). It sits between `qwen3:8b` and `qwen3:14b` in quality
while costing 1868s of DEEP wall clock (roughly the same time cost as
`qwen3:14b`, for slightly worse quality).

`gemma4:31b-it-q4_K_M` (dense 31B) **timed out** past 60 minutes on
the DEEP repository render — same failure mode as `qwen3.5:122b`.
Dense 31B is slower per token than the MoE variant, and at DEEP's
per-section output size the total wall clock exceeds the worker's
default `TimeoutKnowledgeRepository=3600s`. It does finish MEDIUM in
462s, though, which is interesting: **gemma's 31B dense is faster at
MEDIUM than its 26B MoE** (462s vs 932s), the opposite pattern from
Qwen 3.6 where the MoE was faster. The MoE's routing overhead
dominates at shorter output lengths.

Practical takeaway: skip `gemma4:31b` unless you're staying at MEDIUM.
`gemma4:26b-a4b-moe` is a reasonable DEEP option if you want an
Apache-2-licensed alternative to the Qwen 3.x family, but it won't
beat `qwen3:32b` or `qwen3.6-MoE` on this task.

### 6. 122 B MoE is too slow for this pipeline's default deadline

`qwen3.5:122b-a10b` took 26 minutes on just the understanding phase, then
ran past the gRPC 1-hour deadline during DEEP. This is a pipeline-budget
finding, not a quality finding — the model may well produce perfect output
given more time, but the article-relevant number is wall-clock at the
default ceiling, and 81 GB models on a 128 GB box are at the edge of
practical.

## Quality-vs-cost tradeoff

Rough cost-per-run (output tokens × OpenRouter list price; zero for local
Ollama on owned hardware):

| Model | $ per run | middle avg | Notes |
|---|---:|---:|---|
| `qwen3:32b` (local) | $0.00 | 10.00 | Slowest of the locals but perfect |
| `qwen3.6:35b-a3b-q4_K_M` (local) | $0.00 | 10.00 | Faster MoE equivalent |
| `claude-haiku-4.5` | ~$0.22 | 9.50 | Best cloud quality/cost |
| `claude-sonnet-4` | ~$1.00 | 9.25 | Over-pays vs Haiku for this task |
| `gemini-2.5-flash` | ~$0.02 | 8.50 | Cheapest usable cloud |
| `gemma4:26b-a4b-moe` (local) | $0.00 | 8.25 | Apache-2 alternative to Qwen; 0% halluc |
| `qwen3:8b` (local) | $0.00 | 8.00 | Best laptop-class option |
| `llama-3.3-70b` | ~$0.15 | 7.00 | Not worth it for this task |
| `llama3.2:3b` (local) | $0.00 | 2.00 | Below the viability floor |

If wall-clock matters more than peak quality, **Haiku-4.5 is the practical
winner**: 370s DEEP, 9.5/10, 0% hallucinations, 107 tokens/s output. If
quality is paramount and local is acceptable, **qwen3:32b or
qwen3.6-MoE tied for top**.

## MEDIUM-depth companion sweep

A MEDIUM render is asked for 8 sections vs DEEP's 16, and finishes
dramatically faster. The same top-5 plus the two Gemma 4 variants were
re-run at MEDIUM to see how quality + speed shift:

| Model | MEDIUM s | MEDIUM middle_avg | MEDIUM H/M/L | DEEP middle_avg | Δ DEEP→MEDIUM | Halluc @ MEDIUM |
|---|---:|---:|---|---:|---:|---:|
| `qwen3:32b` (dense) | 257 | **9.00** | 6/2/0 | 10.00 | −1.0 | 0.0% |
| `claude-haiku-4.5` | 51 | **9.00** | 8/0/0 | 9.50 | −0.5 | 0.0% |
| `claude-sonnet-4` | 72 | **9.00** | 8/0/0 | 9.25 | −0.25 | 0.0% |
| `gemini-2.5-flash` | 31 | 8.50 | 8/0/0 | 8.50 | 0.0 | 0.0% |
| `gemma4:31b` (dense) | 462 | 8.00 | 8/0/0 | **timeout** | n/a | 0.0% |
| `qwen3.6:35b-a3b` (MoE) | 1580 | 4.00 | 0/8/0 | 10.00 | **−6.0** ⚠️ | 37.5% |
| `gemma4:26b-a4b` (MoE) | 932 | **1.00** | 0/0/8 | 8.25 | **−7.25** ⚠️ | 0.0% |

**The striking pattern: dense models degrade gracefully at MEDIUM,
MoE models collapse.** Qwen 3.6 MoE drops six full points; Gemma 4 MoE
drops seven. Both DEEP-tuned through the same structured-fact pipeline
we ported from cliff notes — without that scaffold the MoE routing
apparently can't hold section coherence on 8-section output.

Two practical consequences:

1. **If MEDIUM is the real target** (faster responses, less content),
   pick a dense model — `gemma4:31b` actually runs at MEDIUM even
   though it can't finish DEEP in budget, and lands at 8.00/10.
2. **If you've deployed a local MoE** for DEEP, don't assume MEDIUM
   will produce a compact version of the same artifact. It won't —
   you'll get noisier prose with far fewer HIGH-confidence sections.

## Operational notes from the sweep

- The structured-fact pipeline tuned in v13 ported to local models without
  modification — no prompt-engineering was redone per model.
- In-process embedding cache (added during the tuning iterations) means the
  nomic-embed-text run happens once per worker process, then cache-hits for
  every subsequent DEEP render. Huge wall-clock win at scale.
- Several models hit the aggressive post-gate "unsupported_claims" or
  "needs_evidence" flag because they use hedging language ("may",
  "likely"); the confidence-floor enforcement that ships with the v13 code
  correctly upgrades these when the surviving text is still grounded.
- All grounded citations were sanity-checked against the real repo file
  tree (67 K files indexed). A hallucination is a cited path that neither
  matches a full path nor any file's basename in the repo.

## Raw data

- `summary.csv` — one row per model with timing + gate outcomes
- `per_section.csv` — 16 rows per model with per-section scores
- `quality_analysis.json` — full per-section quality records (excerpts,
  cited files, hallucinated paths)
- Per-model directories under `local-sweep-v1/<label>/` hold the original
  artifact JSON, the worker log, and a `summary.json` with wall times.

## Hardware, software, settings

- **Mac Studio 2** — M4 Max, 128 GB unified RAM, running Ollama at
  `192.168.10.108:11434/v1`. Thinking disabled (`SOURCEBRIDGE_LLM_ENABLE_THINKING=false`)
  so output goes straight to JSON.
- **Dev host** — Docker Compose running the API + worker + SurrealDB, with
  `nomic-embed-text:latest` on a local Ollama container for the embedding path.
- **Cloud models** — `claude-haiku-4.5`, `claude-sonnet-4`,
  `meta-llama/llama-3.3-70b-instruct`, `google/gemini-2.5-flash` — all via
  OpenRouter (API keys pulled from `kubectl -n automation`).
- **Pipeline** — SourceBridge v13 (the same commit across every run). No
  model-specific prompt tuning. Same DEEP-from-understanding scenario.

## Addendum: extending first-class to the other three artifacts

The cliff-notes quality bar was reached on haiku after 13 iterations. A
follow-on sweep ported the same techniques to **learning path**, **code
tour**, and **workflow story**. Goal: get all three to the same
first-class bar (≥66% HIGH-confidence sections, ≤5% hallucination, no
parse fallback) without custom per-artifact prompt engineering.

**Techniques that carried over** (all shared across the three
artifacts):

1. **Null-safe int coercion** (`parse_utils.coerce_int`) — fixes the
   original cliff-notes crash when LLMs emit `null` for `line_start` /
   `line_end`. Now reused by every artifact's parser.
2. **Confidence-floor enforcement** (`parse_utils.meets_confidence_floor`)
   — if a unit cites N unique files and names M backtick identifiers,
   upgrade its confidence regardless of what the LLM self-reported.
3. **Structured-fact hints in the prompt** (`fact_hints.build_fact_hints_block`)
   — surfaces representative files, entry-point symbols, public-API
   symbols, high-fan-in symbols, and external dependencies so the LLM
   has an anchored vocabulary for citations.
4. **Max-token ceiling raised to 16384** — DEEP learning paths and code
   tours run 22–35 k characters of JSON; the old 4096 / 8192 caps were
   truncating mid-response and sending the parser into fallback.

**Artifact-specific tightenings:**

- *learning_path*: "2+ files per step" + FILE-PATH DISCIPLINE block in
  the prompt; post-parse filter drops file paths absent from the
  snapshot's known directories.
- *code_tour*: "2+ named symbols per stop description" in the prompt;
  drop hallucinated stops entirely rather than keep them with broken
  anchors (a tour stop without a real file anchor would open to a 404).
- *workflow_story*: max_tokens bump after haiku hit 23.9 k and
  truncated its Observability section.

**Haiku results after tightening (iteration 7, 2026-04-19):**

| Artifact | Sections | H / M / L | Halluc | First-class? |
|---|---:|---|---:|---|
| learning_path | 15 | 9 / 2 / 4 | 2.0% | borderline (target 10 HIGH; 4 LOW over limit) |
| code_tour | 15 | 9–14 / 0–6 / 0–1 | 0.0% | ✅ at target (run-to-run HIGH count variance) |
| workflow_story | 9 | 6 / 2 / 1 | 0.0% | ✅ at target (67% HIGH) |

A top-5 cross-model sweep — same five models as the cliff-notes
leaderboard above — is in progress to confirm these numbers generalise.
Results will be appended here when it completes.
