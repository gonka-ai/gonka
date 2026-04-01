"""PoC callback sender with retry-until-stop and bounded buffer."""
import asyncio
import hashlib
import hmac
import json
import os
import time
from collections import deque
from typing import Any, Dict, List, Optional

import aiohttp

from vllm.logger import init_logger
from .data import Artifact

logger = init_logger(__name__)

# Signature key for HMAC-SHA256 (same format as receiver). Override via POC_SIGNATURE_KEY env.
POC_SIGNATURE_KEY = (os.environ.get("POC_SIGNATURE_KEY") or "SOME_SIGNATURE_KEY").encode("utf-8")

POC_CALLBACK_INTERVAL_SEC = float(os.environ.get("POC_CALLBACK_INTERVAL_SEC", "5"))
POC_CALLBACK_MAX_ARTIFACTS = int(os.environ.get("POC_CALLBACK_MAX_ARTIFACTS", "1000000"))
POC_CALLBACK_RETRY_BACKOFF_SEC = 1.0
POC_CALLBACK_RETRY_MAX_BACKOFF_SEC = 30.0


def _sign_payload(payload: Dict) -> tuple[str, str]:
    """Return (payload_json, signature) for callback POST."""
    payload_json = json.dumps(payload, sort_keys=True, separators=(',', ':'))
    signature = hmac.new(
        POC_SIGNATURE_KEY,
        payload_json.encode('utf-8'),
        hashlib.sha256,
    ).hexdigest()
    return payload_json, signature


class CallbackSender:
    """Manages callback sending with retry and bounded buffer."""
    
    def __init__(
        self,
        callback_url: str,
        stop_event: asyncio.Event,
        k_dim: int = 12,
        max_artifacts: int = POC_CALLBACK_MAX_ARTIFACTS,
    ):
        self.callback_url = callback_url
        self.stop_event = stop_event
        self.k_dim = k_dim
        self.max_artifacts = max_artifacts
        
        self._buffer: deque[Artifact] = deque()
        self._metadata: Dict[str, Any] = {}
        self._pending_payload: Optional[Dict] = None
        self._task: Optional[asyncio.Task] = None
    
    def add_artifacts(self, artifacts: List[Artifact], metadata: Dict[str, Any]):
        """Add artifacts to buffer, dropping oldest if cap exceeded."""
        self._metadata = metadata
        for artifact in artifacts:
            self._buffer.append(artifact)
        
        while len(self._buffer) > self.max_artifacts:
            self._buffer.popleft()
    
    def clear(self):
        """Clear all buffered artifacts."""
        self._buffer.clear()
        self._pending_payload = None
    
    @property
    def buffered_count(self) -> int:
        return len(self._buffer)
    
    async def run(self):
        """Main sender loop - batches and sends with retry-until-stop."""
        last_send_time = time.time()
        backoff = POC_CALLBACK_RETRY_BACKOFF_SEC
        retry_attempt = 0
        
        async with aiohttp.ClientSession() as session:
            while not self.stop_event.is_set():
                await asyncio.sleep(0.1)
                
                current_time = time.time()
                should_send = (
                    (self._buffer or self._pending_payload) and
                    (current_time - last_send_time >= POC_CALLBACK_INTERVAL_SEC)
                )
                
                if not should_send:
                    continue
                
                if self._pending_payload is None and self._buffer:
                    artifacts_to_send = list(self._buffer)
                    self._buffer.clear()
                    self._pending_payload = {
                        **self._metadata,
                        "artifacts": [{"nonce": a.nonce, "vector_b64": a.vector_b64} for a in artifacts_to_send],
                        "encoding": {"dtype": "f16", "k_dim": self.k_dim, "endian": "le"},
                    }
                    retry_attempt = 0
                
                if self._pending_payload:
                    retry_attempt += 1
                    success = await self._send_callback(session, self._pending_payload, retry_attempt)
                    if success:
                        if retry_attempt > 1:
                            logger.info(f"Callback to {self.callback_url} succeeded after {retry_attempt} attempts")
                        self._pending_payload = None
                        backoff = POC_CALLBACK_RETRY_BACKOFF_SEC
                        retry_attempt = 0
                        last_send_time = current_time
                    else:
                        if retry_attempt == 1 or retry_attempt % 10 == 0:
                            logger.warning(f"Callback to {self.callback_url} failed (attempt {retry_attempt}, next backoff {backoff:.1f}s)")
                        await asyncio.sleep(backoff)
                        backoff = min(backoff * 2, POC_CALLBACK_RETRY_MAX_BACKOFF_SEC)
    
    async def _send_callback(self, session: aiohttp.ClientSession, payload: Dict, attempt: int = 1) -> bool:
        """Send callback with HMAC-SHA256 signature, return True on success."""
        try:
            payload_json, signature = self._sign_payload(payload)
            headers = {
                "Content-Type": "application/json",
                "X-Signature": signature,
            }
            async with session.post(
                f"{self.callback_url}/generated",
                data=payload_json,
                headers=headers,
                timeout=aiohttp.ClientTimeout(total=10),
            ) as resp:
                if resp.status < 400:
                    logger.debug(f"Callback sent: {len(payload.get('artifacts', []))} artifacts")
                    return True
                # Log failure reason: status + body (truncated)
                try:
                    body = await resp.text()
                    body_preview = body[:500] + "..." if len(body) > 500 else body
                except Exception:
                    body_preview = "(could not read body)"
                logger.warning(
                    "Callback to %s/generated failed: HTTP %s %s, body: %s",
                    self.callback_url,
                    resp.status,
                    resp.reason or "",
                    body_preview,
                )
                return False
        except Exception as e:
            logger.warning(
                "Callback to %s/generated failed: %s",
                self.callback_url,
                e,
                exc_info=False,
            )
            return False

async def send_oneshot_callback(
    url: str,
    path: str,
    payload: Dict,
    stop_event: Optional[asyncio.Event] = None,
):
    """Send a single callback with retry-until-stop or success (signed)."""
    backoff = POC_CALLBACK_RETRY_BACKOFF_SEC
    attempt = 0
    payload_json, signature = _sign_payload(payload)
    headers = {"Content-Type": "application/json", "X-Signature": signature}

    async with aiohttp.ClientSession() as session:
        while True:
            attempt += 1
            if stop_event and stop_event.is_set():
                return False

            try:
                async with session.post(
                    f"{url}/{path}",
                    data=payload_json,
                    headers=headers,
                    timeout=aiohttp.ClientTimeout(total=10),
                ) as resp:
                    if resp.status < 400:
                        if attempt > 1:
                            logger.info(f"Callback to {url}/{path} succeeded after {attempt} attempts")
                        return True
                    if attempt == 1 or attempt % 10 == 0:
                        logger.warning(f"Callback to {url}/{path} HTTP {resp.status} (attempt {attempt}, next backoff {backoff:.1f}s)")
            except Exception as e:
                if attempt == 1 or attempt % 10 == 0:
                    logger.warning(f"Callback to {url}/{path} failed: {e} (attempt {attempt}, next backoff {backoff:.1f}s)")
            
            if stop_event is None:
                return False
            
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, POC_CALLBACK_RETRY_MAX_BACKOFF_SEC)
