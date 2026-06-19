import asyncio
import secrets
from dataclasses import dataclass
from typing import AsyncIterator, List, Optional

from common.logger import create_logger

logger = create_logger(__name__)

DEFAULT_MAX_CALLBACKS = 16384


@dataclass(frozen=True)
class BufferedCallback:
    id: str
    path: str
    body: bytes
    seq: int


class CallbackBuffer:
    def __init__(self, max_callbacks: int = DEFAULT_MAX_CALLBACKS):
        self._boot_id = secrets.token_hex(8)
        self._max_callbacks = max_callbacks
        self._condition = asyncio.Condition()
        self._callbacks: List[BufferedCallback] = []
        self._consumer_generation = 0
        self._next_seq = 0

    def _parse_seq(self, callback_id: str) -> Optional[int]:
        prefix = f"{self._boot_id}-"
        if not callback_id.startswith(prefix):
            return None
        try:
            return int(callback_id[len(prefix):])
        except ValueError:
            return None

    async def add(self, path: str, body: bytes) -> str:
        async with self._condition:
            self._next_seq += 1
            seq = self._next_seq
            callback = BufferedCallback(
                id=f"{self._boot_id}-{seq}",
                path=path,
                body=body,
                seq=seq,
            )
            self._callbacks.append(callback)
            if len(self._callbacks) > self._max_callbacks:
                dropped = self._callbacks.pop(0)
                logger.warning(
                    "PoC callback buffer overflow: dropped %s (%s)",
                    dropped.id,
                    dropped.path,
                )
            self._condition.notify_all()
            return callback.id

    async def ack(self, callback_ids: List[str]) -> None:
        if not callback_ids:
            return
        acked = set(callback_ids)
        async with self._condition:
            self._callbacks = [c for c in self._callbacks if c.id not in acked]

    async def stream(self, resume_after_id: str = "") -> AsyncIterator[BufferedCallback]:
        async with self._condition:
            self._consumer_generation += 1
            my_generation = self._consumer_generation
            resume_seq = self._parse_seq(resume_after_id) if resume_after_id else None
            if resume_seq is not None:
                self._callbacks = [c for c in self._callbacks if c.seq > resume_seq]
            self._condition.notify_all()

        last_sent_seq = 0
        while True:
            async with self._condition:
                while True:
                    if self._consumer_generation != my_generation:
                        return
                    pending = [c for c in self._callbacks if c.seq > last_sent_seq]
                    if pending:
                        break
                    await self._condition.wait()
            for callback in pending:
                yield callback
                last_sent_seq = callback.seq

    def pending_count(self) -> int:
        return len(self._callbacks)
