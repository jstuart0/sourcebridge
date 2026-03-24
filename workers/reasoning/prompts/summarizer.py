"""Prompt templates for code summarization."""

FUNCTION_SUMMARY_SYSTEM = (
    "You are a code analysis assistant. Given a code entity (function, method,"
    " or class), produce a structured JSON summary.\n\n"
    "Return ONLY valid JSON with these fields:\n"
    '- "purpose": string — one-sentence description of what this code does\n'
    '- "inputs": list of strings — parameter names and their roles\n'
    '- "outputs": list of strings — what the code returns or produces\n'
    '- "dependencies": list of strings — other functions/modules this code'
    " calls or depends on\n"
    '- "side_effects": list of strings — external effects (I/O, mutations,'
    " network calls)\n"
    '- "risks": list of strings — potential failure modes or security concerns\n'
    '- "confidence": float 0-1 — how confident you are in this analysis\n\n'
    "Do NOT include any text outside the JSON object."
)

FILE_SUMMARY_SYSTEM = (
    "You are a code analysis assistant. Given a source file with its symbols,"
    " produce a structured JSON summary of the file.\n\n"
    "Return ONLY valid JSON with these fields:\n"
    '- "purpose": string — what this file/module is responsible for\n'
    '- "inputs": list of strings — key imports and dependencies\n'
    '- "outputs": list of strings — exported symbols and their roles\n'
    '- "dependencies": list of strings — external packages and internal'
    " modules used\n"
    '- "side_effects": list of strings — global state, I/O, or registration'
    " effects\n"
    '- "risks": list of strings — potential issues at the file level\n'
    '- "confidence": float 0-1 — how confident you are in this analysis\n\n'
    "Do NOT include any text outside the JSON object."
)

MODULE_SUMMARY_SYSTEM = (
    "You are a code analysis assistant. Given a module (directory of related"
    " files), produce a structured JSON summary.\n\n"
    "Return ONLY valid JSON with these fields:\n"
    '- "purpose": string — what this module/package is responsible for\n'
    '- "inputs": list of strings — key external dependencies\n'
    '- "outputs": list of strings — public API surface (exported functions,'
    " types, etc.)\n"
    '- "dependencies": list of strings — other modules this depends on\n'
    '- "side_effects": list of strings — system-level effects\n'
    '- "risks": list of strings — architectural or design concerns\n'
    '- "confidence": float 0-1 — how confident you are in this analysis\n\n'
    "Do NOT include any text outside the JSON object."
)


def build_function_prompt(name: str, language: str, content: str, doc_comment: str) -> str:
    """Build prompt for function-level summary."""
    parts = [f"Summarize this {language} function:\n\nName: {name}"]
    if doc_comment:
        parts.append(f"Documentation:\n{doc_comment}")
    parts.append(f"Code:\n```{language}\n{content}\n```")
    return "\n\n".join(parts)


def build_file_prompt(file_path: str, language: str, symbols: list[str]) -> str:
    """Build prompt for file-level summary."""
    symbol_list = "\n".join(f"  - {s}" for s in symbols)
    return f"Summarize this {language} file:\n\nFile: {file_path}\n\nSymbols defined:\n{symbol_list}"


def build_module_prompt(module_name: str, files: list[str], key_symbols: list[str]) -> str:
    """Build prompt for module-level summary."""
    file_list = "\n".join(f"  - {f}" for f in files)
    symbol_list = "\n".join(f"  - {s}" for s in key_symbols)
    return f"Summarize this module:\n\nModule: {module_name}\n\nFiles:\n{file_list}\n\nKey symbols:\n{symbol_list}"
