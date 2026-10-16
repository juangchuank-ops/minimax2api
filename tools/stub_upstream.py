#!/usr/bin/env python3
"""A stub MiniMax Agent upstream, for exercising the gateway without credits.

The real upstream cannot answer a video request usefully: its agent sandbox has
no tool instance to submit the generation with, so a turn costs points and
produces nothing (see UPSTREAM.md). That makes the real service useless as a
target for testing *this* gateway, whose job is to phrase the turn correctly.

So this stands in for it. It answers the two calls the adapter makes — a session
handshake and the message stream — and writes one record per turn: the
`<video-generation-options>` block it was sent, and the reference pictures the
turn carried. Those two are what the gateway decides, and they are the thing
under test.

Usage:
    python tools/stub_upstream.py [--port 18081] [--log /tmp/turns.jsonl]
"""

import argparse
import base64
import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

# One transparent pixel: the smallest thing that is really an image, served for
# the reference URLs a test hands the gateway.
TINY_PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQ"
    "AAAABJRU5ErkJggg=="
)

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
        path = urlsplit(self.path).path
        self._note(f"GET  {path}")
        # Reference pictures. The gateway fetches a reference itself now, so a
        # test URL has to resolve to something — and this is the only server in
        # the test that is guaranteed to be up.
        if path.startswith("/image/"):
            self.send_response(200)
            self.send_header("content-type", "image/png")
            self.send_header("content-length", str(len(TINY_PNG)))
            self.end_headers()
            self.wfile.write(TINY_PNG)
            return
        # Everything else that is not the message stream: the session handshake,
        # the agent config, the drive lookups. The gateway tolerates a drive
        # that answers nothing useful, so one shape covers all of them.
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

        # The object-store leg. A picture is uploaded before the turn now — the
        # upstream refuses an attachment without an object key the account owns
        # — so a stub that cannot complete an upload cannot test anything that
        # carries one. The key is derived from the file name so a test can
        # assert on it.
        if path.endswith("/uploads/prepare"):
            try:
                prepared = json.loads(raw)
            except ValueError:
                prepared = {}
            name = prepared.get("file_name") or "upload.bin"
            self._json({
                "object_key": "stub/" + name,
                "upload_instruction": {
                    "method": "POST",
                    "url": f"http://127.0.0.1:{self.server.server_port}/oss",
                    "form_fields": {"key": "stub"},
                },
            })
            return
        if path.endswith("/oss"):
            # The real object store answers 204 with no body, and the client
            # treats anything else as a failure.
            self.send_response(204)
            self.send_header("content-length", "0")
            self.end_headers()
            return

        if path.endswith("/message"):
            try:
                payload = json.loads(raw)
            except ValueError:
                payload = {}
            content = payload.get("content", "")
            match = OPTIONS.search(content)
            self._note(f"     block={'found' if match else 'MISSING'} content={content[-160:]!r}")

            # One record per turn: the generation parameters and the pictures
            # the turn actually carried. The second half is what makes a
            # reference-image test mean anything — the response says what the
            # gateway decided, this says what it did.
            # Pictures and documents share the envelope and are told apart by
            # the kind string: "image" is a picture. A reference with no object
            # key is the thing the upstream refuses, so the key is recorded too
            # — it is the difference between "the gateway meant to send a
            # picture" and "the picture actually got uploaded".
            references = []
            for item in payload.get("attachments") or []:
                meta = item.get("meta") or {}
                if meta.get("attachment_type") != "image":
                    continue
                cloud = item.get("cloud") or {}
                references.append({
                    "kind": meta.get("attachment_type"),
                    "file_name": meta.get("file_name"),
                    "mime_type": meta.get("mime_type"),
                    "object_key": cloud.get("object_key"),
                })
            record = {
                "options": json.loads(match.group(1)) if match else None,
                "references": references,
            }
            with Handler.lock:
                Handler.blocks.append(record)
                if Handler.log_path:
                    with open(Handler.log_path, "a", encoding="utf-8") as handle:
                        handle.write(json.dumps(record, ensure_ascii=False) + "\n")
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
