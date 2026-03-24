from common.v1 import types_pb2 as _types_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class AnalyzeSymbolRequest(_message.Message):
    __slots__ = ("symbol", "surrounding_context", "repository_id")
    SYMBOL_FIELD_NUMBER: _ClassVar[int]
    SURROUNDING_CONTEXT_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    symbol: _types_pb2.CodeSymbol
    surrounding_context: str
    repository_id: str
    def __init__(self, symbol: _Optional[_Union[_types_pb2.CodeSymbol, _Mapping]] = ..., surrounding_context: _Optional[str] = ..., repository_id: _Optional[str] = ...) -> None: ...

class AnalyzeSymbolResponse(_message.Message):
    __slots__ = ("summary", "purpose", "concerns", "suggestions", "usage")
    SUMMARY_FIELD_NUMBER: _ClassVar[int]
    PURPOSE_FIELD_NUMBER: _ClassVar[int]
    CONCERNS_FIELD_NUMBER: _ClassVar[int]
    SUGGESTIONS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    summary: str
    purpose: str
    concerns: _containers.RepeatedScalarFieldContainer[str]
    suggestions: _containers.RepeatedScalarFieldContainer[str]
    usage: _types_pb2.LLMUsage
    def __init__(self, summary: _Optional[str] = ..., purpose: _Optional[str] = ..., concerns: _Optional[_Iterable[str]] = ..., suggestions: _Optional[_Iterable[str]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class ExplainRelationshipRequest(_message.Message):
    __slots__ = ("source", "target", "relationship_type")
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    TARGET_FIELD_NUMBER: _ClassVar[int]
    RELATIONSHIP_TYPE_FIELD_NUMBER: _ClassVar[int]
    source: _types_pb2.CodeSymbol
    target: _types_pb2.CodeSymbol
    relationship_type: str
    def __init__(self, source: _Optional[_Union[_types_pb2.CodeSymbol, _Mapping]] = ..., target: _Optional[_Union[_types_pb2.CodeSymbol, _Mapping]] = ..., relationship_type: _Optional[str] = ...) -> None: ...

class ExplainRelationshipResponse(_message.Message):
    __slots__ = ("explanation", "confidence", "usage")
    EXPLANATION_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    explanation: str
    confidence: _types_pb2.Confidence
    usage: _types_pb2.LLMUsage
    def __init__(self, explanation: _Optional[str] = ..., confidence: _Optional[_Union[_types_pb2.Confidence, str]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class AnswerQuestionRequest(_message.Message):
    __slots__ = ("question", "repository_id", "context_symbols", "max_tokens", "context_code", "file_path", "language")
    QUESTION_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    CONTEXT_SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    MAX_TOKENS_FIELD_NUMBER: _ClassVar[int]
    CONTEXT_CODE_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    question: str
    repository_id: str
    context_symbols: _containers.RepeatedCompositeFieldContainer[_types_pb2.CodeSymbol]
    max_tokens: int
    context_code: str
    file_path: str
    language: _types_pb2.Language
    def __init__(self, question: _Optional[str] = ..., repository_id: _Optional[str] = ..., context_symbols: _Optional[_Iterable[_Union[_types_pb2.CodeSymbol, _Mapping]]] = ..., max_tokens: _Optional[int] = ..., context_code: _Optional[str] = ..., file_path: _Optional[str] = ..., language: _Optional[_Union[_types_pb2.Language, str]] = ...) -> None: ...

class AnswerQuestionResponse(_message.Message):
    __slots__ = ("answer", "referenced_symbols", "usage")
    ANSWER_FIELD_NUMBER: _ClassVar[int]
    REFERENCED_SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    answer: str
    referenced_symbols: _containers.RepeatedCompositeFieldContainer[_types_pb2.CodeSymbol]
    usage: _types_pb2.LLMUsage
    def __init__(self, answer: _Optional[str] = ..., referenced_symbols: _Optional[_Iterable[_Union[_types_pb2.CodeSymbol, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class ReviewFileRequest(_message.Message):
    __slots__ = ("repository_id", "file_path", "language", "content", "template")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    TEMPLATE_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    file_path: str
    language: _types_pb2.Language
    content: str
    template: str
    def __init__(self, repository_id: _Optional[str] = ..., file_path: _Optional[str] = ..., language: _Optional[_Union[_types_pb2.Language, str]] = ..., content: _Optional[str] = ..., template: _Optional[str] = ...) -> None: ...

class ReviewFileResponse(_message.Message):
    __slots__ = ("template", "findings", "score", "usage")
    TEMPLATE_FIELD_NUMBER: _ClassVar[int]
    FINDINGS_FIELD_NUMBER: _ClassVar[int]
    SCORE_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    template: str
    findings: _containers.RepeatedCompositeFieldContainer[ReviewFinding]
    score: float
    usage: _types_pb2.LLMUsage
    def __init__(self, template: _Optional[str] = ..., findings: _Optional[_Iterable[_Union[ReviewFinding, _Mapping]]] = ..., score: _Optional[float] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class ReviewFinding(_message.Message):
    __slots__ = ("category", "severity", "message", "file_path", "start_line", "end_line", "suggestion")
    CATEGORY_FIELD_NUMBER: _ClassVar[int]
    SEVERITY_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    START_LINE_FIELD_NUMBER: _ClassVar[int]
    END_LINE_FIELD_NUMBER: _ClassVar[int]
    SUGGESTION_FIELD_NUMBER: _ClassVar[int]
    category: str
    severity: str
    message: str
    file_path: str
    start_line: int
    end_line: int
    suggestion: str
    def __init__(self, category: _Optional[str] = ..., severity: _Optional[str] = ..., message: _Optional[str] = ..., file_path: _Optional[str] = ..., start_line: _Optional[int] = ..., end_line: _Optional[int] = ..., suggestion: _Optional[str] = ...) -> None: ...

class GenerateEmbeddingRequest(_message.Message):
    __slots__ = ("text", "model")
    TEXT_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    text: str
    model: str
    def __init__(self, text: _Optional[str] = ..., model: _Optional[str] = ...) -> None: ...

class GenerateEmbeddingResponse(_message.Message):
    __slots__ = ("embedding", "usage")
    EMBEDDING_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    embedding: _types_pb2.Embedding
    usage: _types_pb2.LLMUsage
    def __init__(self, embedding: _Optional[_Union[_types_pb2.Embedding, _Mapping]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...
