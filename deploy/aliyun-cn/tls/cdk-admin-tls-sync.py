#!/usr/bin/python3
"""Pull the fixed SG certificate, validate it, and atomically switch one directory."""
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile


ROOT = Path("/etc/cdk-admin-tls")
IDENTITY = Path("/var/lib/cdk-admin-tls-sync")


def run(args, data=None):
    return subprocess.run(args, input=data, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, check=True, timeout=20).stdout


def alarm_handler(_signal, _frame):
    raise RuntimeError("operation timeout")


def validate(directory):
    chain = str(directory / "fullchain.pem")
    key = str(directory / "privkey.pem")
    # A malformed/mismatched/expired certificate must not replace the running one.
    public_cert = run(["openssl", "x509", "-in", chain, "-pubkey", "-noout"])
    public_key = run(["openssl", "pkey", "-in", key, "-pubout"])
    if public_cert != public_key:
        raise RuntimeError("key mismatch")
    run(["openssl", "x509", "-in", chain, "-noout", "-checkend", "86400"])
    san = run(["openssl", "x509", "-in", chain, "-noout", "-ext", "subjectAltName"]).decode("ascii")
    san_entries = [item.strip() for item in "".join(san.splitlines()[1:]).split(",")]
    if san_entries != ["DNS:cdk-admin.lanting.live"]:
        raise RuntimeError("unexpected certificate SAN")
    for hostname in ("cdk-admin.lanting.live",):
        run(["openssl", "verify", "-CAfile", "/etc/pki/tls/certs/ca-bundle.crt",
             "-untrusted", chain, "-purpose", "sslserver", "-verify_hostname",
             hostname, chain])


def switch(target):
    temporary = ROOT / (".current-" + str(os.getpid()))
    try:
        os.symlink(target, str(temporary))
        os.replace(str(temporary), str(ROOT / "current"))
    finally:
        if temporary.is_symlink():
            temporary.unlink()


def main():
    if len(sys.argv) != 1 or os.geteuid() != 0:
        raise RuntimeError("root required, arguments forbidden")
    os.umask(0o077)
    ROOT.mkdir(mode=0o700, exist_ok=True)
    releases = ROOT / "releases"
    releases.mkdir(mode=0o700, exist_ok=True)
    with (IDENTITY / "sync.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        signal.signal(signal.SIGALRM, alarm_handler)
        signal.alarm(100)
        command = ["ssh", "-F", "/dev/null", "-T", "-o", "BatchMode=yes",
                   "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
                   "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no",
                   "-o", "HostKeyAlgorithms=ssh-ed25519", "-o",
                   "UserKnownHostsFile=" + str(IDENTITY / "known_hosts"),
                   "-o", "GlobalKnownHostsFile=/dev/null", "-o", "ForwardAgent=no",
                   "-o", "PasswordAuthentication=no", "-o", "ConnectTimeout=10",
                   "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
                   "-i", str(IDENTITY / "id_ed25519"),
                   "cdk-admin-tls-export@43.156.235.208"]
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        try:
            payload = process.stdout.read(70001)
            if len(payload) > 70000 or process.wait(timeout=10) != 0:
                raise RuntimeError("export failed")
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
        bundle = json.loads(payload.decode("ascii"))
        if set(bundle) != {"fullchain.pem", "privkey.pem"}:
            raise RuntimeError("unexpected export fields")
        stage = Path(tempfile.mkdtemp(prefix=".staging-", dir=str(ROOT)))
        try:
            for name, maximum in (("fullchain.pem", 32768), ("privkey.pem", 16384)):
                value = base64.b64decode(bundle[name], validate=True)
                if not value or len(value) > maximum:
                    raise RuntimeError("invalid certificate size")
                (stage / name).write_bytes(value)
            validate(stage)
            digest = hashlib.sha256((stage / "fullchain.pem").read_bytes()).hexdigest()
            release = releases / digest
            if release.exists():
                if any((release / name).read_bytes() != (stage / name).read_bytes()
                       for name in bundle):
                    raise RuntimeError("existing release mismatch")
            else:
                os.rename(str(stage), str(release))
            target = "releases/" + digest
            current = ROOT / "current"
            previous = os.readlink(str(current)) if current.is_symlink() else None
            if current.exists() and not current.is_symlink():
                raise RuntimeError("current must be a symlink")
            if previous == target:
                print("cdk-admin TLS certificate unchanged")
                return
            switch(target)
            active = subprocess.run(["systemctl", "is-active", "--quiet", "nginx"]).returncode == 0
            try:
                if active:
                    run(["/usr/sbin/nginx", "-t"])
                    run(["systemctl", "reload", "nginx"])
            except Exception:
                if previous is not None:
                    switch(previous)
                    if active:
                        run(["/usr/sbin/nginx", "-t"])
                        run(["systemctl", "reload", "nginx"])
                else:
                    current.unlink()
                raise
            print("cdk-admin TLS certificate installed sha256=" + digest)
        finally:
            if stage.exists():
                shutil.rmtree(str(stage))
            signal.alarm(0)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        sys.stderr.write("cdk-admin TLS sync failed; current certificate preserved\n")
        sys.exit(1)
