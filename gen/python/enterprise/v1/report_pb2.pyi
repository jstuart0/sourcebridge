from common.v1 import types_pb2 as _types_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class GenerateReportRequest(_message.Message):
    __slots__ = ("report_id", "report_name", "report_type", "audience", "repository_ids", "selected_sections", "include_diagrams", "loe_mode", "output_dir", "repo_data_json", "section_definitions_json", "model_override", "analysis_depth", "include_recommendations", "include_loe", "style_system_prompt", "style_section_rules")
    REPORT_ID_FIELD_NUMBER: _ClassVar[int]
    REPORT_NAME_FIELD_NUMBER: _ClassVar[int]
    REPORT_TYPE_FIELD_NUMBER: _ClassVar[int]
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_IDS_FIELD_NUMBER: _ClassVar[int]
    SELECTED_SECTIONS_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_DIAGRAMS_FIELD_NUMBER: _ClassVar[int]
    LOE_MODE_FIELD_NUMBER: _ClassVar[int]
    OUTPUT_DIR_FIELD_NUMBER: _ClassVar[int]
    REPO_DATA_JSON_FIELD_NUMBER: _ClassVar[int]
    SECTION_DEFINITIONS_JSON_FIELD_NUMBER: _ClassVar[int]
    MODEL_OVERRIDE_FIELD_NUMBER: _ClassVar[int]
    ANALYSIS_DEPTH_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_RECOMMENDATIONS_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_LOE_FIELD_NUMBER: _ClassVar[int]
    STYLE_SYSTEM_PROMPT_FIELD_NUMBER: _ClassVar[int]
    STYLE_SECTION_RULES_FIELD_NUMBER: _ClassVar[int]
    report_id: str
    report_name: str
    report_type: str
    audience: str
    repository_ids: _containers.RepeatedScalarFieldContainer[str]
    selected_sections: _containers.RepeatedScalarFieldContainer[str]
    include_diagrams: bool
    loe_mode: str
    output_dir: str
    repo_data_json: str
    section_definitions_json: str
    model_override: str
    analysis_depth: str
    include_recommendations: bool
    include_loe: bool
    style_system_prompt: str
    style_section_rules: str
    def __init__(self, report_id: _Optional[str] = ..., report_name: _Optional[str] = ..., report_type: _Optional[str] = ..., audience: _Optional[str] = ..., repository_ids: _Optional[_Iterable[str]] = ..., selected_sections: _Optional[_Iterable[str]] = ..., include_diagrams: bool = ..., loe_mode: _Optional[str] = ..., output_dir: _Optional[str] = ..., repo_data_json: _Optional[str] = ..., section_definitions_json: _Optional[str] = ..., model_override: _Optional[str] = ..., analysis_depth: _Optional[str] = ..., include_recommendations: bool = ..., include_loe: bool = ..., style_system_prompt: _Optional[str] = ..., style_section_rules: _Optional[str] = ...) -> None: ...

class GenerateReportResponse(_message.Message):
    __slots__ = ("markdown", "section_count", "word_count", "evidence_count", "content_dir", "sections", "usage", "evidence_json")
    MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    SECTION_COUNT_FIELD_NUMBER: _ClassVar[int]
    WORD_COUNT_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_COUNT_FIELD_NUMBER: _ClassVar[int]
    CONTENT_DIR_FIELD_NUMBER: _ClassVar[int]
    SECTIONS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_JSON_FIELD_NUMBER: _ClassVar[int]
    markdown: str
    section_count: int
    word_count: int
    evidence_count: int
    content_dir: str
    sections: _containers.RepeatedCompositeFieldContainer[ReportSectionResult]
    usage: _types_pb2.LLMUsage
    evidence_json: str
    def __init__(self, markdown: _Optional[str] = ..., section_count: _Optional[int] = ..., word_count: _Optional[int] = ..., evidence_count: _Optional[int] = ..., content_dir: _Optional[str] = ..., sections: _Optional[_Iterable[_Union[ReportSectionResult, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ..., evidence_json: _Optional[str] = ...) -> None: ...

class ReportSectionResult(_message.Message):
    __slots__ = ("key", "title", "category", "status", "word_count", "duration_ms", "error_message")
    KEY_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    CATEGORY_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    WORD_COUNT_FIELD_NUMBER: _ClassVar[int]
    DURATION_MS_FIELD_NUMBER: _ClassVar[int]
    ERROR_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    key: str
    title: str
    category: str
    status: str
    word_count: int
    duration_ms: int
    error_message: str
    def __init__(self, key: _Optional[str] = ..., title: _Optional[str] = ..., category: _Optional[str] = ..., status: _Optional[str] = ..., word_count: _Optional[int] = ..., duration_ms: _Optional[int] = ..., error_message: _Optional[str] = ...) -> None: ...
