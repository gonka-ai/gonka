"""GET /api/v1/clock — timestamp probe for dapi's mlnode ping job.

Contract (devshard/docs/proposals/gateway-host-ping-observability.md,
"Checklist for the mlnode PR"): 204, empty body, X-Server-Recv-Ns /
X-Server-Send-Ns in unix nanoseconds, no side effects. dapi computes RTT
and clock divergence from the two headers; without this endpoint it falls
back to /readyz + the Date header, which is second-granular.
"""

import time

from fastapi import APIRouter, Response

router = APIRouter()


@router.get("/clock", status_code=204)
async def clock() -> Response:
    recv_ns = time.time_ns()
    response = Response(status_code=204)
    response.headers["X-Server-Recv-Ns"] = str(recv_ns)
    response.headers["X-Server-Send-Ns"] = str(time.time_ns())
    return response
