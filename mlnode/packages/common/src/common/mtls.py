import os

CLIENT_CERT_ENV = "MLNODE_MTLS_CLIENT_CERT"
CLIENT_KEY_ENV = "MLNODE_MTLS_CLIENT_KEY"
SERVER_CA_ENV = "MLNODE_MTLS_SERVER_CA"


def mtls_enabled() -> bool:
    return bool(
        os.getenv(CLIENT_CERT_ENV)
        and os.getenv(CLIENT_KEY_ENV)
        and os.getenv(SERVER_CA_ENV)
    )


def client_kwargs() -> dict:
    if not mtls_enabled():
        return {}
    return {
        "cert": (os.environ[CLIENT_CERT_ENV], os.environ[CLIENT_KEY_ENV]),
        "verify": os.environ[SERVER_CA_ENV],
    }
