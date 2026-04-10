# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""PDF renderer using Playwright (headless Chromium).

Converts Markdown to HTML, then renders to PDF with professional
formatting: cover page, TOC, headers/footers, typography.

Playwright is optional — if not installed, PDF rendering is skipped
and the report is available as Markdown only.
"""

from __future__ import annotations

import logging
import os
import re
from pathlib import Path

logger = logging.getLogger(__name__)

# CSS for professional PDF rendering
PDF_CSS = """\
@page {
  size: letter;
  margin: 1in 0.75in;
  @top-center {
    content: counter(page);
    font-size: 9pt;
    color: #666;
  }
}

body {
  font-family: 'Segoe UI', system-ui, -apple-system, sans-serif;
  font-size: 11pt;
  line-height: 1.6;
  color: #1a1a1a;
  max-width: 100%;
}

h1 {
  font-size: 24pt;
  font-weight: 700;
  color: #111;
  border-bottom: 3px solid #2563eb;
  padding-bottom: 8px;
  margin-top: 2em;
  break-after: avoid;
}

h2 {
  font-size: 16pt;
  font-weight: 600;
  color: #222;
  border-bottom: 1px solid #e5e7eb;
  padding-bottom: 4px;
  margin-top: 1.5em;
  break-after: avoid;
}

h3 {
  font-size: 13pt;
  font-weight: 600;
  color: #333;
  margin-top: 1em;
  break-after: avoid;
}

p { margin: 0.5em 0; }

table {
  width: 100%;
  border-collapse: collapse;
  margin: 1em 0;
  font-size: 10pt;
}

th, td {
  border: 1px solid #e5e7eb;
  padding: 6px 10px;
  text-align: left;
}

th {
  background: #f9fafb;
  font-weight: 600;
}

code {
  background: #f3f4f6;
  padding: 1px 4px;
  border-radius: 3px;
  font-size: 9.5pt;
  font-family: 'JetBrains Mono', 'Fira Code', monospace;
}

pre {
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  padding: 12px;
  overflow-x: auto;
  font-size: 9pt;
}

blockquote {
  border-left: 3px solid #2563eb;
  margin: 1em 0;
  padding: 0.5em 1em;
  background: #f0f9ff;
  color: #1e40af;
}

hr {
  border: none;
  border-top: 2px solid #e5e7eb;
  margin: 2em 0;
}

strong { color: #111; }

/* Severity badges */
.badge-critical { background: #dc2626; color: white; padding: 2px 8px; border-radius: 4px; font-size: 9pt; font-weight: 600; }
.badge-high { background: #ea580c; color: white; padding: 2px 8px; border-radius: 4px; font-size: 9pt; font-weight: 600; }
.badge-medium { background: #d97706; color: white; padding: 2px 8px; border-radius: 4px; font-size: 9pt; font-weight: 600; }
.badge-low { background: #059669; color: white; padding: 2px 8px; border-radius: 4px; font-size: 9pt; font-weight: 600; }

/* Cover page */
.cover-page {
  text-align: center;
  padding-top: 30%;
  break-after: page;
}
.cover-page h1 {
  font-size: 28pt;
  border: none;
}
"""


def markdown_to_html(markdown: str) -> str:
    """Convert Markdown to HTML for PDF rendering.

    This is a simple converter for the structured report output.
    For production, consider using a proper Markdown library.
    """
    html = markdown

    # Headings
    html = re.sub(r"^### (.+)$", r"<h3>\1</h3>", html, flags=re.MULTILINE)
    html = re.sub(r"^## (.+)$", r"<h2>\1</h2>", html, flags=re.MULTILINE)
    html = re.sub(r"^# (.+)$", r"<h1>\1</h1>", html, flags=re.MULTILINE)

    # Bold and italic
    html = re.sub(r"\*\*(.+?)\*\*", r"<strong>\1</strong>", html)
    html = re.sub(r"\*(.+?)\*", r"<em>\1</em>", html)

    # Code
    html = re.sub(r"`([^`]+)`", r"<code>\1</code>", html)

    # Blockquotes
    html = re.sub(r"^> (.+)$", r"<blockquote>\1</blockquote>", html, flags=re.MULTILINE)

    # Horizontal rules
    html = re.sub(r"^---$", r"<hr>", html, flags=re.MULTILINE)

    # Paragraphs
    html = re.sub(r"\n\n", r"</p><p>", html)
    html = f"<p>{html}</p>"

    return f"""<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><style>{PDF_CSS}</style></head>
<body>{html}</body>
</html>"""


async def render_pdf(markdown_path: str, pdf_path: str) -> bool:
    """Render a Markdown file to PDF using Playwright.

    Returns True if successful, False if Playwright is not available.
    """
    try:
        from playwright.async_api import async_playwright
    except ImportError:
        logger.warning("Playwright not installed — PDF rendering skipped. Install with: pip install playwright && playwright install chromium")
        return False

    md_content = Path(markdown_path).read_text()
    html_content = markdown_to_html(md_content)

    # Write temporary HTML file
    html_path = markdown_path.replace(".md", ".html")
    Path(html_path).write_text(html_content)

    try:
        async with async_playwright() as p:
            browser = await p.chromium.launch()
            page = await browser.new_page()
            await page.set_content(html_content, wait_until="networkidle")
            await page.pdf(
                path=pdf_path,
                format="Letter",
                print_background=True,
                margin={"top": "1in", "bottom": "1in", "left": "0.75in", "right": "0.75in"},
            )
            await browser.close()

        logger.info("pdf_rendered", pdf_path=pdf_path, size_bytes=os.path.getsize(pdf_path))
        return True
    except Exception as e:
        logger.error("pdf_render_failed", error=str(e))
        return False
    finally:
        # Clean up temp HTML
        try:
            os.unlink(html_path)
        except OSError:
            pass
