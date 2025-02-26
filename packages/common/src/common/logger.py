import logging


def create_logger(name):
    logger = logging.getLogger(name)
    return setup_logger(logger)


def setup_logger(
    logger: logging.Logger,
    log_level: int = logging.DEBUG
) -> logging.Logger:
    logger.setLevel(log_level)
    handler = logging.StreamHandler()  # Outputs to console
    formatter = logging.Formatter(
        "%(asctime)s - %(name)s - %(levelname)s - %(message)s"
    )
    handler.setFormatter(formatter)
    logger.addHandler(handler)
    return logger
