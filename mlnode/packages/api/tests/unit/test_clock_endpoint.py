"""Contract test for GET /api/v1/clock (gateway-host-ping-observability)."""

import time

from fastapi import FastAPI
from fastapi.testclient import TestClient

from api.clock import router


def test_clock_contract():
    app = FastAPI()
    app.include_router(router)

    response = TestClient(app).get("/clock")

    assert response.status_code == 204
    assert response.content == b""

    recv_ns = int(response.headers["X-Server-Recv-Ns"])
    send_ns = int(response.headers["X-Server-Send-Ns"])
    assert send_ns >= recv_ns
    # Nanosecond scale: within a day of the test's own clock.
    assert abs(recv_ns - time.time_ns()) < 86_400 * 10**9
