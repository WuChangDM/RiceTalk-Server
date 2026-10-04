#!/usr/bin/env bash
# 下载 RidgeRiceTalk TTS 模型（Sherpa-ONNX VITS）
# 支持镜像覆盖和断点续传
# Usage: ./download-tts-model.sh [OUTPUT_DIR]
#   OUTPUT_DIR 默认: ridgericetalk/server/models/tts

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_OUTPUT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)/server/models/tts"
OUTPUT_DIR="${1:-$DEFAULT_OUTPUT_DIR}"

MODEL_URL="${TTS_MODEL_URL:-https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-melo-tts-zh_en.tar.bz2}"
MODEL_NAME="vits-melo-tts-zh_en"
MODEL_PATH="$OUTPUT_DIR/$MODEL_NAME"
ARCHIVE_PATH="$OUTPUT_DIR/$MODEL_NAME.tar.bz2"

mkdir -p "$OUTPUT_DIR"

if [[ -f "$MODEL_PATH/model.onnx" ]]; then
    echo "TTS 模型已存在: $MODEL_PATH"
    exit 0
fi

echo "下载 TTS 模型到 $OUTPUT_DIR ..."
echo "模型 URL: $MODEL_URL"

if curl -C - -fSL --progress-bar -o "$ARCHIVE_PATH" "$MODEL_URL"; then
    echo "下载完成，解压中..."
    tar -xjf "$ARCHIVE_PATH" -C "$OUTPUT_DIR"
    rm -f "$ARCHIVE_PATH"
    if [[ -f "$MODEL_PATH/model.onnx" ]]; then
        echo "TTS 模型准备完成: $MODEL_PATH"
    else
        echo "错误：解压后未找到 $MODEL_PATH/model.onnx" >&2
        exit 1
    fi
else
    echo "错误：TTS 模型下载失败" >&2
    echo "可尝试设置镜像：TTS_MODEL_URL=<mirror> ./download-tts-model.sh" >&2
    exit 1
fi
