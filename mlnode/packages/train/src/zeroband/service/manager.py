import asyncio
import subprocess
import os
import toml
import logging
from typing import Optional
from common.logger import create_logger
from common.manager import IManager

TIMEOUT = 60
TRAIN_DIR = "/app/packages/train/"
CERTS_DIR = "/app/packages/train/resources/certs/"


class TrainManager(IManager):
    def __init__(self):
        super().__init__()
        self.process: Optional[subprocess.Popen] = None
        self.logger: logging.Logger = create_logger(__name__)

    def set_gloo_certs(
        self,
        private_key_path: str,
        node_cert_path: str,
        ca_cert_path: str
    ):
        """
        Configures Gloo to use TLS for inter-node communication.

        Args:
            private_key_path (str): Full path to the private key file for this node.
            node_cert_path (str): Full path to the certificate file for this node.
            ca_cert_path (str): Full path to the CA (or CA bundle) certificate file.
        """
        os.environ["GLOO_DEVICE_TRANSPORT"] = "TCP_TLS"
        os.environ["GLOO_DEVICE_TRANSPORT_TCP_TLS_PKEY"] = private_key_path
        os.environ["GLOO_DEVICE_TRANSPORT_TCP_TLS_CERT"] = node_cert_path
        os.environ["GLOO_DEVICE_TRANSPORT_TCP_TLS_CA_FILE"] = ca_cert_path

    def set_training_env(self, train_env_dict: dict):
        for key, value in train_env_dict.items():
            os.environ[key] = value
            
    def _start(self, train_dict: dict):
        if self.process is not None:
            raise RuntimeError("Training is already running")

        # TODO: Replace with actual certs when integrated
        self.set_gloo_certs(
            os.path.join(CERTS_DIR, "dummy.key"),
            os.path.join(CERTS_DIR, "dummy.crt"),
            os.path.join(CERTS_DIR, "dummy.crt")
        )

        self.set_training_env(train_dict["train_env"])
        self.logger.info(f"Training environment: {train_dict['train_env']}")

        with open("train_config.toml", "w") as f:
            toml.dump(train_dict["train_config"], f)

        command = [
            "bash",
            os.path.join(TRAIN_DIR, "scripts/run-diloco-node.sh"),
            os.path.join(TRAIN_DIR, "src/zeroband/train.py"),
            "@train_config.toml"
        ]

        self.process = subprocess.Popen(command)

    def _stop(self):
        if self.process is None:
            raise RuntimeError("Training is not running")
        self.logger.info("Stopping training")
        self.process.terminate()
        self.process.wait(timeout=5)
        if self.process.poll() is None:
            self.process.kill()
        self.process = None

    def is_running(self) -> bool:
        return self.process is not None and self.process.poll() is None

    def _is_healthy(self) -> bool:
        return self.is_running()
    
    async def start_async(self, train_dict: dict):
        return await asyncio.to_thread(self.start, train_dict)
