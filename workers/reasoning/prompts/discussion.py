"""Prompt templates for code discussion."""

DISCUSSION_SYSTEM = (
    "You are a code discussion assistant. Answer questions about the provided"
    " code clearly and accurately.\n\n"
    "Return ONLY valid JSON with these fields:\n"
    '- "answer": string — clear, detailed answer to the question\n'
    '- "references": list of strings — file paths and line ranges referenced'
    ' (e.g., "main.go:10-25")\n'
    '- "related_requirements": list of strings — requirement IDs mentioned or'
    ' relevant (e.g., "REQ-001")\n\n'
    "Do NOT include any text outside the JSON object."
)


def build_discussion_prompt(question: str, context_code: str, context_metadata: str = "") -> str:
    """Build prompt for code discussion."""
    parts = [f"Question: {question}"]
    if context_metadata:
        parts.append(f"Context:\n{context_metadata}")
    parts.append(f"Code:\n{context_code}")
    return "\n\n".join(parts)
