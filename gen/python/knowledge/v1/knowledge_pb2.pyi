from common.v1 import types_pb2 as _types_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class GenerateCliffNotesRequest(_message.Message):
    __slots__ = ("repository_id", "repository_name", "audience", "depth", "snapshot_json", "scope_type", "scope_path")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_NAME_FIELD_NUMBER: _ClassVar[int]
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_JSON_FIELD_NUMBER: _ClassVar[int]
    SCOPE_TYPE_FIELD_NUMBER: _ClassVar[int]
    SCOPE_PATH_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    repository_name: str
    audience: str
    depth: str
    snapshot_json: str
    scope_type: str
    scope_path: str
    def __init__(self, repository_id: _Optional[str] = ..., repository_name: _Optional[str] = ..., audience: _Optional[str] = ..., depth: _Optional[str] = ..., snapshot_json: _Optional[str] = ..., scope_type: _Optional[str] = ..., scope_path: _Optional[str] = ...) -> None: ...

class GenerateCliffNotesResponse(_message.Message):
    __slots__ = ("sections", "usage", "diagnostics")
    SECTIONS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    DIAGNOSTICS_FIELD_NUMBER: _ClassVar[int]
    sections: _containers.RepeatedCompositeFieldContainer[KnowledgeSection]
    usage: _types_pb2.LLMUsage
    diagnostics: CliffNotesDiagnostics
    def __init__(self, sections: _Optional[_Iterable[_Union[KnowledgeSection, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ..., diagnostics: _Optional[_Union[CliffNotesDiagnostics, _Mapping]] = ...) -> None: ...

class GenerateArchitectureDiagramRequest(_message.Message):
    __slots__ = ("repository_id", "repository_name", "audience", "depth", "snapshot_json", "deterministic_diagram_json")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_NAME_FIELD_NUMBER: _ClassVar[int]
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_JSON_FIELD_NUMBER: _ClassVar[int]
    DETERMINISTIC_DIAGRAM_JSON_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    repository_name: str
    audience: str
    depth: str
    snapshot_json: str
    deterministic_diagram_json: str
    def __init__(self, repository_id: _Optional[str] = ..., repository_name: _Optional[str] = ..., audience: _Optional[str] = ..., depth: _Optional[str] = ..., snapshot_json: _Optional[str] = ..., deterministic_diagram_json: _Optional[str] = ...) -> None: ...

class GenerateArchitectureDiagramResponse(_message.Message):
    __slots__ = ("mermaid_source", "raw_mermaid_source", "validation_status", "repair_summary", "diagram_summary", "evidence", "inferred_edges", "usage", "detail_mermaid_source", "detail_raw_mermaid_source", "detail_validation_status", "detail_repair_summary", "detail_diagram_summary", "detail_subsystem_name", "detail_candidate_subsystems", "detail_evidence")
    MERMAID_SOURCE_FIELD_NUMBER: _ClassVar[int]
    RAW_MERMAID_SOURCE_FIELD_NUMBER: _ClassVar[int]
    VALIDATION_STATUS_FIELD_NUMBER: _ClassVar[int]
    REPAIR_SUMMARY_FIELD_NUMBER: _ClassVar[int]
    DIAGRAM_SUMMARY_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_FIELD_NUMBER: _ClassVar[int]
    INFERRED_EDGES_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    DETAIL_MERMAID_SOURCE_FIELD_NUMBER: _ClassVar[int]
    DETAIL_RAW_MERMAID_SOURCE_FIELD_NUMBER: _ClassVar[int]
    DETAIL_VALIDATION_STATUS_FIELD_NUMBER: _ClassVar[int]
    DETAIL_REPAIR_SUMMARY_FIELD_NUMBER: _ClassVar[int]
    DETAIL_DIAGRAM_SUMMARY_FIELD_NUMBER: _ClassVar[int]
    DETAIL_SUBSYSTEM_NAME_FIELD_NUMBER: _ClassVar[int]
    DETAIL_CANDIDATE_SUBSYSTEMS_FIELD_NUMBER: _ClassVar[int]
    DETAIL_EVIDENCE_FIELD_NUMBER: _ClassVar[int]
    mermaid_source: str
    raw_mermaid_source: str
    validation_status: str
    repair_summary: str
    diagram_summary: str
    evidence: _containers.RepeatedCompositeFieldContainer[KnowledgeEvidence]
    inferred_edges: _containers.RepeatedScalarFieldContainer[str]
    usage: _types_pb2.LLMUsage
    detail_mermaid_source: str
    detail_raw_mermaid_source: str
    detail_validation_status: str
    detail_repair_summary: str
    detail_diagram_summary: str
    detail_subsystem_name: str
    detail_candidate_subsystems: _containers.RepeatedScalarFieldContainer[str]
    detail_evidence: _containers.RepeatedCompositeFieldContainer[KnowledgeEvidence]
    def __init__(self, mermaid_source: _Optional[str] = ..., raw_mermaid_source: _Optional[str] = ..., validation_status: _Optional[str] = ..., repair_summary: _Optional[str] = ..., diagram_summary: _Optional[str] = ..., evidence: _Optional[_Iterable[_Union[KnowledgeEvidence, _Mapping]]] = ..., inferred_edges: _Optional[_Iterable[str]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ..., detail_mermaid_source: _Optional[str] = ..., detail_raw_mermaid_source: _Optional[str] = ..., detail_validation_status: _Optional[str] = ..., detail_repair_summary: _Optional[str] = ..., detail_diagram_summary: _Optional[str] = ..., detail_subsystem_name: _Optional[str] = ..., detail_candidate_subsystems: _Optional[_Iterable[str]] = ..., detail_evidence: _Optional[_Iterable[_Union[KnowledgeEvidence, _Mapping]]] = ...) -> None: ...

class CliffNotesDiagnostics(_message.Message):
    __slots__ = ("cached_nodes", "fallback_count", "provider_compute_errors", "leaf_cache_hits", "file_cache_hits", "package_cache_hits", "root_cache_hits", "total_nodes", "corpus_id", "revision_fp", "strategy", "model_used")
    CACHED_NODES_FIELD_NUMBER: _ClassVar[int]
    FALLBACK_COUNT_FIELD_NUMBER: _ClassVar[int]
    PROVIDER_COMPUTE_ERRORS_FIELD_NUMBER: _ClassVar[int]
    LEAF_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    FILE_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    PACKAGE_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    ROOT_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_NODES_FIELD_NUMBER: _ClassVar[int]
    CORPUS_ID_FIELD_NUMBER: _ClassVar[int]
    REVISION_FP_FIELD_NUMBER: _ClassVar[int]
    STRATEGY_FIELD_NUMBER: _ClassVar[int]
    MODEL_USED_FIELD_NUMBER: _ClassVar[int]
    cached_nodes: int
    fallback_count: int
    provider_compute_errors: int
    leaf_cache_hits: int
    file_cache_hits: int
    package_cache_hits: int
    root_cache_hits: int
    total_nodes: int
    corpus_id: str
    revision_fp: str
    strategy: str
    model_used: str
    def __init__(self, cached_nodes: _Optional[int] = ..., fallback_count: _Optional[int] = ..., provider_compute_errors: _Optional[int] = ..., leaf_cache_hits: _Optional[int] = ..., file_cache_hits: _Optional[int] = ..., package_cache_hits: _Optional[int] = ..., root_cache_hits: _Optional[int] = ..., total_nodes: _Optional[int] = ..., corpus_id: _Optional[str] = ..., revision_fp: _Optional[str] = ..., strategy: _Optional[str] = ..., model_used: _Optional[str] = ...) -> None: ...

class GenerateLearningPathRequest(_message.Message):
    __slots__ = ("repository_id", "repository_name", "audience", "depth", "snapshot_json", "focus_area")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_NAME_FIELD_NUMBER: _ClassVar[int]
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_JSON_FIELD_NUMBER: _ClassVar[int]
    FOCUS_AREA_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    repository_name: str
    audience: str
    depth: str
    snapshot_json: str
    focus_area: str
    def __init__(self, repository_id: _Optional[str] = ..., repository_name: _Optional[str] = ..., audience: _Optional[str] = ..., depth: _Optional[str] = ..., snapshot_json: _Optional[str] = ..., focus_area: _Optional[str] = ...) -> None: ...

class GenerateLearningPathResponse(_message.Message):
    __slots__ = ("steps", "usage")
    STEPS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    steps: _containers.RepeatedCompositeFieldContainer[LearningStep]
    usage: _types_pb2.LLMUsage
    def __init__(self, steps: _Optional[_Iterable[_Union[LearningStep, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class GenerateWorkflowStoryRequest(_message.Message):
    __slots__ = ("repository_id", "repository_name", "audience", "depth", "snapshot_json", "scope_type", "scope_path", "anchor_label", "execution_path_json")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_NAME_FIELD_NUMBER: _ClassVar[int]
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_JSON_FIELD_NUMBER: _ClassVar[int]
    SCOPE_TYPE_FIELD_NUMBER: _ClassVar[int]
    SCOPE_PATH_FIELD_NUMBER: _ClassVar[int]
    ANCHOR_LABEL_FIELD_NUMBER: _ClassVar[int]
    EXECUTION_PATH_JSON_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    repository_name: str
    audience: str
    depth: str
    snapshot_json: str
    scope_type: str
    scope_path: str
    anchor_label: str
    execution_path_json: str
    def __init__(self, repository_id: _Optional[str] = ..., repository_name: _Optional[str] = ..., audience: _Optional[str] = ..., depth: _Optional[str] = ..., snapshot_json: _Optional[str] = ..., scope_type: _Optional[str] = ..., scope_path: _Optional[str] = ..., anchor_label: _Optional[str] = ..., execution_path_json: _Optional[str] = ...) -> None: ...

class GenerateWorkflowStoryResponse(_message.Message):
    __slots__ = ("sections", "usage")
    SECTIONS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    sections: _containers.RepeatedCompositeFieldContainer[KnowledgeSection]
    usage: _types_pb2.LLMUsage
    def __init__(self, sections: _Optional[_Iterable[_Union[KnowledgeSection, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class LearningStep(_message.Message):
    __slots__ = ("order", "title", "objective", "content", "file_paths", "symbol_ids", "estimated_time", "prerequisite_steps", "difficulty", "exercises", "checkpoint", "confidence", "refinement_status")
    ORDER_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    OBJECTIVE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    FILE_PATHS_FIELD_NUMBER: _ClassVar[int]
    SYMBOL_IDS_FIELD_NUMBER: _ClassVar[int]
    ESTIMATED_TIME_FIELD_NUMBER: _ClassVar[int]
    PREREQUISITE_STEPS_FIELD_NUMBER: _ClassVar[int]
    DIFFICULTY_FIELD_NUMBER: _ClassVar[int]
    EXERCISES_FIELD_NUMBER: _ClassVar[int]
    CHECKPOINT_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    REFINEMENT_STATUS_FIELD_NUMBER: _ClassVar[int]
    order: int
    title: str
    objective: str
    content: str
    file_paths: _containers.RepeatedScalarFieldContainer[str]
    symbol_ids: _containers.RepeatedScalarFieldContainer[str]
    estimated_time: str
    prerequisite_steps: _containers.RepeatedScalarFieldContainer[int]
    difficulty: str
    exercises: _containers.RepeatedScalarFieldContainer[str]
    checkpoint: str
    confidence: str
    refinement_status: str
    def __init__(self, order: _Optional[int] = ..., title: _Optional[str] = ..., objective: _Optional[str] = ..., content: _Optional[str] = ..., file_paths: _Optional[_Iterable[str]] = ..., symbol_ids: _Optional[_Iterable[str]] = ..., estimated_time: _Optional[str] = ..., prerequisite_steps: _Optional[_Iterable[int]] = ..., difficulty: _Optional[str] = ..., exercises: _Optional[_Iterable[str]] = ..., checkpoint: _Optional[str] = ..., confidence: _Optional[str] = ..., refinement_status: _Optional[str] = ...) -> None: ...

class ExplainSystemRequest(_message.Message):
    __slots__ = ("repository_id", "repository_name", "audience", "question", "snapshot_json", "scope_type", "scope_path", "depth")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_NAME_FIELD_NUMBER: _ClassVar[int]
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    QUESTION_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_JSON_FIELD_NUMBER: _ClassVar[int]
    SCOPE_TYPE_FIELD_NUMBER: _ClassVar[int]
    SCOPE_PATH_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    repository_name: str
    audience: str
    question: str
    snapshot_json: str
    scope_type: str
    scope_path: str
    depth: str
    def __init__(self, repository_id: _Optional[str] = ..., repository_name: _Optional[str] = ..., audience: _Optional[str] = ..., question: _Optional[str] = ..., snapshot_json: _Optional[str] = ..., scope_type: _Optional[str] = ..., scope_path: _Optional[str] = ..., depth: _Optional[str] = ...) -> None: ...

class ExplainSystemResponse(_message.Message):
    __slots__ = ("explanation", "evidence", "usage")
    EXPLANATION_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    explanation: str
    evidence: _containers.RepeatedCompositeFieldContainer[KnowledgeEvidence]
    usage: _types_pb2.LLMUsage
    def __init__(self, explanation: _Optional[str] = ..., evidence: _Optional[_Iterable[_Union[KnowledgeEvidence, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class GenerateCodeTourRequest(_message.Message):
    __slots__ = ("repository_id", "repository_name", "audience", "depth", "snapshot_json", "theme")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_NAME_FIELD_NUMBER: _ClassVar[int]
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_JSON_FIELD_NUMBER: _ClassVar[int]
    THEME_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    repository_name: str
    audience: str
    depth: str
    snapshot_json: str
    theme: str
    def __init__(self, repository_id: _Optional[str] = ..., repository_name: _Optional[str] = ..., audience: _Optional[str] = ..., depth: _Optional[str] = ..., snapshot_json: _Optional[str] = ..., theme: _Optional[str] = ...) -> None: ...

class GenerateCodeTourResponse(_message.Message):
    __slots__ = ("stops", "usage")
    STOPS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    stops: _containers.RepeatedCompositeFieldContainer[CodeTourStop]
    usage: _types_pb2.LLMUsage
    def __init__(self, stops: _Optional[_Iterable[_Union[CodeTourStop, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class CodeTourStop(_message.Message):
    __slots__ = ("order", "title", "description", "file_path", "line_start", "line_end", "trail", "modification_hints", "confidence", "refinement_status")
    ORDER_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    LINE_START_FIELD_NUMBER: _ClassVar[int]
    LINE_END_FIELD_NUMBER: _ClassVar[int]
    TRAIL_FIELD_NUMBER: _ClassVar[int]
    MODIFICATION_HINTS_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    REFINEMENT_STATUS_FIELD_NUMBER: _ClassVar[int]
    order: int
    title: str
    description: str
    file_path: str
    line_start: int
    line_end: int
    trail: str
    modification_hints: _containers.RepeatedScalarFieldContainer[str]
    confidence: str
    refinement_status: str
    def __init__(self, order: _Optional[int] = ..., title: _Optional[str] = ..., description: _Optional[str] = ..., file_path: _Optional[str] = ..., line_start: _Optional[int] = ..., line_end: _Optional[int] = ..., trail: _Optional[str] = ..., modification_hints: _Optional[_Iterable[str]] = ..., confidence: _Optional[str] = ..., refinement_status: _Optional[str] = ...) -> None: ...

class KnowledgeSection(_message.Message):
    __slots__ = ("title", "content", "summary", "confidence", "inferred", "evidence", "refinement_status")
    TITLE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    SUMMARY_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    INFERRED_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_FIELD_NUMBER: _ClassVar[int]
    REFINEMENT_STATUS_FIELD_NUMBER: _ClassVar[int]
    title: str
    content: str
    summary: str
    confidence: str
    inferred: bool
    evidence: _containers.RepeatedCompositeFieldContainer[KnowledgeEvidence]
    refinement_status: str
    def __init__(self, title: _Optional[str] = ..., content: _Optional[str] = ..., summary: _Optional[str] = ..., confidence: _Optional[str] = ..., inferred: bool = ..., evidence: _Optional[_Iterable[_Union[KnowledgeEvidence, _Mapping]]] = ..., refinement_status: _Optional[str] = ...) -> None: ...

class KnowledgeEvidence(_message.Message):
    __slots__ = ("source_type", "source_id", "file_path", "line_start", "line_end", "rationale")
    SOURCE_TYPE_FIELD_NUMBER: _ClassVar[int]
    SOURCE_ID_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    LINE_START_FIELD_NUMBER: _ClassVar[int]
    LINE_END_FIELD_NUMBER: _ClassVar[int]
    RATIONALE_FIELD_NUMBER: _ClassVar[int]
    source_type: str
    source_id: str
    file_path: str
    line_start: int
    line_end: int
    rationale: str
    def __init__(self, source_type: _Optional[str] = ..., source_id: _Optional[str] = ..., file_path: _Optional[str] = ..., line_start: _Optional[int] = ..., line_end: _Optional[int] = ..., rationale: _Optional[str] = ...) -> None: ...

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
