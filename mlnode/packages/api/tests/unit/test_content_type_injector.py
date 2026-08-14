"""DAPI sends JSON POSTs without Content-Type; the injector must add it.

Asserted at the ASGI level (a spy app records the scope) rather than through
a FastAPI handler: whether FastAPI itself tolerates a missing header depends
on its version (``strict_content_type``), and a handler-level test would pass
on the forgiving pin with no middleware at all.
"""

from fastapi.testclient import TestClient
from starlette.responses import Response

from api.proxy import ContentTypeInjector


def _content_type_seen_by_app(send_headers: dict | None) -> bytes | None:
    """Run one POST through the injector; return the Content-Type the app saw."""
    seen = {}

    async def app(scope, receive, send):
        seen["content-type"] = dict(scope["headers"]).get(b"content-type")
        await Response(status_code=204)(scope, receive, send)

    client = TestClient(ContentTypeInjector(app))
    client.post("/api/v1/echo", content=b'{"value": 7}', headers=send_headers)
    return seen["content-type"]


def test_missing_content_type_is_injected():
    assert _content_type_seen_by_app(None) == b"application/json"


def test_existing_content_type_is_not_overwritten():
    assert _content_type_seen_by_app({"Content-Type": "text/plain"}) == b"text/plain"
