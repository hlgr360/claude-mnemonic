#!/usr/bin/env python3
"""Serve ui/dist and proxy /api/* to the isolated worker (SSE is answered 204 so the page does not hang)."""
import http.server, os, sys, urllib.request, urllib.error
DIST, WORKER, PORT = sys.argv[1], sys.argv[2], int(sys.argv[3])

class H(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **k): super().__init__(*a, directory=DIST, **k)
    def log_message(self, *a): pass
    def _proxy(self):
        if self.path.startswith("/api/events") or self.path.startswith("/api/stream"):
            self.send_response(204); self.end_headers(); return
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0)) or None
        req = urllib.request.Request(WORKER + self.path, data=body, method=self.command,
                                     headers={k: v for k, v in self.headers.items() if k.lower() in ("content-type",)})
        try:
            with urllib.request.urlopen(req, timeout=20) as r:
                data = r.read(); self.send_response(r.status)
                for k, v in r.headers.items():
                    if k.lower() in ("content-type", "cache-control"): self.send_header(k, v)
                self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
        except urllib.error.HTTPError as e:
            data = e.read(); self.send_response(e.code); self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
    do_GET = lambda self: self._proxy() if self.path.startswith("/api") else super(H, self).do_GET()
    do_POST = do_DELETE = do_PUT = _proxy

http.server.ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
