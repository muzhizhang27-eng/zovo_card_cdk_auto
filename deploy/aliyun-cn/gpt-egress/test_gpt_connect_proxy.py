import importlib.util
import pathlib
import unittest
from unittest.mock import Mock, patch

path = pathlib.Path(__file__).parent / "gpt-connect-proxy.py"
spec = importlib.util.spec_from_file_location("gpt_connect_proxy", path)
proxy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proxy)


class TargetRestrictionTest(unittest.TestCase):
    def test_only_exact_chatgpt_tls_target_is_allowed(self):
        self.assertEqual(proxy.ALLOWED_TARGETS, {"chatgpt.com:443"})
        for protocol in ("HTTP/1.0", "HTTP/1.1"):
            with self.subTest(protocol=protocol):
                self.assertEqual(
                    proxy.parse_target(f"CONNECT chatgpt.com:443 {protocol}\r\n".encode()),
                    "chatgpt.com:443",
                )

    def test_arbitrary_and_deceptive_targets_are_rejected(self):
        for target in (
            "example.com:443",
            "oauth2.googleapis.com:443",
            "sandbox.chatgpt.com:443",
            "127.0.0.1:3306",
            "169.254.169.254:80",
            "[::1]:443",
            "43.156.235.208:443",
            "chatgpt.com.attacker.invalid:443",
            "attacker@chatgpt.com:443",
            "chatgpt.com.:443",
            "https://chatgpt.com:443",
            "chatgpt.com:443/path",
            "chatgpt.com:443?target=example.com",
            "chatgpt.com:443#fragment",
        ):
            with self.subTest(target=target):
                self.assertIsNone(proxy.parse_target(f"CONNECT {target} HTTP/1.1\r\n".encode()))

    def test_wrong_ports_methods_and_malformed_requests_are_rejected(self):
        requests = [
            b"CONNECT chatgpt.com:80 HTTP/1.1\r\n",
            b"CONNECT chatgpt.com:8443 HTTP/1.1\r\n",
            b"CONNECT chatgpt.com:0443 HTTP/1.1\r\n",
            b"CONNECT chatgpt.com HTTP/1.1\r\n",
            b"GET https://chatgpt.com/ HTTP/1.1\r\n",
            b"POST chatgpt.com:443 HTTP/1.1\r\n",
            b"connect chatgpt.com:443 HTTP/1.1\r\n",
            b"CONNECT chatgpt.com:443 HTTP/2\r\n",
            b"CONNECT  chatgpt.com:443 HTTP/1.1\r\n",
            b" CONNECT chatgpt.com:443 HTTP/1.1\r\n",
            b"CONNECT\tchatgpt.com:443 HTTP/1.1\r\n",
            b"CONNECT chatgpt.com:443 HTTP/1.1 extra\r\n",
            b"CONNECT chatgpt.com:443 HTTP/1.1\x00\r\n",
            b"\xff\xff",
            b"",
        ]
        for request in requests:
            with self.subTest(request=request):
                self.assertIsNone(proxy.parse_target(request))

    def test_rejected_request_never_connects_upstream(self):
        request = Mock()
        request.recv.return_value = b"CONNECT 127.0.0.1:3306 HTTP/1.1\r\n\r\n"
        with patch.object(proxy.socket, "create_connection") as connect:
            proxy.Handler(request, ("127.0.0.1", 12345), Mock())
        connect.assert_not_called()
        request.sendall.assert_called_once_with(
            b"HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n"
        )

    def test_upstream_failure_has_no_alternate_target_or_retry(self):
        request = Mock()
        request.recv.return_value = b"CONNECT chatgpt.com:443 HTTP/1.1\r\n\r\n"
        with patch.object(proxy.socket, "create_connection", side_effect=OSError) as connect:
            proxy.Handler(request, ("127.0.0.1", 12345), Mock())
        connect.assert_called_once_with(("chatgpt.com", 443), timeout=10)
        request.sendall.assert_called_once_with(
            b"HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"
        )

    def test_listener_is_loopback_only(self):
        self.assertEqual(proxy.LISTEN_ADDRESS, ("127.0.0.1", 18785))


if __name__ == "__main__":
    unittest.main()
