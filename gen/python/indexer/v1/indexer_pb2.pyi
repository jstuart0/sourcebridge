from common.v1 import types_pb2 as _types_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class RelationType(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    RELATION_TYPE_UNSPECIFIED: _ClassVar[RelationType]
    RELATION_TYPE_CALLS: _ClassVar[RelationType]
    RELATION_TYPE_IMPORTS: _ClassVar[RelationType]
    RELATION_TYPE_EXTENDS: _ClassVar[RelationType]
    RELATION_TYPE_IMPLEMENTS: _ClassVar[RelationType]
    RELATION_TYPE_CONTAINS: _ClassVar[RelationType]
    RELATION_TYPE_USES: _ClassVar[RelationType]
    RELATION_TYPE_TESTS: _ClassVar[RelationType]
RELATION_TYPE_UNSPECIFIED: RelationType
RELATION_TYPE_CALLS: RelationType
RELATION_TYPE_IMPORTS: RelationType
RELATION_TYPE_EXTENDS: RelationType
RELATION_TYPE_IMPLEMENTS: RelationType
RELATION_TYPE_CONTAINS: RelationType
RELATION_TYPE_USES: RelationType
RELATION_TYPE_TESTS: RelationType

class IndexRepositoryRequest(_message.Message):
    __slots__ = ("repository_id", "root_path", "include_patterns", "exclude_patterns", "incremental", "commit_hash")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    ROOT_PATH_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_PATTERNS_FIELD_NUMBER: _ClassVar[int]
    EXCLUDE_PATTERNS_FIELD_NUMBER: _ClassVar[int]
    INCREMENTAL_FIELD_NUMBER: _ClassVar[int]
    COMMIT_HASH_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    root_path: str
    include_patterns: _containers.RepeatedScalarFieldContainer[str]
    exclude_patterns: _containers.RepeatedScalarFieldContainer[str]
    incremental: bool
    commit_hash: str
    def __init__(self, repository_id: _Optional[str] = ..., root_path: _Optional[str] = ..., include_patterns: _Optional[_Iterable[str]] = ..., exclude_patterns: _Optional[_Iterable[str]] = ..., incremental: bool = ..., commit_hash: _Optional[str] = ...) -> None: ...

class IndexRepositoryResponse(_message.Message):
    __slots__ = ("files_processed", "symbols_found", "relations_found", "errors", "duration_ms")
    FILES_PROCESSED_FIELD_NUMBER: _ClassVar[int]
    SYMBOLS_FOUND_FIELD_NUMBER: _ClassVar[int]
    RELATIONS_FOUND_FIELD_NUMBER: _ClassVar[int]
    ERRORS_FIELD_NUMBER: _ClassVar[int]
    DURATION_MS_FIELD_NUMBER: _ClassVar[int]
    files_processed: int
    symbols_found: int
    relations_found: int
    errors: _containers.RepeatedScalarFieldContainer[str]
    duration_ms: float
    def __init__(self, files_processed: _Optional[int] = ..., symbols_found: _Optional[int] = ..., relations_found: _Optional[int] = ..., errors: _Optional[_Iterable[str]] = ..., duration_ms: _Optional[float] = ...) -> None: ...

class IndexFileRequest(_message.Message):
    __slots__ = ("repository_id", "file_path", "content", "language")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    file_path: str
    content: str
    language: _types_pb2.Language
    def __init__(self, repository_id: _Optional[str] = ..., file_path: _Optional[str] = ..., content: _Optional[str] = ..., language: _Optional[_Union[_types_pb2.Language, str]] = ...) -> None: ...

class IndexFileResponse(_message.Message):
    __slots__ = ("symbols", "relations", "errors")
    SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    RELATIONS_FIELD_NUMBER: _ClassVar[int]
    ERRORS_FIELD_NUMBER: _ClassVar[int]
    symbols: _containers.RepeatedCompositeFieldContainer[_types_pb2.CodeSymbol]
    relations: _containers.RepeatedCompositeFieldContainer[SymbolRelation]
    errors: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, symbols: _Optional[_Iterable[_Union[_types_pb2.CodeSymbol, _Mapping]]] = ..., relations: _Optional[_Iterable[_Union[SymbolRelation, _Mapping]]] = ..., errors: _Optional[_Iterable[str]] = ...) -> None: ...

class GetSymbolsRequest(_message.Message):
    __slots__ = ("repository_id", "query", "kind_filter", "language_filter", "limit", "offset")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    QUERY_FIELD_NUMBER: _ClassVar[int]
    KIND_FILTER_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FILTER_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    OFFSET_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    query: str
    kind_filter: _types_pb2.SymbolKind
    language_filter: _types_pb2.Language
    limit: int
    offset: int
    def __init__(self, repository_id: _Optional[str] = ..., query: _Optional[str] = ..., kind_filter: _Optional[_Union[_types_pb2.SymbolKind, str]] = ..., language_filter: _Optional[_Union[_types_pb2.Language, str]] = ..., limit: _Optional[int] = ..., offset: _Optional[int] = ...) -> None: ...

class GetSymbolsResponse(_message.Message):
    __slots__ = ("symbols", "total_count")
    SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_COUNT_FIELD_NUMBER: _ClassVar[int]
    symbols: _containers.RepeatedCompositeFieldContainer[_types_pb2.CodeSymbol]
    total_count: int
    def __init__(self, symbols: _Optional[_Iterable[_Union[_types_pb2.CodeSymbol, _Mapping]]] = ..., total_count: _Optional[int] = ...) -> None: ...

class SymbolRelation(_message.Message):
    __slots__ = ("source_id", "target_id", "type")
    SOURCE_ID_FIELD_NUMBER: _ClassVar[int]
    TARGET_ID_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    source_id: str
    target_id: str
    type: RelationType
    def __init__(self, source_id: _Optional[str] = ..., target_id: _Optional[str] = ..., type: _Optional[_Union[RelationType, str]] = ...) -> None: ...
