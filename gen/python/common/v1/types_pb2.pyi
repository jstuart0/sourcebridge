import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Language(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    LANGUAGE_UNSPECIFIED: _ClassVar[Language]
    LANGUAGE_GO: _ClassVar[Language]
    LANGUAGE_PYTHON: _ClassVar[Language]
    LANGUAGE_TYPESCRIPT: _ClassVar[Language]
    LANGUAGE_JAVASCRIPT: _ClassVar[Language]
    LANGUAGE_JAVA: _ClassVar[Language]
    LANGUAGE_RUST: _ClassVar[Language]
    LANGUAGE_CSHARP: _ClassVar[Language]
    LANGUAGE_CPP: _ClassVar[Language]
    LANGUAGE_RUBY: _ClassVar[Language]
    LANGUAGE_PHP: _ClassVar[Language]

class SymbolKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SYMBOL_KIND_UNSPECIFIED: _ClassVar[SymbolKind]
    SYMBOL_KIND_FUNCTION: _ClassVar[SymbolKind]
    SYMBOL_KIND_METHOD: _ClassVar[SymbolKind]
    SYMBOL_KIND_CLASS: _ClassVar[SymbolKind]
    SYMBOL_KIND_STRUCT: _ClassVar[SymbolKind]
    SYMBOL_KIND_INTERFACE: _ClassVar[SymbolKind]
    SYMBOL_KIND_ENUM: _ClassVar[SymbolKind]
    SYMBOL_KIND_CONSTANT: _ClassVar[SymbolKind]
    SYMBOL_KIND_VARIABLE: _ClassVar[SymbolKind]
    SYMBOL_KIND_MODULE: _ClassVar[SymbolKind]
    SYMBOL_KIND_PACKAGE: _ClassVar[SymbolKind]
    SYMBOL_KIND_TYPE: _ClassVar[SymbolKind]
    SYMBOL_KIND_TEST: _ClassVar[SymbolKind]

class Confidence(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CONFIDENCE_UNSPECIFIED: _ClassVar[Confidence]
    CONFIDENCE_LOW: _ClassVar[Confidence]
    CONFIDENCE_MEDIUM: _ClassVar[Confidence]
    CONFIDENCE_HIGH: _ClassVar[Confidence]
    CONFIDENCE_VERIFIED: _ClassVar[Confidence]
LANGUAGE_UNSPECIFIED: Language
LANGUAGE_GO: Language
LANGUAGE_PYTHON: Language
LANGUAGE_TYPESCRIPT: Language
LANGUAGE_JAVASCRIPT: Language
LANGUAGE_JAVA: Language
LANGUAGE_RUST: Language
LANGUAGE_CSHARP: Language
LANGUAGE_CPP: Language
LANGUAGE_RUBY: Language
LANGUAGE_PHP: Language
SYMBOL_KIND_UNSPECIFIED: SymbolKind
SYMBOL_KIND_FUNCTION: SymbolKind
SYMBOL_KIND_METHOD: SymbolKind
SYMBOL_KIND_CLASS: SymbolKind
SYMBOL_KIND_STRUCT: SymbolKind
SYMBOL_KIND_INTERFACE: SymbolKind
SYMBOL_KIND_ENUM: SymbolKind
SYMBOL_KIND_CONSTANT: SymbolKind
SYMBOL_KIND_VARIABLE: SymbolKind
SYMBOL_KIND_MODULE: SymbolKind
SYMBOL_KIND_PACKAGE: SymbolKind
SYMBOL_KIND_TYPE: SymbolKind
SYMBOL_KIND_TEST: SymbolKind
CONFIDENCE_UNSPECIFIED: Confidence
CONFIDENCE_LOW: Confidence
CONFIDENCE_MEDIUM: Confidence
CONFIDENCE_HIGH: Confidence
CONFIDENCE_VERIFIED: Confidence

class FileLocation(_message.Message):
    __slots__ = ("path", "start_line", "end_line", "start_column", "end_column")
    PATH_FIELD_NUMBER: _ClassVar[int]
    START_LINE_FIELD_NUMBER: _ClassVar[int]
    END_LINE_FIELD_NUMBER: _ClassVar[int]
    START_COLUMN_FIELD_NUMBER: _ClassVar[int]
    END_COLUMN_FIELD_NUMBER: _ClassVar[int]
    path: str
    start_line: int
    end_line: int
    start_column: int
    end_column: int
    def __init__(self, path: _Optional[str] = ..., start_line: _Optional[int] = ..., end_line: _Optional[int] = ..., start_column: _Optional[int] = ..., end_column: _Optional[int] = ...) -> None: ...

class CodeSymbol(_message.Message):
    __slots__ = ("id", "name", "qualified_name", "kind", "language", "location", "signature", "doc_comment", "annotations")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    QUALIFIED_NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    DOC_COMMENT_FIELD_NUMBER: _ClassVar[int]
    ANNOTATIONS_FIELD_NUMBER: _ClassVar[int]
    id: str
    name: str
    qualified_name: str
    kind: SymbolKind
    language: Language
    location: FileLocation
    signature: str
    doc_comment: str
    annotations: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., qualified_name: _Optional[str] = ..., kind: _Optional[_Union[SymbolKind, str]] = ..., language: _Optional[_Union[Language, str]] = ..., location: _Optional[_Union[FileLocation, _Mapping]] = ..., signature: _Optional[str] = ..., doc_comment: _Optional[str] = ..., annotations: _Optional[_Iterable[str]] = ...) -> None: ...

class Requirement(_message.Message):
    __slots__ = ("id", "external_id", "title", "description", "source", "priority", "tags", "created_at", "updated_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    EXTERNAL_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    PRIORITY_FIELD_NUMBER: _ClassVar[int]
    TAGS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    external_id: str
    title: str
    description: str
    source: str
    priority: str
    tags: _containers.RepeatedScalarFieldContainer[str]
    created_at: _timestamp_pb2.Timestamp
    updated_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., external_id: _Optional[str] = ..., title: _Optional[str] = ..., description: _Optional[str] = ..., source: _Optional[str] = ..., priority: _Optional[str] = ..., tags: _Optional[_Iterable[str]] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class RequirementLink(_message.Message):
    __slots__ = ("id", "requirement_id", "symbol_id", "confidence", "rationale", "verified", "verified_by", "created_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    REQUIREMENT_ID_FIELD_NUMBER: _ClassVar[int]
    SYMBOL_ID_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    RATIONALE_FIELD_NUMBER: _ClassVar[int]
    VERIFIED_FIELD_NUMBER: _ClassVar[int]
    VERIFIED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    requirement_id: str
    symbol_id: str
    confidence: Confidence
    rationale: str
    verified: bool
    verified_by: str
    created_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., requirement_id: _Optional[str] = ..., symbol_id: _Optional[str] = ..., confidence: _Optional[_Union[Confidence, str]] = ..., rationale: _Optional[str] = ..., verified: bool = ..., verified_by: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class Embedding(_message.Message):
    __slots__ = ("id", "source_id", "source_type", "vector", "model", "dimensions")
    ID_FIELD_NUMBER: _ClassVar[int]
    SOURCE_ID_FIELD_NUMBER: _ClassVar[int]
    SOURCE_TYPE_FIELD_NUMBER: _ClassVar[int]
    VECTOR_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    DIMENSIONS_FIELD_NUMBER: _ClassVar[int]
    id: str
    source_id: str
    source_type: str
    vector: _containers.RepeatedScalarFieldContainer[float]
    model: str
    dimensions: int
    def __init__(self, id: _Optional[str] = ..., source_id: _Optional[str] = ..., source_type: _Optional[str] = ..., vector: _Optional[_Iterable[float]] = ..., model: _Optional[str] = ..., dimensions: _Optional[int] = ...) -> None: ...

class LLMUsage(_message.Message):
    __slots__ = ("model", "input_tokens", "output_tokens", "latency_ms", "operation")
    MODEL_FIELD_NUMBER: _ClassVar[int]
    INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    OUTPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    LATENCY_MS_FIELD_NUMBER: _ClassVar[int]
    OPERATION_FIELD_NUMBER: _ClassVar[int]
    model: str
    input_tokens: int
    output_tokens: int
    latency_ms: float
    operation: str
    def __init__(self, model: _Optional[str] = ..., input_tokens: _Optional[int] = ..., output_tokens: _Optional[int] = ..., latency_ms: _Optional[float] = ..., operation: _Optional[str] = ...) -> None: ...
