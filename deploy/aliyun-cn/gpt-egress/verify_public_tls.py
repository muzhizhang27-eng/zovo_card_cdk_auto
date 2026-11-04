#!/usr/bin/env python3
"""Verify the public TLS handshake through loopback; never send HTTP/session data."""
import json
import socket
import ssl
import time

started = time.monotonic()
with socket.create_connection(("127.0.0.1", 18785), timeout=20) as connection:
    connection.settimeout(20)
    connection.sendall(b"CONNECT chatgpt.com:443 HTTP/1.1\r\nHost: chatgpt.com:443\r\n\r\n")
    header = b""
    while not header.endswith(b"\r\n\r\n"):
        part = connection.recv(1)
        if not part or len(header) > 16384:
            raise RuntimeError("Incomplete CONNECT response")
        header += part
    if not header.startswith(b"HTTP/1.1 200 "):
        raise RuntimeError("CONNECT failed")
    with ssl.create_default_context().wrap_socket(connection, server_hostname="chatgpt.com") as tls:
        print(json.dumps({"host": "chatgpt.com", "tls": tls.version(), "certificateVerified": True,
                          "applicationRequestSent": False, "seconds": round(time.monotonic() - started, 2)}))
