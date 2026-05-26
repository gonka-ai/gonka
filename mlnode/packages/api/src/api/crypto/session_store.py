\
\
\
\
\
\
\
   

from __future__ import annotations

import threading
import time
from dataclasses import dataclass

                                              
DEFAULT_MAX_SESSIONS = 4096
DEFAULT_SESSION_TTL_SECONDS = 3600


@dataclass
class _SessionEntry:
    last_nonce: int
    last_touch: float


class NonceReplayError(ValueError):
    pass


class SessionNonceStore:
                                                                             

    def __init__(
        self,
        max_sessions: int = DEFAULT_MAX_SESSIONS,
        ttl_seconds: float = DEFAULT_SESSION_TTL_SECONDS,
        clock: "callable[[], float]" = time.monotonic,
    ) -> None:
        self._max_sessions = max_sessions
        self._ttl = ttl_seconds
        self._clock = clock
        self._lock = threading.Lock()
        self._entries: dict[str, _SessionEntry] = {}

    def check_and_record(self, session_id: str, nonce: int) -> None:                                                          
        now = self._clock()
        with self._lock:
            entry = self._entries.get(session_id)
            if entry is not None and nonce <= entry.last_nonce:
                raise NonceReplayError(
                    f"nonce {nonce} not greater than last seen {entry.last_nonce} "
                    f"for session {session_id!r}"
                )
            self._entries[session_id] = _SessionEntry(last_nonce=nonce, last_touch=now)
            if len(self._entries) > self._max_sessions:
                self._evict_locked(now)

    def reset(self, session_id: str) -> None:
                                                                        
        with self._lock:
            self._entries.pop(session_id, None)

    def __len__(self) -> int:
        with self._lock:
            return len(self._entries)

    def _evict_locked(self, now: float) -> None:
        cutoff = now - self._ttl
        stale = [sid for sid, e in self._entries.items() if e.last_touch < cutoff]
        for sid in stale:
            self._entries.pop(sid, None)

                                                                   
        if len(self._entries) > self._max_sessions:
            ordered = sorted(self._entries.items(), key=lambda kv: kv[1].last_touch)
            overflow = len(self._entries) - self._max_sessions
            for sid, _ in ordered[:overflow]:
                self._entries.pop(sid, None)
