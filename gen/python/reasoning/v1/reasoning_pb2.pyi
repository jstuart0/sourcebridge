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

class AnswerDelta(_message.Message):
    __slots__ = ("content_delta", "finished", "referenced_symbols", "usage", "progress")
    CONTENT_DELTA_FIELD_NUMBER: _ClassVar[int]
    FINISHED_FIELD_NUMBER: _ClassVar[int]
    REFERENCED_SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    PROGRESS_FIELD_NUMBER: _ClassVar[int]
    content_delta: str
    finished: bool
    referenced_symbols: _containers.RepeatedCompositeFieldContainer[_types_pb2.CodeSymbol]
    usage: _types_pb2.LLMUsage
    progress: ProgressEvent
    def __init__(self, content_delta: _Optional[str] = ..., finished: bool = ..., referenced_symbols: _Optional[_Iterable[_Union[_types_pb2.CodeSymbol, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ..., progress: _Optional[_Union[ProgressEvent, _Mapping]] = ...) -> None: ...

class ProgressEvent(_message.Message):
    __slots__ = ("phase", "detail", "tool_name", "elapsed_ms")
    PHASE_FIELD_NUMBER: _ClassVar[int]
    DETAIL_FIELD_NUMBER: _ClassVar[int]
    TOOL_NAME_FIELD_NUMBER: _ClassVar[int]
    ELAPSED_MS_FIELD_NUMBER: _ClassVar[int]
    phase: str
    detail: str
    tool_name: str
    elapsed_ms: int
    def __init__(self, phase: _Optional[str] = ..., detail: _Optional[str] = ..., tool_name: _Optional[str] = ..., elapsed_ms: _Optional[int] = ...) -> None: ...

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

class SimulateChangeRequest(_message.Message):
    __slots__ = ("repository_id", "description", "anchor_file", "anchor_symbol", "symbols", "top_n", "confidence_threshold")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    ANCHOR_FILE_FIELD_NUMBER: _ClassVar[int]
    ANCHOR_SYMBOL_FIELD_NUMBER: _ClassVar[int]
    SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    TOP_N_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_THRESHOLD_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    description: str
    anchor_file: str
    anchor_symbol: str
    symbols: _containers.RepeatedCompositeFieldContainer[_types_pb2.CodeSymbol]
    top_n: int
    confidence_threshold: float
    def __init__(self, repository_id: _Optional[str] = ..., description: _Optional[str] = ..., anchor_file: _Optional[str] = ..., anchor_symbol: _Optional[str] = ..., symbols: _Optional[_Iterable[_Union[_types_pb2.CodeSymbol, _Mapping]]] = ..., top_n: _Optional[int] = ..., confidence_threshold: _Optional[float] = ...) -> None: ...

class SimulatedSymbolMatch(_message.Message):
    __slots__ = ("symbol_id", "name", "qualified_name", "kind", "file_path", "similarity", "is_anchor")
    SYMBOL_ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    QUALIFIED_NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    SIMILARITY_FIELD_NUMBER: _ClassVar[int]
    IS_ANCHOR_FIELD_NUMBER: _ClassVar[int]
    symbol_id: str
    name: str
    qualified_name: str
    kind: str
    file_path: str
    similarity: float
    is_anchor: bool
    def __init__(self, symbol_id: _Optional[str] = ..., name: _Optional[str] = ..., qualified_name: _Optional[str] = ..., kind: _Optional[str] = ..., file_path: _Optional[str] = ..., similarity: _Optional[float] = ..., is_anchor: bool = ...) -> None: ...

class SimulateChangeResponse(_message.Message):
    __slots__ = ("resolved_symbols", "description_embedding_model", "symbols_evaluated", "usage")
    RESOLVED_SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_EMBEDDING_MODEL_FIELD_NUMBER: _ClassVar[int]
    SYMBOLS_EVALUATED_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    resolved_symbols: _containers.RepeatedCompositeFieldContainer[SimulatedSymbolMatch]
    description_embedding_model: str
    symbols_evaluated: int
    usage: _types_pb2.LLMUsage
    def __init__(self, resolved_symbols: _Optional[_Iterable[_Union[SimulatedSymbolMatch, _Mapping]]] = ..., description_embedding_model: _Optional[str] = ..., symbols_evaluated: _Optional[int] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class ToolSchema(_message.Message):
    __slots__ = ("name", "description", "input_schema_json")
    NAME_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    INPUT_SCHEMA_JSON_FIELD_NUMBER: _ClassVar[int]
    name: str
    description: str
    input_schema_json: str
    def __init__(self, name: _Optional[str] = ..., description: _Optional[str] = ..., input_schema_json: _Optional[str] = ...) -> None: ...

class AgentMessage(_message.Message):
    __slots__ = ("role", "text", "tool_calls", "tool_results")
    ROLE_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    TOOL_CALLS_FIELD_NUMBER: _ClassVar[int]
    TOOL_RESULTS_FIELD_NUMBER: _ClassVar[int]
    role: str
    text: str
    tool_calls: _containers.RepeatedCompositeFieldContainer[ToolCall]
    tool_results: _containers.RepeatedCompositeFieldContainer[ToolResult]
    def __init__(self, role: _Optional[str] = ..., text: _Optional[str] = ..., tool_calls: _Optional[_Iterable[_Union[ToolCall, _Mapping]]] = ..., tool_results: _Optional[_Iterable[_Union[ToolResult, _Mapping]]] = ...) -> None: ...

class ToolCall(_message.Message):
    __slots__ = ("call_id", "name", "args_json")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    ARGS_JSON_FIELD_NUMBER: _ClassVar[int]
    call_id: str
    name: str
    args_json: str
    def __init__(self, call_id: _Optional[str] = ..., name: _Optional[str] = ..., args_json: _Optional[str] = ...) -> None: ...

class ToolResult(_message.Message):
    __slots__ = ("call_id", "ok", "data_json", "error", "hint")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    OK_FIELD_NUMBER: _ClassVar[int]
    DATA_JSON_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    HINT_FIELD_NUMBER: _ClassVar[int]
    call_id: str
    ok: bool
    data_json: str
    error: str
    hint: str
    def __init__(self, call_id: _Optional[str] = ..., ok: bool = ..., data_json: _Optional[str] = ..., error: _Optional[str] = ..., hint: _Optional[str] = ...) -> None: ...

class AnswerQuestionWithToolsRequest(_message.Message):
    __slots__ = ("repository_id", "messages", "tools", "max_tokens", "enable_prompt_caching")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    MESSAGES_FIELD_NUMBER: _ClassVar[int]
    TOOLS_FIELD_NUMBER: _ClassVar[int]
    MAX_TOKENS_FIELD_NUMBER: _ClassVar[int]
    ENABLE_PROMPT_CACHING_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    messages: _containers.RepeatedCompositeFieldContainer[AgentMessage]
    tools: _containers.RepeatedCompositeFieldContainer[ToolSchema]
    max_tokens: int
    enable_prompt_caching: bool
    def __init__(self, repository_id: _Optional[str] = ..., messages: _Optional[_Iterable[_Union[AgentMessage, _Mapping]]] = ..., tools: _Optional[_Iterable[_Union[ToolSchema, _Mapping]]] = ..., max_tokens: _Optional[int] = ..., enable_prompt_caching: bool = ...) -> None: ...

class AnswerQuestionWithToolsResponse(_message.Message):
    __slots__ = ("capability_supported", "turn", "usage", "termination_hint", "cache_creation_input_tokens", "cache_read_input_tokens")
    CAPABILITY_SUPPORTED_FIELD_NUMBER: _ClassVar[int]
    TURN_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    TERMINATION_HINT_FIELD_NUMBER: _ClassVar[int]
    CACHE_CREATION_INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    CACHE_READ_INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    capability_supported: bool
    turn: AgentMessage
    usage: _types_pb2.LLMUsage
    termination_hint: str
    cache_creation_input_tokens: int
    cache_read_input_tokens: int
    def __init__(self, capability_supported: bool = ..., turn: _Optional[_Union[AgentMessage, _Mapping]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ..., termination_hint: _Optional[str] = ..., cache_creation_input_tokens: _Optional[int] = ..., cache_read_input_tokens: _Optional[int] = ...) -> None: ...

class ClassifyQuestionRequest(_message.Message):
    __slots__ = ("repository_id", "question", "file_path", "pinned_code")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    QUESTION_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    PINNED_CODE_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    question: str
    file_path: str
    pinned_code: str
    def __init__(self, repository_id: _Optional[str] = ..., question: _Optional[str] = ..., file_path: _Optional[str] = ..., pinned_code: _Optional[str] = ...) -> None: ...

class ClassifyQuestionResponse(_message.Message):
    __slots__ = ("capability_supported", "question_class", "needs_call_graph", "needs_requirements", "needs_tests", "needs_summaries", "symbol_candidates", "file_candidates", "topic_terms", "usage")
    CAPABILITY_SUPPORTED_FIELD_NUMBER: _ClassVar[int]
    QUESTION_CLASS_FIELD_NUMBER: _ClassVar[int]
    NEEDS_CALL_GRAPH_FIELD_NUMBER: _ClassVar[int]
    NEEDS_REQUIREMENTS_FIELD_NUMBER: _ClassVar[int]
    NEEDS_TESTS_FIELD_NUMBER: _ClassVar[int]
    NEEDS_SUMMARIES_FIELD_NUMBER: _ClassVar[int]
    SYMBOL_CANDIDATES_FIELD_NUMBER: _ClassVar[int]
    FILE_CANDIDATES_FIELD_NUMBER: _ClassVar[int]
    TOPIC_TERMS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    capability_supported: bool
    question_class: str
    needs_call_graph: bool
    needs_requirements: bool
    needs_tests: bool
    needs_summaries: bool
    symbol_candidates: _containers.RepeatedScalarFieldContainer[str]
    file_candidates: _containers.RepeatedScalarFieldContainer[str]
    topic_terms: _containers.RepeatedScalarFieldContainer[str]
    usage: _types_pb2.LLMUsage
    def __init__(self, capability_supported: bool = ..., question_class: _Optional[str] = ..., needs_call_graph: bool = ..., needs_requirements: bool = ..., needs_tests: bool = ..., needs_summaries: bool = ..., symbol_candidates: _Optional[_Iterable[str]] = ..., file_candidates: _Optional[_Iterable[str]] = ..., topic_terms: _Optional[_Iterable[str]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class DecomposeQuestionRequest(_message.Message):
    __slots__ = ("repository_id", "question", "question_class", "max_sub_questions")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    QUESTION_FIELD_NUMBER: _ClassVar[int]
    QUESTION_CLASS_FIELD_NUMBER: _ClassVar[int]
    MAX_SUB_QUESTIONS_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    question: str
    question_class: str
    max_sub_questions: int
    def __init__(self, repository_id: _Optional[str] = ..., question: _Optional[str] = ..., question_class: _Optional[str] = ..., max_sub_questions: _Optional[int] = ...) -> None: ...

class DecomposeQuestionResponse(_message.Message):
    __slots__ = ("capability_supported", "sub_questions", "usage")
    CAPABILITY_SUPPORTED_FIELD_NUMBER: _ClassVar[int]
    SUB_QUESTIONS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    capability_supported: bool
    sub_questions: _containers.RepeatedScalarFieldContainer[str]
    usage: _types_pb2.LLMUsage
    def __init__(self, capability_supported: bool = ..., sub_questions: _Optional[_Iterable[str]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class SynthesizeDecomposedAnswerRequest(_message.Message):
    __slots__ = ("repository_id", "original_question", "sub_answers", "enable_prompt_caching")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    ORIGINAL_QUESTION_FIELD_NUMBER: _ClassVar[int]
    SUB_ANSWERS_FIELD_NUMBER: _ClassVar[int]
    ENABLE_PROMPT_CACHING_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    original_question: str
    sub_answers: _containers.RepeatedCompositeFieldContainer[DecomposedSubAnswer]
    enable_prompt_caching: bool
    def __init__(self, repository_id: _Optional[str] = ..., original_question: _Optional[str] = ..., sub_answers: _Optional[_Iterable[_Union[DecomposedSubAnswer, _Mapping]]] = ..., enable_prompt_caching: bool = ...) -> None: ...

class DecomposedSubAnswer(_message.Message):
    __slots__ = ("sub_question", "sub_answer", "reference_handles", "termination_reason", "tool_calls_count")
    SUB_QUESTION_FIELD_NUMBER: _ClassVar[int]
    SUB_ANSWER_FIELD_NUMBER: _ClassVar[int]
    REFERENCE_HANDLES_FIELD_NUMBER: _ClassVar[int]
    TERMINATION_REASON_FIELD_NUMBER: _ClassVar[int]
    TOOL_CALLS_COUNT_FIELD_NUMBER: _ClassVar[int]
    sub_question: str
    sub_answer: str
    reference_handles: _containers.RepeatedScalarFieldContainer[str]
    termination_reason: str
    tool_calls_count: int
    def __init__(self, sub_question: _Optional[str] = ..., sub_answer: _Optional[str] = ..., reference_handles: _Optional[_Iterable[str]] = ..., termination_reason: _Optional[str] = ..., tool_calls_count: _Optional[int] = ...) -> None: ...

class SynthesizeDecomposedAnswerResponse(_message.Message):
    __slots__ = ("answer", "usage", "cache_creation_input_tokens", "cache_read_input_tokens")
    ANSWER_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    CACHE_CREATION_INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    CACHE_READ_INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    answer: str
    usage: _types_pb2.LLMUsage
    cache_creation_input_tokens: int
    cache_read_input_tokens: int
    def __init__(self, answer: _Optional[str] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ..., cache_creation_input_tokens: _Optional[int] = ..., cache_read_input_tokens: _Optional[int] = ...) -> None: ...

class GetProviderCapabilitiesRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetProviderCapabilitiesResponse(_message.Message):
    __slots__ = ("provider", "model", "tool_use_supported", "prompt_caching_supported")
    PROVIDER_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    TOOL_USE_SUPPORTED_FIELD_NUMBER: _ClassVar[int]
    PROMPT_CACHING_SUPPORTED_FIELD_NUMBER: _ClassVar[int]
    provider: str
    model: str
    tool_use_supported: bool
    prompt_caching_supported: bool
    def __init__(self, provider: _Optional[str] = ..., model: _Optional[str] = ..., tool_use_supported: bool = ..., prompt_caching_supported: bool = ...) -> None: ...
