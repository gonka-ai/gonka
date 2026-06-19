#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."

OUT=mlnode/packages/api/src/api/pocstream/gen

python3 -m grpc_tools.protoc \
    -I proto \
    --python_out="$OUT" \
    --pyi_out="$OUT" \
    --grpc_python_out="$OUT" \
    poc_callback_stream.proto

sed -i.bak \
    's/^import poc_callback_stream_pb2 as poc__callback__stream__pb2/from . import poc_callback_stream_pb2 as poc__callback__stream__pb2/' \
    "$OUT/poc_callback_stream_pb2_grpc.py"
rm -f "$OUT/poc_callback_stream_pb2_grpc.py.bak"
