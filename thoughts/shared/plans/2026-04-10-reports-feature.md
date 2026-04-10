# Reports Feature — Plan

**Date:** 2026-04-10
**Author:** Jay Stuart (drafted with Claude)
**Status:** Proposed
**Scope:** Go API, Python worker, Web UI, new report engine

## Overview

SourceBridge currently generates per-repository artifacts (cliff notes, learning paths, code tours, workflow stories) but has no way to produce cross-repository professional reports. Organizations evaluating, auditing, or documenting their software portfolios need structured reports that synthesize analysis across multiple repositories into a single cohesive document.

This plan adds a **Reports** tab to SourceBridge where operators can:

1. Select one or more repositories
2. Choose a report type (Architecture Baseline, SWOT, Environment Evaluation, etc.)
3. Select a **target audience** that controls language, depth, and framing
4. Select which categories/sections to include
5. Generate a professional document in Markdown, PDF, or Word format
6. Download or share the result

When multiple repositories are selected, the report seamlessly integrates findings across all of them, identifying commonalities, inconsistencies, and portfolio-level patterns rather than producing separate per-repo sections.

## Target Audiences

Every report is generated for a specific audience. The audience selection controls:
- **Language complexity** — technical jargon vs. business language
- **Detail level** — implementation specifics vs. strategic implications
- **Framing** — "how it works" vs. "what it means for the business"
- **Recommendations** — tactical fixes vs. strategic initiatives
- **Metrics** — code-level metrics vs. risk/cost/timeline estimates

### Audience Presets

| Audience | Description | Tone | Technical Depth | Focus |
|---|---|---|---|---|
| **C-Suite / Board** | CEOs, CFOs, board members with no technical background | Plain business English, no jargon. Every technical concept explained in one sentence. | Minimal — risks framed as business impact (revenue, liability, reputation). No file paths or code references. | Strategic risk, compliance exposure, investment priorities, business continuity. |
| **Executive / VP** | VPs of Engineering, CTOs, Directors who understand technology at a strategic level | Business-technical hybrid. Technical terms used but always with context. | Moderate — architecture patterns named, risks quantified, but no line-level detail. | Portfolio health, resource allocation, team capability gaps, modernization ROI. |
| **Technical Leadership** | Engineering managers, architects, tech leads who make implementation decisions | Professional technical. Assumes familiarity with frameworks, patterns, and DevOps concepts. | High — specific technologies, version numbers, architectural patterns, integration points. | Architecture quality, technical debt quantification, remediation priorities with effort estimates. |
| **Developer** | Individual contributors who will implement changes | Direct technical. Uses framework/language-specific terminology freely. | Full — file paths, function names, code patterns, specific CVEs, exact configuration changes. | What to fix, where to fix it, how to fix it, in what order. |
| **Compliance / Audit** | Compliance officers, auditors, legal teams reviewing technical systems | Formal, evidence-based. Maps findings to specific control frameworks. | Moderate — enough technical detail to substantiate findings, but framed in compliance language. | Control gaps, evidence inventory, remediation requirements, regulatory exposure. |
| **Non-Technical Stakeholder** | Project managers, product owners, business analysts | Accessible language. Technical concepts explained with analogies. | Low — focuses on "what does this mean" rather than "how does it work." | Timeline impact, user-facing risk, feature delivery implications, resource needs. |

### How Audience Affects Each Section

The same section data produces different output depending on audience. Example for a "Testing" finding where no automated tests exist:

**C-Suite / Board:**
> The applications have no automated safety checks before changes go live. This means every update carries a risk of breaking something in production. In a regulated environment handling student records, this creates both operational risk (service outages) and compliance risk (undetected data exposure). Estimated cost to remediate: 2-4 weeks of engineering effort per application.

**Developer:**
> None of the 7 repositories contain automated test suites. No `jest.config`, `vitest.config`, `pytest.ini`, or test directories were detected. The `package.json` files in the Next.js applications have no `test` script defined. The only test found across the portfolio is a single manual pipeline test in AskMax (`tests/test_ingestion.py`). Recommended: Start with integration tests for the most critical data mutation endpoints in each application, using the existing Supabase test helpers.

**Compliance / Audit:**
> Finding: No automated testing controls exist across the application portfolio. This represents a gap in change management controls. Without automated validation, there is no systematic assurance that code changes do not introduce security regressions or data handling violations. This affects the ability to demonstrate compliance with FERPA's requirement for reasonable safeguards (34 CFR 99.31) and GLBA's written information security program requirements. Remediation: Implement automated testing as a required gate before production deployment.

### Implementation

Audience is stored on the report record and passed to every section generation prompt. The prompt template includes audience-specific instructions:

```python
AUDIENCE_INSTRUCTIONS = {
    "c_suite": {
        "language": "Write in plain business English. No technical jargon — if you must use a technical term, explain it in one sentence. Frame everything in terms of business impact: risk, cost, liability, reputation, and timeline.",
        "depth": "Do not reference file names, code patterns, or specific technologies unless absolutely necessary. Focus on what the finding means for the organization.",
        "recommendations": "Frame recommendations as business decisions with estimated cost, timeline, and risk reduction. Use language like 'invest in' not 'implement'.",
        "metrics": "Use business metrics: estimated cost to fix, risk level (High/Medium/Low), compliance impact, timeline to remediate.",
    },
    "executive": {
        "language": "Use business-technical hybrid language. Technical terms are acceptable if they're commonly understood by engineering leaders. Avoid deep implementation details.",
        "depth": "Name technologies and architectural patterns but don't go to file/function level. Quantify technical debt in terms of effort and risk.",
        "recommendations": "Frame as strategic initiatives with team allocation and quarterly timeline. Include trade-offs.",
        "metrics": "Use portfolio-level metrics: repo health scores, vulnerability counts, coverage percentages, effort estimates in person-weeks.",
    },
    "technical_leadership": {
        "language": "Professional technical writing. Assume the reader understands software architecture, CI/CD, and cloud infrastructure.",
        "depth": "Include specific technologies, version numbers, architectural patterns, and integration points. Reference specific repos when they diverge from the norm.",
        "recommendations": "Provide prioritized remediation with effort estimates (T-shirt sizing) and dependency chains. Flag quick wins separately from structural changes.",
        "metrics": "Include code-level metrics: complexity scores, dependency counts, test ratios, OWASP finding counts by severity.",
    },
    "developer": {
        "language": "Direct technical language. Use framework-specific terminology freely.",
        "depth": "Full detail: file paths, function names, code patterns, specific CVE numbers, exact configuration changes needed.",
        "recommendations": "Provide specific fix instructions: which file to change, what to add, code examples where helpful. Ordered by priority.",
        "metrics": "Raw metrics: line counts, function counts, dependency versions, specific vulnerability IDs.",
    },
    "compliance": {
        "language": "Formal, evidence-based language suitable for regulatory documentation. Use passive voice where appropriate for findings.",
        "depth": "Enough technical detail to substantiate each finding, but always framed in terms of the applicable control framework.",
        "recommendations": "Map each recommendation to a specific control requirement. Include evidence expectations for demonstrating remediation.",
        "metrics": "Control coverage percentages, finding counts by severity mapped to control categories, evidence inventory completeness.",
    },
    "non_technical": {
        "language": "Accessible, jargon-free language. Explain technical concepts with everyday analogies. Use 'the system' not 'the application server'.",
        "depth": "Focus on outcomes and user-facing impact. Avoid internal architecture details entirely.",
        "recommendations": "Frame in terms of what the team needs (time, people, budget) and what stakeholders will see when it's done.",
        "metrics": "Simple risk ratings (Red/Yellow/Green), estimated timeline, user impact scope.",
    },
}
```

## Report Types

### 1. Software Architecture Baseline

The flagship report type. Produces a comprehensive assessment of one or more applications covering architecture, security, operations, and organizational readiness. Based on the Hoegg Software template used for the MACU engagement.

**Sections (all optional, operator selects which to include):**

| Category | Section | Data Sources |
|---|---|---|
| **Executive** | Executive Summary | AI synthesis of all selected sections |
| **Executive** | Overall Assessment | AI synthesis + understanding scores |
| **Portfolio** | Applications Inventory | Repository metadata, file counts, languages |
| **Portfolio** | Application Access | Inferred from auth patterns in code |
| **Security** | OWASP Security Findings | Automated OWASP scan (new capability) |
| **Security** | Software Vulnerability Management | Dependency audit (npm audit, pip audit) |
| **Security** | Supply Chain Vulnerabilities | Package analysis |
| **Security** | Protecting User Information | TLS analysis, data handling patterns |
| **Security** | Compliance Considerations | Inferred from data types + industry (FERPA, GLBA, HIPAA, PCI-DSS, SOC 2) |
| **Access** | Authentication (AAA) | Auth pattern detection from code |
| **Access** | Authorization | Role/permission pattern analysis |
| **Access** | Audit Trail | Logging pattern analysis |
| **Operations** | System Availability | Infrastructure pattern analysis |
| **Operations** | Incident Response | Runbook/doc detection |
| **Operations** | Deployment Architecture | Dockerfile, CI/CD, cloud config analysis |
| **Operations** | Application Secrets | Secret pattern detection (.env, vault references) |
| **Delivery** | Requirements and Project Management | Tool detection (Linear, Jira, etc.) |
| **Delivery** | Testing | Test file detection, coverage inference |
| **Delivery** | Build and Deployment | CI/CD pipeline analysis |
| **Delivery** | Monitoring and Configuration | Observability tool detection |
| **Delivery** | Bugfixes and Patches | Git history analysis |
| **Governance** | Application Ownership and Responsibility | CODEOWNERS, contributor analysis |
| **Governance** | Access and Administrative Control | Platform access analysis |
| **Data** | Data Inventory and Handling | Schema/model analysis |
| **Data** | Third-Party Services and Dependencies | Package.json, requirements.txt analysis |
| **Data** | Secrets and Credential Management | Secret pattern analysis |
| **Data** | Backup, Recovery, and Resilience | Infrastructure config analysis |
| **Observability** | Logging, Monitoring, and Observability | Log library detection |
| **Integration** | Integration and System Interactions | API call detection, shared DB detection |
| **Operations** | Operational Workflow and Support Model | Doc/runbook analysis |
| **Engineering** | Source Control and Development Practices | Git workflow analysis (branching, PRs, reviews) |
| **Engineering** | Documentation and Knowledge Management | Doc file detection, README analysis |
| **Engineering** | Application Lifecycle and Maintenance | Dependency age, update frequency |
| **Users** | System Usage and User Base | Inferred from auth + UI patterns |
| **Users** | Training and User Enablement | Doc/guide detection |
| **Review** | Architecture Review Key Findings | AI deep analysis synthesis |
| **Appendix** | Team Members | Git contributor extraction |
| **Appendix** | OWASP Security Scan Results | Per-repo detailed findings |
| **Appendix** | Dependency Audit Results | Per-repo npm/pip audit |

### 2. SWOT Analysis

Strategic assessment of the software portfolio.

**Sections:**

| Section | Description | Data Sources |
|---|---|---|
| Strengths | What the codebase does well | Code quality metrics, test coverage, documentation, architecture patterns |
| Weaknesses | Technical debt, gaps, risks | Missing tests, outdated deps, security findings, complexity hotspots |
| Opportunities | Improvements and modernization paths | Framework upgrades, consolidation opportunities, automation gaps |
| Threats | External and internal risks | Vulnerability exposure, compliance gaps, bus factor, dependency risks |
| Recommendations | Prioritized action items | Synthesized from all quadrants |

### 3. Environment Evaluation

Assessment of the overall technology environment as ascertained from code and available files.

**Sections:**

| Section | Description |
|---|---|
| Technology Stack Summary | Languages, frameworks, databases, cloud services across all repos |
| Infrastructure Topology | Deployment targets, hosting, networking patterns |
| Development Toolchain | Build tools, linters, formatters, CI/CD |
| Code Quality Metrics | Complexity, duplication, test ratios, doc coverage |
| Security Posture | Aggregated vulnerability findings |
| Operational Maturity | Monitoring, logging, alerting, runbooks |
| Integration Map | How systems connect, shared databases, APIs |
| Compliance Readiness | Gap analysis against common frameworks |
| Cost and Resource Implications | Cloud resource patterns, scaling indicators |
| Modernization Readiness | Framework versions, migration complexity |

### 4. Technical Due Diligence

For M&A, investment, or vendor evaluation scenarios.

**Sections:**

| Section | Description |
|---|---|
| Executive Summary | Investment-grade overview |
| Technology Risk Assessment | Technical debt quantification |
| Scalability Analysis | Architecture scaling patterns |
| Team and Knowledge Risk | Bus factor, documentation gaps |
| IP and Licensing | License analysis across dependencies |
| Security and Compliance | Vulnerability and compliance summary |
| Operational Continuity | Deployment, monitoring, incident readiness |
| Estimated Remediation Effort | Prioritized backlog with rough sizing |

### 5. Compliance Gap Analysis

Assessment against a specific framework (FERPA, HIPAA, SOC 2, PCI-DSS, NIST CSF).

**Sections:**

| Section | Description |
|---|---|
| Framework Overview | Selected compliance framework summary |
| Control Mapping | Map code evidence to framework controls |
| Gap Identification | Controls with insufficient evidence |
| Risk Rating | Per-control risk assessment |
| Remediation Roadmap | Prioritized steps to close gaps |
| Evidence Inventory | What evidence exists in the codebase |

### 6. Portfolio Health Dashboard Report

Periodic summary of all indexed repositories.

**Sections:**

| Section | Description |
|---|---|
| Portfolio Overview | Repo count, total LOC, languages, activity |
| Understanding Scores | Score breakdown per repo |
| Security Summary | Aggregated findings across repos |
| Freshness Report | Stale artifacts, outdated deps |
| Activity Summary | Recent commits, active contributors |
| Cross-Repo Patterns | Shared libraries, common patterns, inconsistencies |

## Multi-Repository Analysis

When multiple repos are selected, the report engine does NOT produce isolated per-repo sections. Instead:

1. **Shared context**: The engine first identifies what the repos have in common (shared tech stack, common patterns, shared dependencies, same deployment target).

2. **Unified narrative**: Each section discusses the portfolio as a whole, calling out specific repos only when they differ from the norm. Example: "Five of seven applications use Next.js with Supabase. The transcript analyzer uses Flask/React and deploys to EC2 separately."

3. **Cross-repo findings**: The engine surfaces patterns that only become visible across repos: inconsistent auth strategies, mixed deployment models, dependency version drift, etc.

4. **Per-repo appendices**: Detailed per-repo findings (OWASP scans, dependency audits) go in appendices, not the main narrative.

## Output Formats

### Markdown
- Primary internal format
- Clean, structured with proper heading hierarchy
- Tables for structured data
- Code blocks for technical evidence
- Diagram placeholders as `[DIAGRAM: description]` blocks

### PDF
- Professional formatting with cover page
- Table of contents with page numbers
- Header/footer with report title and page numbers
- Consistent typography and spacing
- Color-coded severity badges (Critical=red, High=orange, etc.)
- Generated via a headless Chromium renderer from the Markdown (Puppeteer or similar)

### Word (.docx)
- Editable format for client delivery
- Same structure as PDF but in .docx
- Proper Word styles (Heading 1, Heading 2, Body, etc.) so users can update the TOC
- Generated via `docx` npm package or Python `python-docx`

### Diagram Placeholders
When the operator enables "Include diagram placeholders":
- Architecture diagrams: `[DIAGRAM: Deployment Architecture — show hosting topology, data flow, and external service connections]`
- Integration maps: `[DIAGRAM: System Integration Map — show how applications connect to each other and shared services]`
- Data flow: `[DIAGRAM: Data Flow Diagram — show how sensitive data moves through the system]`
- Network topology: `[DIAGRAM: Network Topology — show infrastructure layout]`

These are formatted as styled placeholder boxes in PDF/Word with enough description that someone can draw the actual diagram.

### PDF Rendering

**Primary approach: Playwright (headless Chromium)**

Render report HTML/CSS to PDF via `page.pdf()`. This gives full CSS/JS power — the same engine that renders the SourceBridge web UI renders the report. Charts, colored badges, gradients, custom fonts, branding all render faithfully.

Cover page, TOC, headers/footers are achieved via CSS print styles. TOC with accurate page numbers uses a two-pass render (render once to measure, inject TOC, render final).

Docker impact: ~1.8GB for Chromium. Runs as a sidecar or within the Python worker.

## Evidence System and Appendices

Reports are not just narratives — they are **evidence-backed claims**. Every substantive finding in the report body links to supporting evidence in the appendices.

### How It Works

1. **Evidence markers in body text**: The LLM generates inline evidence markers as it writes each section. Markers reference specific evidence items:
   > The housing application exposes student PII without authentication. **[E-SEC-01]** The API endpoint accepts arbitrary application IDs and returns full records. **[E-SEC-02]**

2. **Evidence registry**: During report generation, every piece of evidence is registered with a unique ID, category, and source:
   - `E-SEC-01`: OWASP scan finding — `/api/application/lookup` has no auth check (file: `app/api/application/lookup/route.ts`, line 5-37)
   - `E-SEC-02`: Code analysis — service role key used without session verification (file: `app/api/application/lookup/route.ts`, line 15)

3. **Auto-generated appendices**: Evidence items are grouped into appendices by category:
   - **Appendix A**: OWASP Security Scan Results (per repo)
   - **Appendix B**: Dependency Audit Results (per repo)
   - **Appendix C**: Code Analysis Evidence (file paths, line numbers, code snippets)
   - **Appendix D**: Git History Evidence (commit patterns, branching analysis)
   - **Appendix E**: Configuration Evidence (detected configs, missing configs)
   - Additional appendices generated dynamically based on what evidence exists

4. **Evidence detail levels by audience**:
   - **C-Suite**: Evidence markers are footnote-style numbers. Appendices exist but are summarized.
   - **Developer**: Evidence markers link to specific files and lines. Appendices include code snippets.
   - **Compliance**: Evidence markers map to control framework IDs. Appendices structured as evidence packages.

### Evidence Data Model

```
ca_report_evidence
  id string primary                    -- e.g. "E-SEC-01"
  report_id uuid                       -- FK to ca_report
  category string                      -- "security" | "architecture" | "operations" | etc.
  title string                         -- human-readable evidence title
  description string                   -- what the evidence shows
  source_type string                   -- "owasp_scan" | "code_analysis" | "dependency_audit" | "git_history" | "config_detection"
  source_repo_id string                -- which repo this came from
  file_path string                     -- specific file (if applicable)
  line_start int                       -- line range (if applicable)
  line_end int
  code_snippet string                  -- relevant code excerpt (if applicable)
  raw_data string                      -- JSON blob of the full evidence payload
  severity string                      -- "critical" | "high" | "medium" | "low" | "info"
  created_at datetime
```

## Level of Effort (LOE) Estimation

Reports that include recommendations should quantify the effort required. The LOE estimation system is **configurable** between two modes:

### Estimation Modes

| Mode | Unit | Description | Best For |
|---|---|---|---|
| **Human Hours** | Person-hours / person-weeks | Traditional estimate assuming human developers implementing changes | Organizations with in-house or contracted dev teams |
| **AI-Assisted** | AI agent hours + human review hours | Estimate assuming AI coding agents (Claude Code, Cursor, Copilot) handle implementation with human review/approval | Organizations leveraging AI development tools |

### How It Works

The operator selects the estimation mode in the report wizard (Step 4, alongside section selection). The mode affects how every recommendation's LOE is framed:

**Human Hours example:**
> **Recommendation: Add authentication to all server actions**
> - Effort: 16-24 person-hours (2-3 developer days)
> - Complexity: Medium
> - Prerequisites: None
> - Risk: Low (additive change, no existing behavior modified)

**AI-Assisted example:**
> **Recommendation: Add authentication to all server actions**
> - AI agent effort: 2-4 hours (pattern is repetitive, well-suited to AI implementation)
> - Human review: 1-2 hours (security-critical, requires careful review)
> - Total: 3-6 hours
> - Complexity: Low for AI (clear pattern, many examples in codebase)
> - Prerequisites: None
> - Risk: Low (additive change, but security code requires human sign-off)

### Estimation Factors

The LLM considers these factors when producing LOE estimates:

1. **Scope**: How many files/functions need to change
2. **Pattern repetitiveness**: Is this the same change 15 times? (AI excels here)
3. **Novelty**: Is this a well-known pattern or novel design work? (Humans may be faster for novel architecture)
4. **Risk level**: Security-critical changes need more human review time regardless of mode
5. **Dependencies**: Does this block or depend on other changes?
6. **Testing overhead**: How much testing does the change need?

### Configuration

```python
LOE_MODE_INSTRUCTIONS = {
    "human_hours": {
        "unit": "person-hours",
        "description": "Estimate assuming experienced human developers implementing changes manually.",
        "guidance": "Use person-hours for individual tasks, person-weeks for larger initiatives. "
                    "Assume a mid-level developer familiar with the tech stack but new to this specific codebase. "
                    "Include time for understanding, implementing, testing, and code review.",
    },
    "ai_assisted": {
        "unit": "AI agent hours + human review hours",
        "description": "Estimate assuming AI coding agents handle implementation with human review.",
        "guidance": "Split each estimate into AI agent time and human review time. "
                    "AI agents excel at: repetitive changes, boilerplate, test generation, dependency updates, "
                    "and well-documented pattern implementations. "
                    "Humans are still needed for: architectural decisions, security review, novel design, "
                    "integration testing, and final approval. "
                    "Be realistic — not everything is faster with AI. Novel architecture work may take "
                    "similar time. But 15 identical auth guard additions across server actions is 10x faster.",
    },
}
```

### Data Model Addition

```sql
-- Add to ca_report
  loe_mode string default 'human_hours'  -- "human_hours" | "ai_assisted"
```

## Data Pipeline

### Phase 1: Data Collection

For each selected repository, gather:

1. **Existing SourceBridge data** (already available):
   - Repository metadata (name, path, file count, languages)
   - Symbol index (functions, classes, modules)
   - Understanding score breakdown
   - Cliff notes sections (if generated)
   - Learning path (if generated)
   - Code tour (if generated)
   - Workflow stories (if generated)
   - Requirement links (if imported)
   - Architecture diagrams (if generated)

2. **New analysis passes** (run on demand during report generation):
   - **Dependency audit**: `npm audit --json` / `pip audit --json` output
   - **OWASP scan**: Static analysis against OWASP Top 10 patterns
   - **Git history analysis**: Branching patterns, PR usage, contributor activity
   - **Auth pattern detection**: Scan for authentication/authorization patterns
   - **Secret detection**: Scan for exposed credentials, .env patterns
   - **Test detection**: Find test files, test frameworks, coverage configs
   - **CI/CD detection**: Find GitHub Actions, Dockerfile, deployment configs
   - **License scan**: Analyze dependency licenses

### Phase 2: Cross-Repository Synthesis

When multiple repos are selected:

1. Build a **portfolio context document** that describes:
   - Common technology stack
   - Shared deployment patterns
   - Consistent vs inconsistent practices
   - Portfolio-level metrics (total LOC, total deps, etc.)

2. Feed this context into every section's generation prompt so the LLM writes in portfolio terms, not per-repo terms.

### Phase 3: Section Generation

For each selected section:

1. Assemble the relevant data slice (e.g., for "Testing", gather test file counts, framework detection results, coverage configs across all selected repos)
2. Build a section-specific prompt with the data and the portfolio context
3. Call the LLM to generate the section narrative
4. Parse the result into structured Markdown

If no data is available for a section, generate a placeholder:

```markdown
## Testing

> **No automated testing evidence was detected** across the selected repositories.
> This section would typically cover test frameworks, coverage metrics, CI integration,
> and testing practices. Consider adding automated tests to improve confidence in
> code changes.
>
> _[PLACEHOLDER — Update this section after implementing testing]_
```

### Phase 4: Assembly and Rendering

1. Combine all generated sections into the final document
2. Generate the Executive Summary last (it synthesizes all sections)
3. Add cover page, TOC, and appendices
4. Render to the selected output format(s)

## Architecture

### New Go components

```
internal/reports/
  types.go           — ReportType, ReportConfig, ReportSection, ReportStatus
  store.go           — ReportStore interface (CRUD for report records)
  engine.go          — Orchestrates data collection + section generation
  collector.go       — Gathers data from graph store, knowledge store, git
  renderer_md.go     — Assembles Markdown output
  renderer_pdf.go    — Markdown -> PDF via headless Chrome
  renderer_docx.go   — Markdown -> Word via template

internal/db/
  report_store.go    — SurrealDB implementation of ReportStore
  migrations/
    020_reports.surql — ca_report table

internal/api/rest/
  admin_reports.go   — REST handlers for report CRUD + download

internal/api/graphql/
  report_resolvers.go — GraphQL queries/mutations for reports
```

### New Python components

```
workers/reports/
  __init__.py
  section_generator.py — Per-section LLM generation
  prompts/
    architecture_baseline.py — Section prompts for arch baseline
    swot.py                  — Section prompts for SWOT
    environment_eval.py      — Section prompts for env evaluation
    due_diligence.py         — Section prompts for tech DD
    compliance_gap.py        — Section prompts for compliance
    portfolio_health.py      — Section prompts for portfolio health
  analyzers/
    dependency_audit.py      — npm audit / pip audit runner
    owasp_scanner.py         — Static OWASP pattern detection
    auth_detector.py         — Authentication pattern detection
    secret_scanner.py        — Credential/secret pattern detection
    test_detector.py         — Test framework/coverage detection
    cicd_detector.py         — CI/CD pipeline detection
    git_analyzer.py          — Git history/workflow analysis
    license_scanner.py       — Dependency license analysis
```

### New Frontend

```
web/src/app/(app)/reports/
  page.tsx                   — Reports list page
  new/page.tsx               — New report wizard
  [id]/page.tsx              — Report detail/download page

web/src/components/reports/
  ReportTypeCard.tsx          — Card for selecting report type
  RepoSelector.tsx            — Multi-repo selector
  SectionSelector.tsx         — Category/section checklist
  ReportProgress.tsx          — Generation progress display
  ReportPreview.tsx           — Markdown preview
```

## Data Model

```sql
ca_report
  id uuid primary
  name string                           -- user-provided report name
  report_type string                    -- "architecture_baseline" | "swot" | "environment_eval" | etc.
  audience string                       -- "c_suite" | "executive" | "technical_leadership" | "developer" | "compliance" | "non_technical"
  repository_ids array<string>          -- selected repos
  selected_sections array<string>       -- which sections to include
  include_diagrams bool default false   -- include diagram placeholders
  output_formats array<string>          -- ["markdown", "pdf", "docx"]
  status string                         -- "pending" | "collecting" | "generating" | "rendering" | "ready" | "failed"
  progress float default 0.0
  progress_phase string default ''
  progress_message string default ''
  error_code string default ''
  error_message string default ''
  -- Generated content
  markdown_content string default ''    -- full rendered Markdown
  pdf_path string default ''            -- path to generated PDF
  docx_path string default ''           -- path to generated Word doc
  section_count int default 0
  word_count int default 0
  -- Metadata
  created_by string default ''
  created_at datetime default time::now()
  updated_at datetime default time::now()
  completed_at option<datetime>
```

## UI Flow

### Reports List Page (`/reports`)

- Empty state: "No reports yet. Create your first report to get a professional analysis of your codebase."
- List of past reports with: name, type badge, repo count, status, date, download buttons
- "New Report" button

### New Report Wizard (`/reports/new`)

**Step 1: Report Type**
- Grid of report type cards (Architecture Baseline, SWOT, Environment Eval, Due Diligence, Compliance Gap, Portfolio Health)
- Each card has: icon, title, one-line description, "Select" button

**Step 2: Select Audience**
- Grid of audience cards, each showing:
  - Audience name (e.g., "C-Suite / Board")
  - One-line description (e.g., "Plain business language, strategic risk focus")
  - Example sentence showing the tone
- Default: "Technical Leadership"

**Step 3: Select Repositories**
- Multi-select list of all indexed repositories
- Each shows: name, status badge, understanding score, last indexed date
- "Select All" / "Deselect All" buttons
- Minimum 1 repo required

**Step 4: Configure Sections**
- Grouped checklist of all sections for the chosen report type
- "Select All" / "Deselect All" per category
- Diagram placeholder toggle
- Output format checkboxes (Markdown, PDF, Word)
- Report name input

**Step 5: Generate**
- Summary of selections
- "Generate Report" button
- Progress display with phase labels:
  - "Collecting repository data..." (10-30%)
  - "Analyzing security patterns..." (30-50%)
  - "Generating sections..." (50-80%)
  - "Assembling document..." (80-90%)
  - "Rendering output formats..." (90-100%)

### Report Detail Page (`/reports/[id]`)

- Report header with name, type, date, word count
- Markdown preview (rendered in-browser)
- Download buttons for each generated format
- "Regenerate" button
- Section navigation sidebar

## Prompt Engineering Approach

Each section gets a dedicated prompt that includes:

1. **Portfolio context** — what repos are included, their common patterns
2. **Section-specific data** — the relevant analysis results
3. **Output format instructions** — Markdown with specific heading levels
4. **Tone and style** — professional, grounded, evidence-based
5. **Quality bar** — minimum word count, specificity requirements, evidence citations

Example prompt structure for an Architecture Baseline section:

```
You are writing the "{section_title}" section of a Software Architecture Baseline report.

Portfolio: {repo_count} repositories ({repo_names})
Common stack: {common_technologies}

Evidence for this section:
{section_specific_data}

Write this section as part of a professional architecture review document.
Requirements:
- Write in third person, present tense
- Be specific: name files, technologies, versions, and patterns
- When repos differ, explain the variation
- If evidence is insufficient, note what is unknown and what should be validated
- Include "Areas for Validation / Questions" subsection where appropriate
- Minimum {word_count} words
- Use Markdown formatting: ## for section title, ### for subsections, tables where structured data fits

Output the section content in Markdown. Do not include the report title or other sections.
```

## Implementation Phases

### Phase R1: Foundation (report model + basic generation)
- SurrealDB migration for `ca_report`
- Go report store + REST endpoints
- Basic report wizard UI (type select, repo select, generate)
- Single report type: Architecture Baseline with 5 core sections
- Markdown output only
- Uses existing cliff notes + understanding scores as data

### Phase R2: Full Architecture Baseline
- All Architecture Baseline sections
- New Python analyzers: dependency audit, auth detection, test detection, git analysis
- OWASP scanner integration
- Multi-repo cross-analysis
- PDF output via Puppeteer/wkhtmltopdf

### Phase R3: Additional Report Types + Word Output
- SWOT Analysis
- Environment Evaluation
- Portfolio Health Dashboard
- Word (.docx) output
- Diagram placeholders

### Phase R4: Advanced Report Types + Polish
- Technical Due Diligence
- Compliance Gap Analysis
- Section-level regeneration (re-run one section without redoing the whole report)
- Report templates (save section selections as reusable templates)
- Report scheduling (auto-generate weekly/monthly)

## Success Criteria

1. An operator who has never used SourceBridge can generate a professional Architecture Baseline report for 3 repositories in under 5 minutes of wall-clock configuration time.
2. The generated PDF is suitable for direct client delivery without manual formatting.
3. Multi-repo reports read as a unified narrative, not disconnected per-repo sections.
4. Every claim in the report cites specific evidence (file paths, patterns, metrics).
5. Sections with insufficient data produce useful placeholders rather than fabricated content.
6. The report engine leverages existing SourceBridge analysis (cliff notes, understanding scores) rather than re-analyzing from scratch.

## Resolved Design Decisions

1. **PDF rendering**: Playwright (headless Chromium). Full CSS/JS power, consulting-grade visuals.

2. **Report storage**: Markdown in SurrealDB (searchable text). PDF/DOCX files on disk at `/data/reports/{report_id}/` with paths stored in DB.

3. **Concurrent report generation**: Separate queue from artifact generation with its own concurrency limit (default 1 report at a time). Reports are expensive and shouldn't starve cliff notes.

4. **Incremental updates**: Mark existing reports as "stale" when underlying repos are re-indexed (badge on report list). Do NOT auto-regenerate — reports are deliberate deliverables, not background artifacts.

5. **OWASP scanning**: Python analyzer within the worker. Runs as part of the report data collection phase, not a standalone service.

## Reports UI/UX Principles

The reports feature handles the most complex user flow in SourceBridge — multi-step configuration with many options. If this feels like an enterprise admin wizard, operators will abandon it. Every screen must be designed for someone who has never generated a report before.

### Core UX Rules

1. **The wizard should feel like a conversation, not a form.** Each step asks one question: "What kind of report?" "Who is it for?" "Which repos?" "What should it cover?" One decision per screen. No scrolling walls of checkboxes.

2. **Smart defaults eliminate decisions.** When you pick "Architecture Baseline" + "C-Suite", the system pre-selects the sections that matter for that combination and hides the ones that don't. The operator can customize, but the default should be good enough to just click "Generate."

3. **Show what you'll get before you commit.** Before the final "Generate" button, show a preview card: "Architecture Baseline for 3 repos, C-Suite audience, 18 sections, ~25 pages, estimated 8-12 minutes." No surprises.

4. **Progress is narrative, not a number.** Instead of "47%", show: "Analyzing security patterns across MACU Helpdesk... (3 of 7 repositories complete)". The operator should know what's happening, not just that something is happening.

5. **The report preview is the hero.** When generation completes, the first thing the operator sees is the rendered report — not a download button. They should be able to read it, scroll through it, and decide if it's good before downloading. One-click download for each format sits in a sticky header bar.

6. **Regeneration is surgical, not nuclear.** If one section is weak, the operator can click "Regenerate this section" without redoing the whole report. If the overall framing is wrong, "Regenerate all" starts fresh.

7. **Empty states are invitations.** The reports list starts with: "Reports turn SourceBridge's analysis into professional documents you can share. Pick a report type to get started." Not a blank table.

8. **The report type cards sell themselves.** Each card shows: the type name, a one-line value proposition ("Show your board the real state of your software portfolio"), an example output thumbnail, and estimated generation time. The operator should want to click one.

9. **Audience selection uses examples, not descriptions.** Instead of "C-Suite: plain business English...", show a 2-sentence sample of what the report will actually sound like for that audience. Let the operator read the tone and pick the one that fits.

10. **Download is not the end.** After download, suggest next steps: "Share this report with stakeholders" or "Schedule this report to run monthly" or "Generate a deeper version with more sections."

### Wizard Step Design

**Step 1 — Report Type** (full-width cards, 2 per row)
```
+----------------------------------+  +----------------------------------+
|  [icon]                          |  |  [icon]                          |
|  Architecture Baseline           |  |  SWOT Analysis                   |
|  Comprehensive technical review  |  |  Strategic strengths & risks     |
|  of your software portfolio.     |  |  assessment for leadership.      |
|  ~30 sections | ~40 pages        |  |  ~5 sections | ~10 pages         |
|  [Select]                        |  |  [Select]                        |
+----------------------------------+  +----------------------------------+
```

**Step 2 — Audience** (horizontal cards with tone samples)
```
+-------------------+  +-------------------+  +-------------------+
| C-Suite / Board   |  | Technical Lead    |  | Developer         |
| "The portfolio    |  | "The 4 Supabase   |  | "Add auth guard   |
| carries risk that |  | apps bypass RLS   |  | to route.ts L5:   |
| should be..."     |  | via service keys" |  | const user = ..." |
| [Select]          |  | [Select]          |  | [Select]          |
+-------------------+  +-------------------+  +-------------------+
```

**Step 3 — Repositories** (clean multi-select with search)
```
Search: [________________]  [Select All] [Deselect All]

[x] MACU Helpdesk        Score: 27  |  21 files  |  Indexed 2h ago
[x] MACU Residence       Score: 15  |  34 files  |  Indexed 2h ago
[ ] MACU Fleetly         Score: 22  |  18 files  |  Indexed 2h ago
                                            3 of 7 selected
```

**Step 4 — Sections** (grouped with smart defaults pre-checked)
```
Recommended for Architecture Baseline + C-Suite:  [Use recommended]

[v] Executive           [v] Summary  [v] Assessment
[v] Security            [v] OWASP  [v] Compliance  [ ] Supply Chain
[v] Operations          [v] Deployment  [ ] Monitoring  [ ] Secrets
[ ] Engineering         [ ] Source Control  [ ] Testing  [ ] Lifecycle
[v] Appendices          [v] OWASP Results  [ ] Team Members

Options:
  [v] Include diagram placeholders
  [ ] Include code snippets in appendices
  Estimation mode: [Human Hours v]
  Output formats: [v] PDF  [v] Word  [v] Markdown
```

**Step 5 — Confirm & Generate**
```
+----------------------------------------------------------+
|  Architecture Baseline                                    |
|  For: C-Suite / Board                                     |
|  Repos: MACU Helpdesk, MACU Residence (2)                |
|  Sections: 14 selected across 4 categories                |
|  Output: PDF + Word + Markdown                            |
|  Estimated: ~25 pages, 8-12 minutes                       |
|                                                           |
|  Report name: [MACU Architecture Baseline - April 2026]   |
|                                                           |
|  [Generate Report]                                        |
+----------------------------------------------------------+
```

## References

- Sample Architecture Baseline: MACU engagement (Hoegg Software, 2026)
- OWASP Top 10:2025: https://owasp.org/Top10/
- NIST Cybersecurity Framework: https://www.nist.gov/cyberframework
- FERPA requirements: https://studentprivacy.ed.gov/
- python-docx: https://python-docx.readthedocs.io/
- Playwright PDF: https://playwright.dev/docs/api/class-page#page-pdf
- Typst (runner-up PDF option): https://typst.app/
