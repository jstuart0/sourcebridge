from common.v1 import types_pb2 as _types_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ParseDocumentRequest(_message.Message):
    __slots__ = ("content", "format", "source_path")
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    FORMAT_FIELD_NUMBER: _ClassVar[int]
    SOURCE_PATH_FIELD_NUMBER: _ClassVar[int]
    content: str
    format: str
    source_path: str
    def __init__(self, content: _Optional[str] = ..., format: _Optional[str] = ..., source_path: _Optional[str] = ...) -> None: ...

class ParseDocumentResponse(_message.Message):
    __slots__ = ("requirements", "total_found", "warnings")
    REQUIREMENTS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_FOUND_FIELD_NUMBER: _ClassVar[int]
    WARNINGS_FIELD_NUMBER: _ClassVar[int]
    requirements: _containers.RepeatedCompositeFieldContainer[_types_pb2.Requirement]
    total_found: int
    warnings: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, requirements: _Optional[_Iterable[_Union[_types_pb2.Requirement, _Mapping]]] = ..., total_found: _Optional[int] = ..., warnings: _Optional[_Iterable[str]] = ...) -> None: ...

class ParseCSVRequest(_message.Message):
    __slots__ = ("content", "column_mapping", "source_path")
    class ColumnMappingEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    COLUMN_MAPPING_FIELD_NUMBER: _ClassVar[int]
    SOURCE_PATH_FIELD_NUMBER: _ClassVar[int]
    content: str
    column_mapping: _containers.ScalarMap[str, str]
    source_path: str
    def __init__(self, content: _Optional[str] = ..., column_mapping: _Optional[_Mapping[str, str]] = ..., source_path: _Optional[str] = ...) -> None: ...

class ParseCSVResponse(_message.Message):
    __slots__ = ("requirements", "total_found", "skipped", "warnings")
    REQUIREMENTS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_FOUND_FIELD_NUMBER: _ClassVar[int]
    SKIPPED_FIELD_NUMBER: _ClassVar[int]
    WARNINGS_FIELD_NUMBER: _ClassVar[int]
    requirements: _containers.RepeatedCompositeFieldContainer[_types_pb2.Requirement]
    total_found: int
    skipped: int
    warnings: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, requirements: _Optional[_Iterable[_Union[_types_pb2.Requirement, _Mapping]]] = ..., total_found: _Optional[int] = ..., skipped: _Optional[int] = ..., warnings: _Optional[_Iterable[str]] = ...) -> None: ...

class EnrichRequirementRequest(_message.Message):
    __slots__ = ("requirement",)
    REQUIREMENT_FIELD_NUMBER: _ClassVar[int]
    requirement: _types_pb2.Requirement
    def __init__(self, requirement: _Optional[_Union[_types_pb2.Requirement, _Mapping]] = ...) -> None: ...

class EnrichRequirementResponse(_message.Message):
    __slots__ = ("enriched", "suggested_tags", "suggested_priority", "usage")
    ENRICHED_FIELD_NUMBER: _ClassVar[int]
    SUGGESTED_TAGS_FIELD_NUMBER: _ClassVar[int]
    SUGGESTED_PRIORITY_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    enriched: _types_pb2.Requirement
    suggested_tags: _containers.RepeatedScalarFieldContainer[str]
    suggested_priority: str
    usage: _types_pb2.LLMUsage
    def __init__(self, enriched: _Optional[_Union[_types_pb2.Requirement, _Mapping]] = ..., suggested_tags: _Optional[_Iterable[str]] = ..., suggested_priority: _Optional[str] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class FileEntry(_message.Message):
    __slots__ = ("path", "content", "language", "line_count")
    PATH_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    LINE_COUNT_FIELD_NUMBER: _ClassVar[int]
    path: str
    content: str
    language: str
    line_count: int
    def __init__(self, path: _Optional[str] = ..., content: _Optional[str] = ..., language: _Optional[str] = ..., line_count: _Optional[int] = ...) -> None: ...

class ExtractSpecsRequest(_message.Message):
    __slots__ = ("repository_id", "repository_name", "files", "skip_llm_refinement")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_NAME_FIELD_NUMBER: _ClassVar[int]
    FILES_FIELD_NUMBER: _ClassVar[int]
    SKIP_LLM_REFINEMENT_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    repository_name: str
    files: _containers.RepeatedCompositeFieldContainer[FileEntry]
    skip_llm_refinement: bool
    def __init__(self, repository_id: _Optional[str] = ..., repository_name: _Optional[str] = ..., files: _Optional[_Iterable[_Union[FileEntry, _Mapping]]] = ..., skip_llm_refinement: bool = ...) -> None: ...

class DiscoveredSpec(_message.Message):
    __slots__ = ("source", "source_file", "source_line", "source_files", "text", "raw_text", "group_key", "language", "keywords", "confidence", "llm_refined")
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FILE_FIELD_NUMBER: _ClassVar[int]
    SOURCE_LINE_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FILES_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    RAW_TEXT_FIELD_NUMBER: _ClassVar[int]
    GROUP_KEY_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    KEYWORDS_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    LLM_REFINED_FIELD_NUMBER: _ClassVar[int]
    source: str
    source_file: str
    source_line: int
    source_files: _containers.RepeatedScalarFieldContainer[str]
    text: str
    raw_text: str
    group_key: str
    language: str
    keywords: _containers.RepeatedScalarFieldContainer[str]
    confidence: str
    llm_refined: bool
    def __init__(self, source: _Optional[str] = ..., source_file: _Optional[str] = ..., source_line: _Optional[int] = ..., source_files: _Optional[_Iterable[str]] = ..., text: _Optional[str] = ..., raw_text: _Optional[str] = ..., group_key: _Optional[str] = ..., language: _Optional[str] = ..., keywords: _Optional[_Iterable[str]] = ..., confidence: _Optional[str] = ..., llm_refined: bool = ...) -> None: ...

class ExtractSpecsResponse(_message.Message):
    __slots__ = ("specs", "total_candidates", "total_refined", "usage", "warnings")
    SPECS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_CANDIDATES_FIELD_NUMBER: _ClassVar[int]
    TOTAL_REFINED_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    WARNINGS_FIELD_NUMBER: _ClassVar[int]
    specs: _containers.RepeatedCompositeFieldContainer[DiscoveredSpec]
    total_candidates: int
    total_refined: int
    usage: _types_pb2.LLMUsage
    warnings: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, specs: _Optional[_Iterable[_Union[DiscoveredSpec, _Mapping]]] = ..., total_candidates: _Optional[int] = ..., total_refined: _Optional[int] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ..., warnings: _Optional[_Iterable[str]] = ...) -> None: ...
