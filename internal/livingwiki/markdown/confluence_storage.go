// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package markdown

// confluence_storage.go — XHTML serialization helpers for the Confluence
// storage format (https://confluence.atlassian.com/doc/confluence-storage-format-790796544.html).
//
// Confluence storage format is XHTML with two custom namespaces:
//   - ac: — Atlassian Confluence macros (<ac:structured-macro>, <ac:parameter>, …)
//   - ri: — Resource identifiers (<ri:page>, <ri:user>, …)
//
// This file provides low-level helpers used by confluence_writer.go. It does
// not depend on any AST types, only on standard library primitives.

import (
	"fmt"
	"io"
	"strings"
)

// xmlEscape replaces the five XML special characters with their entity
// equivalents. It is applied to text content and attribute values that are
// not already enclosed in a CDATA section.
//
// Replacements:
//
//	&  →  &amp;   (must come first to avoid double-escaping)
//	<  →  &lt;
//	>  →  &gt;
//	"  →  &quot;  (needed inside double-quoted attributes)
//	'  →  &apos;  (needed inside single-quoted attributes)
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// writeOpenTag writes an opening XML tag with the given name and attribute
// key/value pairs. attrs must be an even-length slice [k0, v0, k1, v1, …].
// Attribute values are XML-escaped automatically.
func writeOpenTag(w io.Writer, name string, attrs ...string) error {
	if len(attrs)%2 != 0 {
		panic(fmt.Sprintf("confluence_storage: writeOpenTag %q: odd attrs slice length", name))
	}
	if _, err := fmt.Fprintf(w, "<%s", name); err != nil {
		return err
	}
	for i := 0; i < len(attrs); i += 2 {
		if _, err := fmt.Fprintf(w, ` %s="%s"`, attrs[i], xmlEscape(attrs[i+1])); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, ">")
	return err
}

// writeCloseTag writes a closing XML tag.
func writeCloseTag(w io.Writer, name string) error {
	_, err := fmt.Fprintf(w, "</%s>", name)
	return err
}

// (writeSelfClosingTag used to live here; the current Confluence
// storage emitter writes self-closing tags inline at the call site
// when needed, so the helper has no consumers. Removed to satisfy
// lint.)

// writeText writes XML-escaped text content.
func writeText(w io.Writer, text string) error {
	_, err := fmt.Fprint(w, xmlEscape(text))
	return err
}

// writeln writes a newline.
func writeln(w io.Writer) error {
	_, err := fmt.Fprintln(w)
	return err
}

// writeAcParameter writes one <ac:parameter> element.
//
//	<ac:parameter ac:name="name">value</ac:parameter>
func writeAcParameter(w io.Writer, name, value string) error {
	if err := writeOpenTag(w, "ac:parameter", "ac:name", name); err != nil {
		return err
	}
	if err := writeText(w, value); err != nil {
		return err
	}
	return writeCloseTag(w, "ac:parameter")
}

// writeAcMacroOpen writes the opening tag of a structured macro.
//
//	<ac:structured-macro ac:name="macroName">
func writeAcMacroOpen(w io.Writer, macroName string) error {
	if err := writeOpenTag(w, "ac:structured-macro", "ac:name", macroName); err != nil {
		return err
	}
	return writeln(w)
}

// writeAcMacroClose writes the closing tag of a structured macro.
//
//	</ac:structured-macro>
func writeAcMacroClose(w io.Writer) error {
	if err := writeCloseTag(w, "ac:structured-macro"); err != nil {
		return err
	}
	return writeln(w)
}

// writeAcRichTextBody opens and closes an <ac:rich-text-body> wrapper.
// The body parameter is expected to be valid XHTML content (not escaped).
// Callers that build body dynamically should use [writeOpenTag] /
// [writeCloseTag] directly for fine-grained control.
func writeAcRichTextBodyWrap(w io.Writer, bodyFn func(io.Writer) error) error {
	if err := writeOpenTag(w, "ac:rich-text-body"); err != nil {
		return err
	}
	if err := writeln(w); err != nil {
		return err
	}
	if err := bodyFn(w); err != nil {
		return err
	}
	if err := writeCloseTag(w, "ac:rich-text-body"); err != nil {
		return err
	}
	return writeln(w)
}

// codeLanguageToConfluence maps common language identifiers to Confluence's
// Code Macro language parameter values. Unknown languages fall back to "none".
var codeLanguageToConfluence = map[string]string{
	"go":         "go",
	"golang":     "go",
	"python":     "python",
	"py":         "python",
	"javascript": "javascript",
	"js":         "javascript",
	"typescript": "typescript",
	"ts":         "typescript",
	"java":       "java",
	"bash":       "bash",
	"sh":         "bash",
	"shell":      "bash",
	"sql":        "sql",
	"yaml":       "yaml",
	"yml":        "yaml",
	"json":       "javascript", // Confluence code macro has no JSON-specific lang
	"xml":        "xml",
	"html":       "html",
	"css":        "css",
	"ruby":       "ruby",
	"rust":       "rust",
	"cpp":        "c++",
	"c++":        "c++",
	"c":          "c",
	"csharp":     "c#",
	"cs":         "c#",
	"php":        "php",
	"swift":      "swift",
	"kotlin":     "kotlin",
	"scala":      "scala",
	"r":          "r",
}

// confluenceCodeLanguage returns the Confluence code-macro language value for
// the given language hint, defaulting to "none" for unknown languages.
func confluenceCodeLanguage(lang string) string {
	if lang == "" {
		return "none"
	}
	if v, ok := codeLanguageToConfluence[strings.ToLower(lang)]; ok {
		return v
	}
	return "none"
}

// headingTagForLevel returns the XHTML heading tag for a heading level 1–6.
// Levels outside that range are clamped to h1/h6.
func headingTagForLevel(level int) string {
	if level < 1 {
		return "h1"
	}
	if level > 6 {
		return "h6"
	}
	return fmt.Sprintf("h%d", level)
}
