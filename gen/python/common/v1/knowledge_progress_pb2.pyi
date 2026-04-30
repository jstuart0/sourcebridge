from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class KnowledgePhase(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    KNOWLEDGE_PHASE_UNSPECIFIED: _ClassVar[KnowledgePhase]
    KNOWLEDGE_PHASE_SNAPSHOT: _ClassVar[KnowledgePhase]
    KNOWLEDGE_PHASE_LEAF_SUMMARIES: _ClassVar[KnowledgePhase]
    KNOWLEDGE_PHASE_FILE_SUMMARIES: _ClassVar[KnowledgePhase]
    KNOWLEDGE_PHASE_PACKAGE_SUMMARIES: _ClassVar[KnowledgePhase]
    KNOWLEDGE_PHASE_ROOT_SYNTHESIS: _ClassVar[KnowledgePhase]
    KNOWLEDGE_PHASE_RENDER: _ClassVar[KnowledgePhase]
    KNOWLEDGE_PHASE_FINALIZING: _ClassVar[KnowledgePhase]
KNOWLEDGE_PHASE_UNSPECIFIED: KnowledgePhase
KNOWLEDGE_PHASE_SNAPSHOT: KnowledgePhase
KNOWLEDGE_PHASE_LEAF_SUMMARIES: KnowledgePhase
KNOWLEDGE_PHASE_FILE_SUMMARIES: KnowledgePhase
KNOWLEDGE_PHASE_PACKAGE_SUMMARIES: KnowledgePhase
KNOWLEDGE_PHASE_ROOT_SYNTHESIS: KnowledgePhase
KNOWLEDGE_PHASE_RENDER: KnowledgePhase
KNOWLEDGE_PHASE_FINALIZING: KnowledgePhase

class KnowledgeStreamProgress(_message.Message):
    __slots__ = ("phase", "completed_units", "total_units", "unit_kind", "message", "leaf_cache_hits", "file_cache_hits", "package_cache_hits", "root_cache_hits")
    PHASE_FIELD_NUMBER: _ClassVar[int]
    COMPLETED_UNITS_FIELD_NUMBER: _ClassVar[int]
    TOTAL_UNITS_FIELD_NUMBER: _ClassVar[int]
    UNIT_KIND_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    LEAF_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    FILE_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    PACKAGE_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    ROOT_CACHE_HITS_FIELD_NUMBER: _ClassVar[int]
    phase: KnowledgePhase
    completed_units: int
    total_units: int
    unit_kind: str
    message: str
    leaf_cache_hits: int
    file_cache_hits: int
    package_cache_hits: int
    root_cache_hits: int
    def __init__(self, phase: _Optional[_Union[KnowledgePhase, str]] = ..., completed_units: _Optional[int] = ..., total_units: _Optional[int] = ..., unit_kind: _Optional[str] = ..., message: _Optional[str] = ..., leaf_cache_hits: _Optional[int] = ..., file_cache_hits: _Optional[int] = ..., package_cache_hits: _Optional[int] = ..., root_cache_hits: _Optional[int] = ...) -> None: ...

class KnowledgeStreamPhaseMarker(_message.Message):
    __slots__ = ("phase", "detail")
    PHASE_FIELD_NUMBER: _ClassVar[int]
    DETAIL_FIELD_NUMBER: _ClassVar[int]
    phase: KnowledgePhase
    detail: str
    def __init__(self, phase: _Optional[_Union[KnowledgePhase, str]] = ..., detail: _Optional[str] = ...) -> None: ...
