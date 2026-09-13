#!/bin/sh
# muniu 插件构建脚本:Cargo workspace 全量构建 → 产物落位到各插件目录。
# 产物供 muniu 注册:muniu.yaml plugins[].path 或 PUT /admin/v1/plugins/{name} {"path": "..."}。
# 依赖:rustup(https://rustup.rs)+ `rustup target add wasm32-unknown-unknown`。
set -eu
cd "$(dirname "$0")"

# rustup 标准安装的 cargo 在非交互 shell 不在 PATH(本机实证),兜底加载其环境
if ! command -v cargo >/dev/null 2>&1 && [ -f "$HOME/.cargo/env" ]; then
    . "$HOME/.cargo/env"
fi

: "${CARGO:=cargo}"
"$CARGO" build --release --target wasm32-unknown-unknown

cp target/wasm32-unknown-unknown/release/pii_masker.wasm pii-masker/pii_masker.wasm
cp target/wasm32-unknown-unknown/release/json_redactor.wasm json-redactor/json_redactor.wasm
echo "built: $(pwd)/pii-masker/pii_masker.wasm"
echo "built: $(pwd)/json-redactor/json_redactor.wasm"
