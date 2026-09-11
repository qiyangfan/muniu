#!/usr/bin/env python3
# T5.4 压测用假上游:固定延迟 SSE 剧本(标准库实现,零依赖)。
# 用法:python3 scripts/bench/fake_upstream.py [port] [chunk数] [每帧延迟秒]
# 剧本契约:与 internal/testkit 剧本形态一致——role 帧 → N 段 content → usage 终帧 → [DONE]。
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 59990
CHUNKS = int(sys.argv[2]) if len(sys.argv) > 2 else 20
DELAY = float(sys.argv[3]) if len(sys.argv) > 3 else 0.01


def chunk(content=None, role=False, finish=None):
    delta = {}
    if role:
        delta["role"] = "assistant"
    if content is not None:
        delta["content"] = content
    body = {
        "id": "bench",
        "object": "chat.completion.chunk",
        "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
    }
    return "data: " + json.dumps(body, ensure_ascii=False) + "\n\n"


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.read_body()
        try:
            req = json.loads(body)
            stream = bool(req.get("stream"))
        except Exception as e:
            print("body parse err:", e, file=sys.stderr)
            stream = b'"stream":true' in body
        print("seen body:", body[:200], "-> stream:", stream, file=sys.stderr)

        if not stream:
            time.sleep(DELAY * CHUNKS)
            resp = {
                "id": "bench",
                "object": "chat.completion",
                "choices": [{"index": 0, "message": {"role": "assistant", "content": "x" * CHUNKS}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 10, "completion_tokens": CHUNKS, "total_tokens": 10 + CHUNKS},
            }
            data = json.dumps(resp, ensure_ascii=False).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        def emit(payload):
            self.wfile.write(payload.encode())
            self.wfile.flush()

        emit(chunk(role=True))
        for i in range(CHUNKS):
            time.sleep(DELAY)
            emit(chunk("x"))
        emit(chunk(finish="stop"))
        emit("data: [DONE]\n\n")

    def read_body(self):
        """兼容 Content-Length 与 chunked 两种请求体(one-api 转发用 chunked)。"""
        length = self.headers.get("Content-Length")
        if length:
            return self.rfile.read(int(length))
        body = b""
        while True:
            line = self.rfile.readline().strip()
            if not line:
                break
            size = int(line, 16)
            if size == 0:
                self.rfile.readline()
                break
            body += self.rfile.read(size)
            self.rfile.readline()
        return body

    def log_message(self, *args):
        pass  # 压测期间静默,避免日志 IO 干扰数字


if __name__ == "__main__":
    print(f"fake upstream :{PORT} chunks={CHUNKS} delay={DELAY}s", file=sys.stderr)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
