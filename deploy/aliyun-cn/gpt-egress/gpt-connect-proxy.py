#!/usr/bin/env python3
"""Loopback-only CONNECT relay for the ChatGPT HTTPS endpoint.

The private SSH tunnel carries the connection; TLS stays end to end. This
relay never decrypts traffic or logs requests, headers, bodies or tokens.
"""
import select
import socket
import socketserver
import threading

ALLOWED_TARGETS = frozenset({"chatgpt.com:443"})
LISTEN_ADDRESS = ("127.0.0.1", 18785)
LIMIT = threading.BoundedSemaphore(8)


def parse_target(request):
    try:
        method, target, protocol = request.decode("ascii").rstrip("\r\n").split(" ")
    except (UnicodeDecodeError, ValueError):
        return None
    if method != "CONNECT" or protocol not in {"HTTP/1.0", "HTTP/1.1"}:
        return None
    return target if target in ALLOWED_TARGETS else None


class Handler(socketserver.BaseRequestHandler):
    def reply(self, code, reason):
        self.request.sendall(
            f"HTTP/1.1 {code} {reason}\r\nConnection: close\r\n\r\n".encode("ascii")
        )

    def handle(self):
        if not LIMIT.acquire(blocking=False):
            self.reply(503, "Busy")
            return
        try:
            self.relay()
        except (OSError, TimeoutError):
            pass
        finally:
            LIMIT.release()

    def relay(self):
        self.request.settimeout(10)
        header = b""
        while b"\r\n\r\n" not in header:
            data = self.request.recv(4096)
            if not data:
                return
            header += data
            if len(header) > 16384:
                self.reply(431, "Header Too Large")
                return
        head, extra = header.split(b"\r\n\r\n", 1)
        target = parse_target(head.split(b"\r\n", 1)[0])
        if target is None:
            self.reply(403, "Forbidden")
            return
        host, port = target.rsplit(":", 1)
        try:
            upstream = socket.create_connection((host, int(port)), timeout=10)
        except OSError:
            self.reply(502, "Bad Gateway")
            return
        with upstream:
            self.request.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            if extra:
                upstream.sendall(extra)
            upstream.settimeout(30)
            self.request.settimeout(30)
            peers = {self.request: upstream, upstream: self.request}
            while True:
                ready, _, _ = select.select(list(peers), [], [], 45)
                if not ready:
                    return
                for source in ready:
                    data = source.recv(65536)
                    if not data:
                        return
                    peers[source].sendall(data)


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True
    request_queue_size = 8

    def handle_error(self, request, client_address):
        # Never emit request contents or client addresses into service logs.
        pass


if __name__ == "__main__":
    with Server(LISTEN_ADDRESS, Handler) as server:
        server.serve_forever(poll_interval=1)
