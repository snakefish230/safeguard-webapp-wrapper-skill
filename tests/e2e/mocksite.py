"""Mock two-step login site (username -> Next -> password -> TOTP) for wrapper E2E tests."""
import http.server, json, sys, urllib.parse, pathlib
OUT = pathlib.Path(sys.argv[2] if len(sys.argv) > 2 else "submissions.jsonl")
LOGIN = '<html><body><form method=post action=/step2><input id=username name=u><button id=next type=submit>Next</button></form></body></html>'
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _send(self, html):
        self.send_response(200); self.send_header("Content-Type","text/html"); self.end_headers(); self.wfile.write(html.encode())
    def do_GET(self):
        self._send(LOGIN if self.path.startswith("/login") else "<html><body>home</body></html>")
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); f = dict(urllib.parse.parse_qsl(self.rfile.read(n).decode()))
        h = lambda k: f'<input type=hidden name={k} value="{f.get(k,"")}">'
        if self.path == "/step2":
            self._send(f'<html><body><form method=post action=/step3>{h("u")}<input id=password type=password name=p><button id=signin type=submit>Sign in</button></form></body></html>')
        elif self.path == "/step3":
            self._send(f'<html><body><form method=post action=/done>{h("u")}{h("p")}<input id=otp name=o><button id=verify type=submit>Verify</button></form></body></html>')
        elif self.path == "/done":
            with OUT.open("a") as fh: fh.write(json.dumps(f) + "\n")
            self._send("<html><body><h1 id=welcome>Welcome</h1></body></html>")
http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
