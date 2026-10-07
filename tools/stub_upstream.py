#!/usr/bin/env python3
"""A stub MiniMax Agent upstream, for exercising the gateway without credits.

The real upstream cannot answer a video request usefully: its agent sandbox has
no tool instance to submit the generation with, so a turn costs points and
produces nothing (see UPSTREAM.md). That makes the real service useless as a
target for testing *this* gateway, whose job is to phrase the turn correctly.

So this stands in for it. It answers the two calls the adapter makes — a session
handshake and the message stream — and records every options block it is sent,
which is the thing under test.

Usage:
    python tools/stub_upstream.py [--port 18081] [--log /tmp/blocks.jsonl]
"""

import argparse
import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

# The upstream's own reader, transcribed from the web bundle: the block is
# anchored to the end of the message, so a block anywhere else is not a block.
OPTIONS = re.compile(r"<video-generation-options>\r?\n(.*?)\r?\n</video-generation-options>\s*$", re.S)


class Handler(BaseHTTPRequestHandler):
    blocks: list = []
    lock = threading.Lock()
    log_path = ""

    def log_message(self, *args):  # keep the test output readable
        pass

    def _note(self, note):
        if Handler.log_path:
            with open(Handler.log_path + ".requests", "a", encoding="utf-8") as handle:
                handle.write(note + "\n")

    def _json(self, payload):
        body = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _sse(self, frames):
        body = "".join("data:" + json.dumps(frame) + "\n\n" for frame in frames).encode()
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        # Everything that is not the message stream: the session handshake, the
        # agent config, the drive lookups. The gateway tolerates a drive that
        # answers nothing useful, so one shape covers all of them.
        self._note(f"GET  {urlsplit(self.path).path}")
        self._json({"session_id": "sess_stub", "status": "ok"})

    def do_POST(self):
        length = int(self.headers.get("content-length") or 0)
        raw = self.rfile.read(length) if length else b""
        # The query string carries the whole signature and every request has
        # one, so the path has to be split before it can be compared. Matching
        # on `self.path` silently classifies every call as a handshake, and the
        # symptom is a gateway that reports an empty upstream response.
        path = urlsplit(self.path).path
        self._note(f"POST {path} ({length} bytes)")

        if path.endswith("/message"):
            try:
                content = json.loads(raw).get("content", "")
            except (ValueError, AttributeError):
                content = ""
            match = OPTIONS.search(content)
            self._note(f"     block={'found' if match else 'MISSING'} content={content[-160:]!r}")
            if match:
                with Handler.lock:
                    Handler.blocks.append(match.group(1))
                    if Handler.log_path:
                        with open(Handler.log_path, "a", encoding="utf-8") as handle:
                            handle.write(match.group(1) + "\n")
            # A media frame, so the gateway's media merge path is exercised too.
            self._sse([
                {"type": 6, "agent_message_chunk": {"msg_content": "已提交"},
                 "media": [{"image_url": "https://stub.invalid/clip.mp4"}]},
                {"type": 9, "finish_reason": "stop"},
            ])
            return

        self._json({"session_id": "sess_stub", "status": "ok"})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=18081)
    parser.add_argument("--log", default="")
    args = parser.parse_args()
    Handler.log_path = args.log
    if args.log:
        open(args.log, "w", encoding="utf-8").close()

    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"stub upstream on http://127.0.0.1:{args.port}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
