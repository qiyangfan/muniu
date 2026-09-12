#!/bin/sh
# pii-masker 构建脚本:Rust → wasm32-unknown-unknown(ABI v0)。产物供 muniu 注册:
#   muniu.yaml plugins[].path 或 PUT /admin/v1/plugins/{name} {"path": "..."}。
# 依赖:rustup(https://rustup.rs)+ `rustup target add wasm32-unknown-unknown`。
set -eu
cd "$(dirname "$0")"

: "${CARGO:=cargo}"
"$CARGO" build --release --target wasm32-unknown-unknown

cp target/wasm32-unknown-unknown/release/pii_masker.wasm ./pii_masker.wasm
echo "built: $(pwd)/pii_masker.wasm"
