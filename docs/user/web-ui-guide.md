# Web UI Guide

The SourceBridge.ai web dashboard provides a visual interface for exploring your codebase, tracing requirements, and reviewing code.

## Accessing the Web UI

The web UI is available at http://localhost:3000 when running with Docker Compose, or after starting `npm run dev` in the `web/` directory.

## Dashboard

The main dashboard shows:

- **Repository count** — Number of indexed repositories
- **Requirements tracked** — Total requirements imported
- **Coverage** — Percentage of requirements linked to code
- **Recent requirement links** — Latest traceability data

## Repositories Page

Browse indexed repositories with:

- File count, symbol count, and requirement count per repo
- Language detection
- Import requirements directly from the UI

## Requirements Page

Split-panel view:

- **Left panel** — List of requirements with category badges and priority
- **Right panel** — Linked code symbols with confidence badges

Click a requirement to see which code implements it.

## Traceability Matrix

Visual matrix showing requirement-to-code relationships:

- Rows = requirements
- Columns = code symbols
- Colored dots indicate link confidence (green = verified, blue = high, yellow = medium, gray = low)
- Click cells to view link details

## Command Palette

Press `Cmd+K` (or `Ctrl+K`) to open the command palette for quick navigation:

- Go to Dashboard
- Go to Repositories
- Go to Requirements
- Go to Settings

## Settings

Configure:

- **Theme** — Dark or light mode
- **API Endpoint** — Configure via `NEXT_PUBLIC_API_URL` environment variable

## Keyboard Shortcuts

| Shortcut | Action |
|----------|--------|
| `Cmd+K` | Open command palette |
| `G D` | Go to Dashboard |
| `G R` | Go to Repositories |
| `G Q` | Go to Requirements |
| `G S` | Go to Settings |
