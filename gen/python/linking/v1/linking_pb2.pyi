from common.v1 import types_pb2 as _types_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class CandidateSymbol(_message.Message):
    __slots__ = ("symbol", "content")
    SYMBOL_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    symbol: _types_pb2.CodeSymbol
    content: str
    def __init__(self, symbol: _Optional[_Union[_types_pb2.CodeSymbol, _Mapping]] = ..., content: _Optional[str] = ...) -> None: ...

class LinkRequirementRequest(_message.Message):
    __slots__ = ("requirement", "repository_id", "candidate_symbols", "min_confidence")
    REQUIREMENT_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    CANDIDATE_SYMBOLS_FIELD_NUMBER: _ClassVar[int]
    MIN_CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    requirement: _types_pb2.Requirement
    repository_id: str
    candidate_symbols: _containers.RepeatedCompositeFieldContainer[CandidateSymbol]
    min_confidence: float
    def __init__(self, requirement: _Optional[_Union[_types_pb2.Requirement, _Mapping]] = ..., repository_id: _Optional[str] = ..., candidate_symbols: _Optional[_Iterable[_Union[CandidateSymbol, _Mapping]]] = ..., min_confidence: _Optional[float] = ...) -> None: ...

class LinkRequirementResponse(_message.Message):
    __slots__ = ("links", "usage")
    LINKS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    links: _containers.RepeatedCompositeFieldContainer[_types_pb2.RequirementLink]
    usage: _types_pb2.LLMUsage
    def __init__(self, links: _Optional[_Iterable[_Union[_types_pb2.RequirementLink, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class BatchLinkRequest(_message.Message):
    __slots__ = ("requirements", "repository_id", "min_confidence")
    REQUIREMENTS_FIELD_NUMBER: _ClassVar[int]
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    MIN_CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    requirements: _containers.RepeatedCompositeFieldContainer[_types_pb2.Requirement]
    repository_id: str
    min_confidence: float
    def __init__(self, requirements: _Optional[_Iterable[_Union[_types_pb2.Requirement, _Mapping]]] = ..., repository_id: _Optional[str] = ..., min_confidence: _Optional[float] = ...) -> None: ...

class BatchLinkResponse(_message.Message):
    __slots__ = ("links", "requirements_processed", "links_found", "usage")
    LINKS_FIELD_NUMBER: _ClassVar[int]
    REQUIREMENTS_PROCESSED_FIELD_NUMBER: _ClassVar[int]
    LINKS_FOUND_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    links: _containers.RepeatedCompositeFieldContainer[_types_pb2.RequirementLink]
    requirements_processed: int
    links_found: int
    usage: _types_pb2.LLMUsage
    def __init__(self, links: _Optional[_Iterable[_Union[_types_pb2.RequirementLink, _Mapping]]] = ..., requirements_processed: _Optional[int] = ..., links_found: _Optional[int] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class ValidateLinkRequest(_message.Message):
    __slots__ = ("link", "current_symbol")
    LINK_FIELD_NUMBER: _ClassVar[int]
    CURRENT_SYMBOL_FIELD_NUMBER: _ClassVar[int]
    link: _types_pb2.RequirementLink
    current_symbol: _types_pb2.CodeSymbol
    def __init__(self, link: _Optional[_Union[_types_pb2.RequirementLink, _Mapping]] = ..., current_symbol: _Optional[_Union[_types_pb2.CodeSymbol, _Mapping]] = ...) -> None: ...

class ValidateLinkResponse(_message.Message):
    __slots__ = ("still_valid", "new_confidence", "reason", "usage")
    STILL_VALID_FIELD_NUMBER: _ClassVar[int]
    NEW_CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    still_valid: bool
    new_confidence: _types_pb2.Confidence
    reason: str
    usage: _types_pb2.LLMUsage
    def __init__(self, still_valid: bool = ..., new_confidence: _Optional[_Union[_types_pb2.Confidence, str]] = ..., reason: _Optional[str] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...
