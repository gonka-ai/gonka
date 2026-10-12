"""XID critical error counter via the NVML event API.

dmesg is not readable in unprivileged containers, while event registration
works even on rented container environments, so the NVML
event API is the v1 mechanism. If registration fails (very old drivers,
exotic virtualization) the listener quietly stays off and no series appear.
"""

import threading
from collections import Counter
from typing import Dict, List, Optional, Tuple

import pynvml

from common.logger import create_logger

from api.metrics import render

logger = create_logger(__name__)

_WAIT_TIMEOUT_MS = 2000

_lock = threading.Lock()
_counts: Counter = Counter()  # (gpu_index, xid) -> count
_listener: Optional[threading.Thread] = None
_event_set = None
_stop = threading.Event()


def _listen(event_set, uuid_to_index: Dict[bytes, str]):
    while not _stop.is_set():
        try:
            data = pynvml.nvmlEventSetWait(event_set, _WAIT_TIMEOUT_MS)
        except pynvml.NVMLError_Timeout:
            continue
        except Exception as e:
            logger.warning(f"XID listener stopped: {e}")
            return
        if data.eventType != pynvml.nvmlEventTypeXidCriticalError:
            continue
        try:
            uuid = pynvml.nvmlDeviceGetUUID(data.device)
            index = uuid_to_index.get(uuid, "unknown")
        except Exception:
            index = "unknown"
        xid = int(data.eventData)
        with _lock:
            _counts[(index, xid)] += 1
        logger.error(f"GPU XID {xid} on gpu_index={index}")


def start():
    """Register for XID events and start the listener thread (idempotent)."""
    global _listener, _event_set
    if _listener is not None:
        return
    event_set = None
    try:
        event_set = pynvml.nvmlEventSetCreate()
        uuid_to_index: Dict[bytes, str] = {}
        for i in range(pynvml.nvmlDeviceGetCount()):
            handle = pynvml.nvmlDeviceGetHandleByIndex(i)
            pynvml.nvmlDeviceRegisterEvents(
                handle, pynvml.nvmlEventTypeXidCriticalError, event_set
            )
            uuid_to_index[pynvml.nvmlDeviceGetUUID(handle)] = str(i)
    except Exception as e:
        logger.warning(f"XID event API unavailable, XID series disabled: {e}")
        if event_set is not None:
            _free_event_set(event_set)
        return
    _stop.clear()
    _event_set = event_set
    _listener = threading.Thread(
        target=_listen, args=(event_set, uuid_to_index), daemon=True, name="xid-listener"
    )
    _listener.start()
    logger.info(f"XID listener started for {len(uuid_to_index)} GPU(s)")


def _free_event_set(event_set):
    try:
        pynvml.nvmlEventSetFree(event_set)
    except Exception:
        pass


def stop():
    """Stop the listener and free NVML resources.

    Must run BEFORE nvmlShutdown(): joins the thread so nothing is blocked in
    nvmlEventSetWait when the NVML session goes away.
    """
    global _listener, _event_set
    _stop.set()
    if _listener is not None:
        # wait timeout is 2s, so the thread notices _stop within that
        _listener.join(timeout=_WAIT_TIMEOUT_MS / 1000 + 1)
        _listener = None
    if _event_set is not None:
        _free_event_set(_event_set)
        _event_set = None


def collect() -> List[str]:
    """Exposition lines for accumulated XID counts (none => no series)."""
    with _lock:
        items: List[Tuple[Tuple[str, int], int]] = sorted(_counts.items())
    return [
        render.series(
            "mlnode_gpu_xid_events_total", {"gpu_index": index, "xid": str(xid)}, count
        )
        for (index, xid), count in items
    ]


def reset():
    with _lock:
        _counts.clear()
