import threading
from enum import Enum
from abc import ABC, abstractmethod
from typing import Optional

from common.logger import create_logger

logger = create_logger(__name__)


class ManagerState(Enum):
    STARTING = "STARTING"
    RUNNING = "RUNNING"
    STOPPING = "STOPPING"
    STOPPED = "STOPPED"
    FAILED = "FAILED"


class IManager(ABC):
    def __init__(self):
        self._is_active = False
        self._lock = threading.Lock()
        self._exception: Optional[Exception] = None
        self._state = ManagerState.STOPPED
    def start(self, *args, **kwargs):
        with self._lock:
            self._state = ManagerState.STARTING
            try:
                self._start(*args, **kwargs)
                self._is_active = True
                self._state = ManagerState.RUNNING
            except Exception as e:
                logger.error(f"Failed to start {self.__class__.__name__}: {e}")
                self._exception = e
                self._state = ManagerState.FAILED
                raise e
    
    def stop(self):
        with self._lock:
            self._state = ManagerState.STOPPING
            try:
                self._is_active = False
                self._stop()
                self._state = ManagerState.STOPPED
            except Exception as e:
                logger.error(f"Failed to stop {self.__class__.__name__}: {e}")
                self._is_active = False
                self._exception = e
                self._state = ManagerState.FAILED
                raise e

    def is_healthy(self) -> bool:
        if self._exception:
            logger.error(f"Manager {self.__class__.__name__} has failed with exception: {self._exception}")
            return False

        if not self._is_active:
            return True

        try:
            return self._is_healthy()
        except Exception as e:
            logger.error(f"Manager {self.__class__.__name__} has failed with exception: {e}")
            return False

    def get_state(self) -> ManagerState:
        return self._state

    @abstractmethod
    def _start(self):
        pass

    @abstractmethod
    def _stop(self):
        pass

    @abstractmethod
    def _is_healthy(self) -> bool:
        pass