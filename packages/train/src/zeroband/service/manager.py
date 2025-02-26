import subprocess
import os
import toml

TIMEOUT = 60

def set_training_env(train_env_dict: dict):
    for key, value in train_env_dict.items():
        os.environ[key] = value

class TrainManager:
    def __init__(self):
        self.process = None

    def start(self, train_dict: dict):
        if self.process is not None:
            raise RuntimeError("Training is already running")
        set_training_env(train_dict["train_env"])
        print(train_dict["train_env"])
        with open("train_config.toml", "w") as f:
            toml.dump(train_dict["train_config"], f)
        command = ["bash", "/app/packages/train/scripts/run-diloco-node.sh", "/app/packages/train/src/zeroband/train.py", "@train_config.toml"]
        self.process = subprocess.Popen(command)

    def stop(self):
        if self.process is None:
            raise RuntimeError("Training is not running")
        self.process.terminate()
        self.process.wait(timeout=5)
        if self.process.poll() is None:
            self.process.kill()
        self.process = None

    def is_running(self):
        return self.process is not None and self.process.poll() is None