import os

world_info = None


class WorldInfo:
    """This class parse env var about torch world into class variables."""

    world_size: int
    rank: int
    local_rank: int
    local_world_size: int

    def __init__(self):
        self.world_size = int(os.environ["WORLD_SIZE"])
        self.rank = int(os.environ["RANK"])
        self.local_rank = int(os.environ["LOCAL_RANK"])
        self.local_world_size = int(os.environ["LOCAL_WORLD_SIZE"])
        self.nnodes = self.world_size // self.local_world_size

        self.global_unique_id = os.environ.get("GLOBAL_UNIQUE_ID", None)
        self.global_addr = os.environ.get("GLOBAL_ADDR", None)
        self.global_port = int(os.environ.get("GLOBAL_PORT")) if "GLOBAL_PORT" in os.environ else None
        self.global_world_size = int(os.environ.get("GLOBAL_WORLD_SIZE", 1))
        self.global_rank = int(os.environ.get("GLOBAL_RANK", 0))

    def __repr__(self):
        return f"WorldInfo(world_size={self.world_size}, rank={self.rank}, local_rank={self.local_rank}, local_world_size={self.local_world_size}, nnodes={self.nnodes}, global_unique_id={self.global_unique_id}, global_addr={self.global_addr}, global_port={self.global_port}, global_world_size={self.global_world_size}, global_rank={self.global_rank})"

    @property
    def diloco_rank(self):
        return self.global_rank

    def json(self) -> dict[str, int | str]:
        return {
            "world_size": self.world_size,
            "rank": self.rank,
            "local_rank": self.local_rank,
            "local_world_size": self.local_world_size,
            "nnodes": self.nnodes,
            "global_unique_id": self.global_unique_id,
            "global_addr": self.global_addr,
            "global_port": self.global_port,
            "global_world_size": self.global_world_size,
            "global_rank": self.global_rank,
        }


def get_world_info() -> WorldInfo:
    """
    Return a WorldInfo singleton.
    """
    global world_info
    if world_info is None:
        world_info = WorldInfo()
    return world_info
