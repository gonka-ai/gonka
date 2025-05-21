from abc import ABC, abstractmethod
from typing import Optional


class ITrackableTask(ABC):
    @abstractmethod
    def is_alive(self) -> bool:
        pass

    def get_error_if_exist(self) -> Optional[str]:
        return None
