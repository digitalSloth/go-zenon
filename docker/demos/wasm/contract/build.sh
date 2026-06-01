#!/bin/sh
# Compile demo.wat to demo.wasm using wat2wasm (from wabt).
# Install: brew install wabt  |  apt install wabt
set -e
cd "$(dirname "$0")"
wat2wasm demo.wat -o demo.wasm
echo "built demo.wasm ($(wc -c < demo.wasm) bytes)"
