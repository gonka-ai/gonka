import grpc

from common.logger import create_logger
from api.pocstream.buffer import CallbackBuffer
from api.pocstream.gen import pb2, pb2_grpc

logger = create_logger(__name__)


class PoCCallbackStreamServicer(pb2_grpc.PoCCallbackStreamServicer):
    def __init__(self, buffer: CallbackBuffer):
        self._buffer = buffer

    async def StreamCallbacks(self, request, context):
        logger.info(
            "PoC callback stream opened: resume_after=%s",
            request.resume_after_id or "<none>",
        )
        try:
            async for callback in self._buffer.stream(request.resume_after_id):
                yield pb2.Callback(
                    id=callback.id,
                    path=callback.path,
                    body=callback.body,
                )
        finally:
            logger.info("PoC callback stream closed")

    async def AckCallbacks(self, request, context):
        await self._buffer.ack(list(request.ids))
        return pb2.AckCallbacksResponse()


async def start_poc_stream_server(buffer: CallbackBuffer, port: int) -> grpc.aio.Server:
    server = grpc.aio.server()
    pb2_grpc.add_PoCCallbackStreamServicer_to_server(
        PoCCallbackStreamServicer(buffer), server
    )
    server.add_insecure_port(f"[::]:{port}")
    await server.start()
    logger.info("PoC callback stream gRPC server listening on port %d", port)
    return server
