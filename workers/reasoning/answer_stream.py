"""Streaming JSON-answer-field extractor for discussion responses.

The discussion prompt asks the model to return:

    {"answer": "...", "references": [...], "related_requirements": [...]}

Without streaming, the full blob comes back, we parse it with json.loads,
and the caller gets a clean DiscussionAnswer. With streaming we want to
emit the `answer` string to the user as characters arrive rather than
forcing them to watch raw JSON tokens scroll by.

`StreamingAnswerExtractor.feed(chunk)` takes each LLM token (or any sized
chunk) and returns the characters that belong to the answer field,
handling the interstitial JSON noise (keys, quotes, escape sequences,
whitespace). The metadata fields (`references`, `related_requirements`)
are recovered from the full buffered response in `finalize()`.

This is intentionally a tiny hand-rolled state machine rather than a
streaming-JSON library — the discussion schema has exactly one string
field we need to expose progressively, and the amount of noise around
it is well-defined.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field


@dataclass
class StreamingAnswerExtractor:
    """Accumulate LLM tokens and yield answer-field deltas."""

    buffer: str = ""
    _answer_start: int = -1  # index of the opening quote of "answer" value
    _emitted_to: int = -1  # last buffer index we've emitted (within the string)
    _answer_done: bool = False
    _escape_active: bool = False  # next char inside the string is escaped

    # Fast fallback for the case where the model produces plain text instead
    # of JSON — we just stream the raw content through. We detect this by
    # the first non-whitespace character NOT being `{`.
    _plain_mode: bool | None = field(default=None)

    def feed(self, chunk: str) -> str:
        """Append `chunk` and return new answer characters to emit."""
        self.buffer += chunk
        if self._plain_mode is None:
            stripped = self.buffer.lstrip()
            if stripped:
                self._plain_mode = not stripped.startswith("{")
        if self._plain_mode:
            # Whole chunk is part of the visible answer.
            return chunk
        if self._answer_done:
            return ""
        if self._answer_start < 0:
            self._locate_answer_start()
            if self._answer_start < 0:
                return ""
        return self._emit_until_close()

    def finalize(self) -> tuple[str, list[str], list[str]]:
        """Return (answer_text, references, related_requirements).

        Called once the stream is complete. Falls back to a best-effort
        parse of whatever we got; never raises.
        """
        if self._plain_mode:
            return self.buffer.strip(), [], []
        # Try the full JSON parse first — most reliable when the model
        # obeyed the schema.
        try:
            from workers.common.llm.parse import parse_json_response
            data = parse_json_response(self.buffer)
            if isinstance(data, dict):
                answer = data.get("answer") or ""
                refs = _coerce_str_list(data.get("references"))
                reqs = _coerce_str_list(data.get("related_requirements"))
                if isinstance(answer, str):
                    return answer, refs, reqs
        except Exception:
            pass
        # Fallback: use whatever we extracted incrementally; empty lists
        # for metadata.
        return self._buffered_answer(), [], []

    # ----- internals -----

    def _locate_answer_start(self) -> None:
        """Find the opening quote of the answer field's string value.

        Handles optional whitespace and quoted/unquoted key ordering in a
        tolerant way: we look for the pattern "answer" ... "  where the
        `...` is the `:` and any whitespace.
        """
        # Must have seen `"answer"` followed by `:` and an opening `"`.
        match = re.search(r'"answer"\s*:\s*"', self.buffer)
        if not match:
            return
        self._answer_start = match.end()  # index of the first char of value
        self._emitted_to = self._answer_start

    def _emit_until_close(self) -> str:
        """Emit buffer characters from `_emitted_to` up to the closing
        quote of the answer value. Respects `\\"` escapes."""
        if self._answer_start < 0:
            return ""
        delta_chars: list[str] = []
        i = self._emitted_to
        while i < len(self.buffer):
            ch = self.buffer[i]
            if self._escape_active:
                # Emit the unescaped form of the escape sequence.
                delta_chars.append(_unescape(ch))
                self._escape_active = False
                i += 1
                continue
            if ch == "\\":
                self._escape_active = True
                i += 1
                continue
            if ch == '"':
                # End of answer string.
                self._answer_done = True
                self._emitted_to = i + 1
                return "".join(delta_chars)
            delta_chars.append(ch)
            i += 1
        self._emitted_to = i
        return "".join(delta_chars)

    def _buffered_answer(self) -> str:
        """Return whatever characters we emitted so far, for fallback."""
        if self._answer_start < 0:
            return ""
        end = len(self.buffer) if self._emitted_to > self._answer_start else self._answer_start
        return self.buffer[self._answer_start:end].rstrip('"').strip()


def _unescape(ch: str) -> str:
    """Translate the character after a backslash into its literal form.

    Handles the common JSON escapes (n, t, r, ", \\, /). Anything else
    is emitted verbatim with the backslash already consumed — good
    enough for user-visible streaming.
    """
    return {
        "n": "\n",
        "t": "\t",
        "r": "\r",
        '"': '"',
        "\\": "\\",
        "/": "/",
    }.get(ch, ch)


def _coerce_str_list(value: object) -> list[str]:
    if not isinstance(value, list):
        return []
    return [v for v in value if isinstance(v, str) and v.strip()]
