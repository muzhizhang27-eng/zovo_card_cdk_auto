#!/usr/bin/env python3
"""Exercise real candidate vhosts in a temporary loopback-only Nginx.

Usage on Ali: python3 nginx/verify_static_cache.py /path/to/staged/aliyun-cn
No production config, application, certificate, database or provider is used.
Only listeners/TLS transport, filesystem paths and upstream destinations are
substituted. Cache, gzip, security headers and routing use the actual candidates.
"""
import gzip
import http.client
import http.server
import json
import pathlib
import re
import shlex
import socket
import subprocess
import sys
import tempfile
import threading
import time


IMMUTABLE = "public, max-age=31536000, immutable"
HOSTS = ["cdk.lanting.live", "cdk-admin.lanting.live"]
BODY = (b"/* synthetic cache/gzip fixture; no business data */\n" * 512)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class FakeAPI(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"synthetic":true}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def main():
    base = pathlib.Path(sys.argv[1]).resolve()
    nginx = base / "nginx"
    checks = []
    with tempfile.TemporaryDirectory(prefix="cdk-cache-review-", dir="/var/tmp") as temporary:
        test = pathlib.Path(temporary)
        test.chmod(0o755)
        snippets, private = test / "snippets", test / "private"
        snippets.mkdir()
        private.mkdir()
        upstream = http.server.HTTPServer(("127.0.0.1", 0), FakeAPI)
        thread = threading.Thread(target=upstream.serve_forever)
        thread.daemon = True
        thread.start()
        port, redirect_port = free_port(), free_port()
        while redirect_port == port:
            redirect_port = free_port()
        marker = test / "maintenance"
        (snippets / "cdk-maintenance-gate.conf").write_text(
            (nginx / "cdk-maintenance-gate.conf").read_text().replace(
                "/var/lib/cdk-migration/maintenance", str(marker)))
        (snippets / "cdk-admin-proxy.conf").write_text(
            (nginx / "cdk-admin-proxy.conf").read_text().replace(
                "127.0.0.1:9081", "127.0.0.1:" + str(upstream.server_port)))
        for name in ["cdk-acme-proxy.conf", "cdk-admin-acme-proxy.conf"]:
            (snippets / name).write_text("location ^~ /.well-known/acme-challenge/ { return 404; }\n")
        includes = []
        for name in ["00-cdk-client-ip.conf", "00-cdk-maintenance.conf", "00-cdk-static-cache.conf"]:
            content = (nginx / name).read_text().replace("/etc/nginx/cdk-private/", str(private) + "/")
            path = test / name
            path.write_text(content)
            includes.append(path)
        expected_security = {}
        for host, service in zip(HOSTS, ["cdk-recharge", "cdk-admin"]):
            root = test / service
            root.mkdir()
            root.chmod(0o755)
            (root / "assets").mkdir()
            (root / "assets").chmod(0o755)
            (root / "index.html").write_bytes(b"<html><body>synthetic page</body></html>")
            (root / "index.html").chmod(0o644)
            for filename in ["app-A1b2C3_d.js", "style-Abc_12-3.css", "plain.js", "short-Abc1234.js", "long-Abc123456.js"]:
                (root / "assets" / filename).write_bytes(BODY)
                (root / "assets" / filename).chmod(0o644)
            original = (nginx / (host + ".conf")).read_text()
            security = {}
            for line in original.splitlines():
                if line.strip().startswith("add_header "):
                    tokens = shlex.split(line.strip().rstrip(";"))
                    if tokens[1].lower() != "cache-control":
                        security[tokens[1].lower()] = tokens[2]
            expected_security[host] = security
            content = original.replace("listen 80;", "listen 127.0.0.1:" + str(redirect_port) + ";")
            content = content.replace("listen 443 ssl http2;", "listen 127.0.0.1:" + str(port) + ";")
            content = re.sub(r"^\s*listen \[::\].*;\n", "", content, flags=re.MULTILINE)
            content = re.sub(r"^\s*ssl_.*;\n", "", content, flags=re.MULTILINE)
            content = re.sub(r"^\s*access_log .*;", "    access_log off;", content, flags=re.MULTILINE)
            content = content.replace("/opt/" + service + "/current/web", str(root))
            content = content.replace("/etc/nginx/snippets/", str(snippets) + "/")
            content = content.replace("127.0.0.1:9080", "127.0.0.1:" + str(upstream.server_port))
            path = test / (host + ".conf")
            path.write_text(content)
            includes.append(path)
        config = test / "nginx.conf"
        config.write_text("pid " + str(test / "nginx.pid") + ";\nerror_log " + str(test / "error.log") + ";\nevents {}\nhttp {\ninclude /etc/nginx/mime.types;\n" +
                          "\n".join("include " + str(path) + ";" for path in includes) + "\n}\n")
        subprocess.run(["nginx", "-t", "-c", str(config)], check=True)
        process = subprocess.Popen(["nginx", "-c", str(config), "-g", "daemon off;"])
        try:
            for attempt in range(50):
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=1):
                        break
                except OSError:
                    time.sleep(0.05)
            else:
                raise RuntimeError("Temporary Nginx did not start")

            def request(host, path, expected_status, expected_cache, method="GET", headers=None, selected_port=None):
                connection = http.client.HTTPConnection("127.0.0.1", selected_port or port, timeout=5)
                sent = {"Host": host, "Accept-Encoding": "identity"}
                sent.update(headers or {})
                connection.request(method, path, headers=sent)
                response = connection.getresponse()
                body = response.read()
                received = {}
                for name, value in response.getheaders():
                    received.setdefault(name.lower(), []).append(value)
                connection.close()
                assert response.status == expected_status, (host, method, path, response.status)
                assert received.get("cache-control") and all(value == expected_cache for value in received["cache-control"]), (host, method, path, received.get("cache-control"))
                if selected_port != redirect_port:
                    for name, value in expected_security.get(host, {}).items():
                        assert received.get(name) == [value], (host, path, "security header lost", name)
                checks.append({"host": host, "method": method, "path": path, "status": response.status,
                               "cacheControl": received["cache-control"], "securityHeadersVerified": selected_port != redirect_port and host in expected_security})
                return received, body

            for host in HOSTS:
                headers, body = request(host, "/assets/app-A1b2C3_d.js", 200, IMMUTABLE)
                assert body == BODY
                etag = headers["etag"][0]
                _, body = request(host, "/assets/app-A1b2C3_d.js", 200, IMMUTABLE, method="HEAD")
                assert body == b""
                _, body = request(host, "/assets/app-A1b2C3_d.js", 206, IMMUTABLE, headers={"Range": "bytes=0-31"})
                assert body == BODY[:32]
                _, body = request(host, "/assets/app-A1b2C3_d.js", 304, IMMUTABLE, headers={"If-None-Match": etag})
                assert body == b""
                for path in ["/assets/app-A1b2C3_d.js", "/assets/style-Abc_12-3.css"]:
                    h, compressed = request(host, path, 200, IMMUTABLE, headers={"Accept-Encoding": "gzip", "Via": "1.1 synthetic-edge"})
                    assert h.get("content-encoding") == ["gzip"], (host, path, h)
                    assert any("accept-encoding" in v.lower() for v in h.get("vary", []))
                    assert gzip.decompress(compressed) == BODY
                    assert len(compressed) < len(BODY)
                    checks[-1]["gzipDecodedBytesMatch"] = True
                    checks[-1]["uncompressedBytes"] = len(BODY)
                    checks[-1]["compressedBytes"] = len(compressed)
                request(host, "/assets/missing-A1b2C3_d.js", 404, "no-store")
                request(host, "/assets/app-A1b2C3_d.js", 405, "no-store", method="POST")
                for path in ["/assets/plain.js", "/assets/short-Abc1234.js", "/assets/long-Abc123456.js"]:
                    request(host, path, 200, "no-store")
                request(host, "/ops/login" if host == HOSTS[1] else "/recharge", 200, "no-store")
                h, body = request(host, "/api/v1/public/site", 200, "no-store", headers={"Accept-Encoding": "gzip"})
                assert "content-encoding" not in h
                assert json.loads(body.decode()) == {"synthetic": True}
                request(host, "/assets/app-A1b2C3_d.js", 308, "no-store", selected_port=redirect_port)
                marker.touch()
                try:
                    request(host, "/assets/app-A1b2C3_d.js", 503, "no-store")
                finally:
                    marker.unlink()
            request("unrelated.example.test", "/assets/app-A1b2C3_d.js", 200, "no-store")
            print(json.dumps({"candidateCacheSyntax": "pass", "checks": checks, "checkCount": len(checks),
                              "productionConfigChanged": False, "productionApplicationRequests": False,
                              "realProviderRequests": False, "allAssertionsPassed": True}))
        finally:
            process.terminate()
            process.wait(timeout=10)
            upstream.shutdown()
            upstream.server_close()


if __name__ == "__main__":
    main()
