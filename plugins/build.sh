#!/bin/sh
# 构建 Cargo workspace，并将 WASM 工件放入各插件目录。
# 需要 rustup 和 wasm32-unknown-unknown target。
set -eu
cd "$(dirname "$0")"

# 非交互 shell 未找到 Cargo 时，加载 rustup 环境。
if ! command -v cargo >/dev/null 2>&1 && [ -f "$HOME/.cargo/env" ]; then
    . "$HOME/.cargo/env"
fi

: "${CARGO:=cargo}"
"$CARGO" build --release --target wasm32-unknown-unknown

cp target/wasm32-unknown-unknown/release/pii_masker.wasm pii-masker/pii_masker.wasm
cp target/wasm32-unknown-unknown/release/json_redactor.wasm json-redactor/json_redactor.wasm
echo "built: $(pwd)/pii-masker/pii_masker.wasm"
echo "built: $(pwd)/json-redactor/json_redactor.wasm"
