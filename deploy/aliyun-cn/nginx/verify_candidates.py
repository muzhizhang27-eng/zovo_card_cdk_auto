#!/usr/bin/env python3
"""Validate staged configs without installing them or starting CDK applications.

Run on Ali: python3 verify_candidates.py /var/tmp/cdk-cn-candidates-20261104
Only the IP/header/maintenance harness starts nginx, on a temporary loopback port.
"""
import hashlib
import http.client
import http.server
import json
import pathlib
import socket
import subprocess
import sys
import tempfile
import threading
import time

base = pathlib.Path(sys.argv[1]).resolve()
nginx = base / "nginx"
with tempfile.TemporaryDirectory(prefix="cdk-nginx-review-", dir="/var/tmp") as temporary:
    test = pathlib.Path(temporary)
    test.chmod(0o755)  # Worker must be able to stat the synthetic maintenance file.
    snippets = test / "snippets"
    snippets.mkdir()
    private = test / "private"
    private.mkdir()
    for name in ["cdk-admin-proxy.conf", "cdk-maintenance-gate.conf"]:
        (snippets / name).write_bytes((nginx / name).read_bytes())
    for name in ["cdk-acme-proxy.conf", "cdk-admin-acme-proxy.conf"]:
        (snippets / name).write_bytes((base / "tls" / name).read_bytes())
    confs = []
    for name in ["00-cdk-client-ip.conf", "00-cdk-maintenance.conf", "00-cdk-static-cache.conf", "cdk.lanting.live.conf", "cdk-admin.lanting.live.conf"]:
        content = (nginx / name).read_text().replace("/etc/nginx/snippets/", str(snippets) + "/").replace("/etc/nginx/cdk-private/", str(private) + "/")
        destination = test / name
        destination.write_text(content)
        confs.append(destination)
    full = test / "full-nginx.conf"
    full.write_text("pid " + str(test / "full.pid") + ";\nerror_log " + str(test / "full-error.log") + ";\nevents {}\nhttp {\ninclude /etc/nginx/mime.types;\n" + "\n".join("include " + str(path) + ";" for path in confs) + "\n}\n")
    subprocess.run(["nginx", "-t", "-c", str(full)], check=True)

    # Parse the actual unit directives; env is present, applications are not started.
    unit_test = test / "units"
    unit_test.mkdir()
    for source in (base / "systemd").iterdir():
        if source.suffix not in [".service", ".slice"]:
            continue
        content = source.read_text()
        lines = content.splitlines()
        (unit_test / source.name).write_text("\n".join(lines) + "\n")
    subprocess.run(["systemd-analyze", "verify"] + [str(path) for path in unit_test.iterdir()], check=True)

    class EchoHeaders(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            body = json.dumps(dict(self.headers.items())).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    upstream = http.server.HTTPServer(("127.0.0.1", 0), EchoHeaders)
    thread = threading.Thread(target=upstream.serve_forever)
    thread.daemon = True
    thread.start()
    with socket.socket() as allocator:
        allocator.bind(("127.0.0.1", 0))
        port = allocator.getsockname()[1]
    # Test values only. The production files remain untouched and use socket IP.
    (private / "cdk-forward-auth.map").write_text('"43.156.235.208:cdk.lanting.live:test-cdk" 1;\n')
    (private / "cdk-admin-forward-auth.map").write_text('"43.156.235.208:cdk-admin.lanting.live:test-admin" 1;\n')
    (private / "cdk-origin-auth.map").write_text('"cdk.lanting.live:test-cdk-origin" 1;\n')
    (private / "cdk-admin-origin-auth.map").write_text('"cdk-admin.lanting.live:test-admin-origin" 1;\n')
    address_maps = confs[0].read_text().replace("$realip_remote_addr", "$http_x_test_socket_ip")
    (test / "test-ip-maps.conf").write_text(address_maps)
    gate = (nginx / "cdk-maintenance-gate.conf").read_text().replace("/var/lib/cdk-migration/maintenance", str(test / "maintenance"))
    (test / "test-gate.conf").write_text(gate)
    proxy = (nginx / "cdk-admin-proxy.conf").read_text().replace("127.0.0.1:9081", "127.0.0.1:" + str(upstream.server_port))
    (test / "test-proxy.conf").write_text(proxy)
    harness = test / "harness.conf"
    harness.write_text("pid " + str(test / "harness.pid") + ";\nerror_log " + str(test / "harness-error.log") + ";\nevents {}\nhttp { access_log off;\ninclude " + str(test / "test-ip-maps.conf") + ";\ninclude " + str(confs[1]) + ";\nserver { listen 127.0.0.1:" + str(port) + ";\ninclude " + str(test / "test-gate.conf") + ";\nlocation ^~ /.well-known/acme-challenge/ { return 200 acme; }\nlocation / { include " + str(test / "test-proxy.conf") + "; }\n}}\n")
    process = subprocess.Popen(["nginx", "-c", str(harness), "-g", "daemon off;"])
    try:
        for attempt in range(30):
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=1):
                    break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("Loopback harness did not start")

        def request(peer, host, secret, supplied_ip, path="/probe", origin_auth="invalid-origin"):
            client = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
            client.request("GET", path, headers={"Host": host, "X-Test-Socket-IP": peer, "X-CDK-Forward-Auth": secret, "X-CDK-Origin-Auth": origin_auth, "X-CDK-Client-IP": supplied_ip, "X-Forwarded-For": "forged", "X-Real-IP": "forged", "Forwarded": "for=forged"})
            response = client.getresponse()
            result = response.status, response.read()
            client.close()
            return result

        cases = [
            ("203.0.113.7", "cdk.lanting.live", "test-cdk", "192.0.2.8", "203.0.113.7"),
            ("43.156.235.208", "cdk.lanting.live", "test-cdk", "192.0.2.8", "192.0.2.8"),
            ("43.156.235.208", "cdk-admin.lanting.live", "test-admin", "2001:db8::1", "2001:db8::1"),
            ("43.156.235.208", "cdk-admin.lanting.live", "test-cdk", "192.0.2.8", "43.156.235.208"),
            ("43.156.235.208", "cdk.lanting.live", "wrong", "192.0.2.8", "43.156.235.208"),
        ]
        for invalid in ["", "999.999.1.2", "255.255.255.255", "1.2.3.4:80", "deadbeef", "[::1]", "fe80::1%eth0", "1.2.3.4,5.6.7.8", "1" * 46, "1:2:3:4:5:6:7:8:9"]:
            cases.append(("43.156.235.208", "cdk.lanting.live", "test-cdk", invalid, "43.156.235.208"))
        for peer, host, secret, supplied_ip, expected in cases:
            status, body = request(peer, host, secret, supplied_ip)
            assert status == 200, (status, supplied_ip)
            headers = {key.lower(): value for key, value in json.loads(body.decode()).items()}
            assert headers.get("x-forwarded-for") == expected, (supplied_ip, headers)
            assert headers.get("x-real-ip") == expected
            assert "x-cdk-forward-auth" not in headers and "x-cdk-client-ip" not in headers and "forwarded" not in headers and "x-cdk-origin-auth" not in headers
        eo_cases = [
            ("cdk.lanting.live", "test-cdk-origin", "192.0.2.8", "192.0.2.8"),
            ("cdk-admin.lanting.live", "test-admin-origin", "2001:db8::2", "2001:db8::2"),
            ("cdk-admin.lanting.live", "test-cdk-origin", "192.0.2.8", "203.0.113.9"),
            ("cdk.lanting.live", "test-cdk-origin", "999.1.2.3", "203.0.113.9"),
        ]
        for host, origin_auth, supplied_ip, expected in eo_cases:
            status, body = request("203.0.113.9", host, "invalid-sg", supplied_ip, origin_auth=origin_auth)
            assert status == 200
            headers = {key.lower(): value for key, value in json.loads(body.decode()).items()}
            assert headers.get("x-forwarded-for") == expected and headers.get("x-real-ip") == expected
            assert not any(name in headers for name in ["x-cdk-origin-auth", "x-cdk-forward-auth", "x-cdk-client-ip", "forwarded"])
        (test / "maintenance").touch()
        assert request("203.0.113.7", "cdk.lanting.live", "", "")[0] == 503
        assert request("203.0.113.7", "cdk.lanting.live", "", "", "/.well-known/acme-challenge/probe")[0] == 200
        (test / "maintenance").unlink()
        assert request("203.0.113.7", "cdk.lanting.live", "", "")[0] == 200
        print(json.dumps({"candidateNginxSyntax": "pass", "unitDirectiveSyntax": "pass", "clientIpAndHeaderCases": len(cases) + len(eo_cases), "maintenanceAndAcmeCases": 3, "liveConfigChanged": False, "cdkApplicationsStarted": False}))
    finally:
        process.terminate()
        process.wait(timeout=10)
        upstream.shutdown()
        upstream.server_close()
