from common.v1 import types_pb2 as _types_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ContractFile(_message.Message):
    __slots__ = ("file_path", "contract_type", "version", "content_hash", "endpoints")
    FILE_PATH_FIELD_NUMBER: _ClassVar[int]
    CONTRACT_TYPE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    CONTENT_HASH_FIELD_NUMBER: _ClassVar[int]
    ENDPOINTS_FIELD_NUMBER: _ClassVar[int]
    file_path: str
    contract_type: str
    version: str
    content_hash: str
    endpoints: _containers.RepeatedCompositeFieldContainer[ContractEndpoint]
    def __init__(self, file_path: _Optional[str] = ..., contract_type: _Optional[str] = ..., version: _Optional[str] = ..., content_hash: _Optional[str] = ..., endpoints: _Optional[_Iterable[_Union[ContractEndpoint, _Mapping]]] = ...) -> None: ...

class ContractEndpoint(_message.Message):
    __slots__ = ("path", "method", "description")
    PATH_FIELD_NUMBER: _ClassVar[int]
    METHOD_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    path: str
    method: str
    description: str
    def __init__(self, path: _Optional[str] = ..., method: _Optional[str] = ..., description: _Optional[str] = ...) -> None: ...

class DetectContractsRequest(_message.Message):
    __slots__ = ("repository_id", "files")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    FILES_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    files: _containers.RepeatedCompositeFieldContainer[FileContent]
    def __init__(self, repository_id: _Optional[str] = ..., files: _Optional[_Iterable[_Union[FileContent, _Mapping]]] = ...) -> None: ...

class FileContent(_message.Message):
    __slots__ = ("path", "content", "language")
    PATH_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    path: str
    content: str
    language: _types_pb2.Language
    def __init__(self, path: _Optional[str] = ..., content: _Optional[str] = ..., language: _Optional[_Union[_types_pb2.Language, str]] = ...) -> None: ...

class DetectContractsResponse(_message.Message):
    __slots__ = ("contracts", "usage")
    CONTRACTS_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    contracts: _containers.RepeatedCompositeFieldContainer[ContractFile]
    usage: _types_pb2.LLMUsage
    def __init__(self, contracts: _Optional[_Iterable[_Union[ContractFile, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...

class ConsumerMatch(_message.Message):
    __slots__ = ("consumer_file", "contract_file", "endpoint_path", "confidence", "evidence")
    CONSUMER_FILE_FIELD_NUMBER: _ClassVar[int]
    CONTRACT_FILE_FIELD_NUMBER: _ClassVar[int]
    ENDPOINT_PATH_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_FIELD_NUMBER: _ClassVar[int]
    consumer_file: str
    contract_file: str
    endpoint_path: str
    confidence: float
    evidence: str
    def __init__(self, consumer_file: _Optional[str] = ..., contract_file: _Optional[str] = ..., endpoint_path: _Optional[str] = ..., confidence: _Optional[float] = ..., evidence: _Optional[str] = ...) -> None: ...

class MatchConsumersRequest(_message.Message):
    __slots__ = ("repository_id", "contracts", "source_files")
    REPOSITORY_ID_FIELD_NUMBER: _ClassVar[int]
    CONTRACTS_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FILES_FIELD_NUMBER: _ClassVar[int]
    repository_id: str
    contracts: _containers.RepeatedCompositeFieldContainer[ContractFile]
    source_files: _containers.RepeatedCompositeFieldContainer[FileContent]
    def __init__(self, repository_id: _Optional[str] = ..., contracts: _Optional[_Iterable[_Union[ContractFile, _Mapping]]] = ..., source_files: _Optional[_Iterable[_Union[FileContent, _Mapping]]] = ...) -> None: ...

class MatchConsumersResponse(_message.Message):
    __slots__ = ("matches", "usage")
    MATCHES_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    matches: _containers.RepeatedCompositeFieldContainer[ConsumerMatch]
    usage: _types_pb2.LLMUsage
    def __init__(self, matches: _Optional[_Iterable[_Union[ConsumerMatch, _Mapping]]] = ..., usage: _Optional[_Union[_types_pb2.LLMUsage, _Mapping]] = ...) -> None: ...
