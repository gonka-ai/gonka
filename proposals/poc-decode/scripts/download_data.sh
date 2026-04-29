#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POC_DECODE_DIR="$(dirname "$SCRIPT_DIR")"
ZIP="$POC_DECODE_DIR/data.zip"

echo "Downloading data.zip..."
curl -L "https://drive.usercontent.google.com/download?id=1qRsPwkGa5SMzjVvU5K6QiQHJp5F2Bvgj&export=download&confirm=t&uuid=24959bee-917f-4a86-92a4-6a5e3be1b9a5" -o "$ZIP"

echo "Unpacking to $POC_DECODE_DIR..."
unzip -q "$ZIP" -d "$POC_DECODE_DIR"
rm "$ZIP"

echo "Done."
