#!/usr/bin/python3
"""Emit only the current cdk-admin.lanting.live origin certificate for a forced SSH command."""
import base64
import json
from pathlib import Path
import sys


def main():
    if len(sys.argv) != 1:
        raise RuntimeError("arguments forbidden")
    base = Path("/etc/letsencrypt/live/cdk-admin.lanting.live")
    result = {}
    for name, maximum in (("fullchain.pem", 32768), ("privkey.pem", 16384)):
        with (base / name).open("rb") as source:
            value = source.read(maximum + 1)
        if not value or len(value) > maximum:
            raise RuntimeError("certificate size invalid")
        result[name] = base64.b64encode(value).decode("ascii")
    sys.stdout.write(json.dumps(result, separators=(",", ":")) + "\n")


if __name__ == "__main__":
    try:
        main()
    except Exception:
        sys.stderr.write("cdk-admin TLS export failed\n")
        sys.exit(1)
