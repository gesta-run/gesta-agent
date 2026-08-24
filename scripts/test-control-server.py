#!/usr/bin/env python3
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def handler_for(request_log: Path):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200 if self.path == "/healthz" else 404)
            self.end_headers()

        def do_POST(self):
            self.send_response(503)
            self.end_headers()

        def do_DELETE(self):
            length = int(self.headers.get("Content-Length", "0"))
            payload = json.loads(self.rfile.read(length) or b"{}")
            request_log.write_text(
                json.dumps(
                    {
                        "authorization": self.headers.get("Authorization", ""),
                        "daemon_id": payload.get("daemon_id", ""),
                        "path": self.path,
                    }
                ),
                encoding="utf-8",
            )
            self.send_response(204)
            self.end_headers()

        def log_message(self, _format, *_args):
            pass

    return Handler


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--request-log", required=True, type=Path)
    args = parser.parse_args()
    ThreadingHTTPServer(
        ("127.0.0.1", args.port), handler_for(args.request_log)
    ).serve_forever()


if __name__ == "__main__":
    main()
