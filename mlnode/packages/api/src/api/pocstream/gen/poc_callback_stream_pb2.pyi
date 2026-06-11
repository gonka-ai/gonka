from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Iterable as _Iterable, Optional as _Optional

DESCRIPTOR: _descriptor.FileDescriptor

class StreamCallbacksRequest(_message.Message):
    __slots__ = ("resume_after_id",)
    RESUME_AFTER_ID_FIELD_NUMBER: _ClassVar[int]
    resume_after_id: str
    def __init__(self, resume_after_id: _Optional[str] = ...) -> None: ...

class Callback(_message.Message):
    __slots__ = ("id", "path", "body")
    ID_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    BODY_FIELD_NUMBER: _ClassVar[int]
    id: str
    path: str
    body: bytes
    def __init__(self, id: _Optional[str] = ..., path: _Optional[str] = ..., body: _Optional[bytes] = ...) -> None: ...

class AckCallbacksRequest(_message.Message):
    __slots__ = ("ids",)
    IDS_FIELD_NUMBER: _ClassVar[int]
    ids: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, ids: _Optional[_Iterable[str]] = ...) -> None: ...

class AckCallbacksResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...
