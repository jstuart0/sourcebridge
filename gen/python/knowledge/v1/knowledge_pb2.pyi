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
    __slots__ = ("sections", "usage")
    SECTIONS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    sections: _containers.RepeatedCompositeFieldContainer[KnowledgeSection]
    usage: _types_pb2.LLMUsage
    def __init__(self, sections: _Optional[_Iterable[_Union[KnowledgeSection, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

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
    __slots__ = ("order", "title", "objective", "content", "file_paths", "symbol_ids", "estimated_time")
    ORDER_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    OBJECTIVE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    FILE_PATHS_FIELD_NUMBER: _ClassVar[int]
    SYMBOL_IDS_FIELD_NUMBER: _ClassVar[int]
    ESTIMATED_TIME_FIELD_NUMBER: _ClassVar[int]
    order: int
    title: str
    objective: str
    content: str
    file_paths: _containers.RepeatedScalarFieldContainer[str]
    symbol_ids: _containers.RepeatedScalarFieldContainer[str]
    estimated_time: str
    def __init__(self, order: _Optional[int] = ..., title: _Optional[str] = ..., objective: _Optional[str] = ..., content: _Optional[str] = ..., file_paths: _Optional[_Iterable[str]] = ..., symbol_ids: _Optional[_Iterable[str]] = ..., estimated_time: _Optional[str] = ...) -> None: ...

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
    __slots__ = ("order", "title", "description", "file_path", "line_start", "line_end")
    ORDER_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    LINE_START_FIELD_NUMBER: _ClassVar[int]
    LINE_END_FIELD_NUMBER: _ClassVar[int]
    order: int
    title: str
    description: str
    file_path: str
    line_start: int
    line_end: int
    def __init__(self, order: _Optional[int] = ..., title: _Optional[str] = ..., description: _Optional[str] = ..., file_path: _Optional[str] = ..., line_start: _Optional[int] = ..., line_end: _Optional[int] = ...) -> None: ...

class KnowledgeSection(_message.Message):
    __slots__ = ("title", "content", "summary", "confidence", "inferred", "evidence")
    TITLE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    SUMMARY_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    INFERRED_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_FIELD_NUMBER: _ClassVar[int]
    title: str
    content: str
    summary: str
    confidence: str
    inferred: bool
    evidence: _containers.RepeatedCompositeFieldContainer[KnowledgeEvidence]
    def __init__(self, title: _Optional[str] = ..., content: _Optional[str] = ..., summary: _Optional[str] = ..., confidence: _Optional[str] = ..., inferred: bool = ..., evidence: _Optional[_Iterable[_Union[KnowledgeEvidence, _Mapping]]] = ...) -> None: ...

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
