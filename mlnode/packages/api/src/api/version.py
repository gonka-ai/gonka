import os
from importlib.metadata import PackageNotFoundError, version

try:
    _PACKAGE_VERSION = version("mlnode-api")
except PackageNotFoundError:
    _PACKAGE_VERSION = "unknown"


def get_mlnode_version() -> str:
    return os.getenv("MLNODE_RELEASE_VERSION") or _PACKAGE_VERSION
