#!/usr/bin/env python3
"""Preserve source, preflight SG, then optionally remove exactly 11 obsolete code paths.

Default is read-only. --execute additionally requires --acceptance-complete, which
the migration owner may supply only after CDN/DNS/functional acceptance passes.
No database, backup, proxy, certificate, main-site code, or shared Go cache removal.
"""
import argparse
import datetime
import hashlib
import json
import pathlib
import subprocess
import sys
import tarfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_MANIFEST = pathlib.Path(
    "/Users/nan/Workhub/personal/backups/cdk-migration-20261105/"
    "cdk-migration-source-20261104T165629Z.manifest.json"
)
EXPECTED_MANIFEST_SHA256 = "d352d093087ce0ec475d4be10d5d8636be03a17ced1047ce5fe8252cc7590a19"

# This is transmitted over authenticated SSH stdin, never installed on SG.
REMOTE = r'''
import datetime, json, os, pathlib, shutil, socket, stat, subprocess, sys

CANDIDATES = (
    "/opt/cdk-recharge",
    "/opt/cdk-admin",
    "/opt/cdk-lookup",
    "/home/ubuntu/cdk-recharge-staging",
    "/home/ubuntu/cdk-admin-staging",
    "/home/ubuntu/cdk-cn-build-20261104",
    "/var/tmp/cdk-cn-source-20261104.bundle",
    "/var/tmp/cdk-cn-source-git-20261104",
    "/var/tmp/cdk-sg-compat-20261104",
    "/tmp/cdk-gpt-egress-20261104",
    "/tmp/cdk-sg-maintenance-20261104",
)
OLD_UNITS = ("cdk-recharge.service", "cdk-admin.service", "cdk-lookup.service")
REQUIRED_UNITS = (
    "cdk-gpt-connect.service", "lanting-zovo-connect.service",
    "lanting-google-connect.service", "nginx.service", "certbot.timer",
)
PROTECTED = (
    "/opt/cdk-gpt-egress", "/opt/lanting-zovo-egress", "/opt/lanting-google-egress",
    "/var/lib/cdk-recharge", "/var/lib/cdk-admin", "/var/lib/cdk-tls-export",
    "/var/lib/cdk-admin-tls-export", "/var/lib/cdk-gpt-egress",
    "/etc/letsencrypt", "/var/www/letsencrypt", "/etc/nginx/cdk-private",
    "/usr/local/libexec/cdk-tls-export.py", "/usr/local/libexec/cdk-admin-tls-export.py",
    "/var/backups/cdk-sg-compat", "/var/backups/cdk-gpt-egress",
)

def run(args):
    return subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          universal_newlines=True, timeout=45)

def units(names):
    result = []
    for name in names:
        p = run(["systemctl", "show", name, "-p", "Id", "-p", "LoadState",
                 "-p", "ActiveState", "-p", "MainPID", "-p", "FragmentPath"])
        fields = dict(line.split("=", 1) for line in p.stdout.splitlines() if "=" in line)
        fields["query_exit"] = p.returncode
        result.append(fields)
    return result

def candidate_mentions(value):
    return [p for p in CANDIDATES if p in value]

def process_refs():
    found = []
    for proc in pathlib.Path("/proc").iterdir():
        if not proc.name.isdigit() or int(proc.name) == os.getpid():
            continue
        hits = set()
        for name in ("cwd", "exe"):
            try: hits.update(candidate_mentions(os.readlink(str(proc / name))))
            except OSError: pass
        for name in ("cmdline", "maps"):
            try: hits.update(candidate_mentions((proc / name).read_bytes().decode("utf-8", "replace")))
            except OSError: pass
        try:
            for fd in (proc / "fd").iterdir():
                try: hits.update(candidate_mentions(os.readlink(str(fd))))
                except OSError: pass
        except OSError: pass
        if hits:
            try: comm = (proc / "comm").read_text().strip()
            except OSError: comm = "exited"
            found.append({"pid": int(proc.name), "comm": comm, "paths": sorted(hits)})
    return found

def config_refs():
    found = []
    for root in ("/etc/systemd/system", "/run/systemd/transient", "/etc/cron.d",
                 "/etc/cron.daily", "/var/spool/cron/crontabs", "/etc/supervisor",
                 "/etc/supervisord.d", "/etc/init.d", "/etc/letsencrypt/renewal-hooks",
                 "/usr/local/libexec", "/usr/local/bin"):
        directory = pathlib.Path(root)
        if not directory.exists(): continue
        for path in directory.rglob("*"):
            if path.is_symlink() or not path.is_file(): continue
            try:
                if path.stat().st_size > 1024 * 1024: continue
                text = path.read_text()
            except (OSError, UnicodeError): continue
            hits = candidate_mentions(text)
            if hits: found.append({"file": str(path), "paths": hits})
    return found

def protected_state():
    return {p: {"exists": os.path.exists(p), "device": os.stat(p).st_dev,
                "inode": os.stat(p).st_ino} if os.path.exists(p) else {"exists": False}
            for p in PROTECTED}

def preflight():
    result = {"observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "hostname": socket.gethostname(), "root": os.geteuid() == 0,
              "old_units": units(OLD_UNITS), "required_units": units(REQUIRED_UNITS),
              "process_references": process_refs(), "configuration_references": config_refs(),
              "protected_paths": protected_state(), "paths": [], "errors": []}
    errors = result["errors"]
    if result["hostname"] != "VM-0-10-ubuntu" or not result["root"]:
        errors.append("Unexpected server identity or insufficient privilege")
    for unit in result["old_units"]:
        if unit.get("LoadState") != "masked" or unit.get("ActiveState") != "inactive" or unit.get("MainPID") != "0":
            errors.append("Old business unit is not masked and inactive: " + unit.get("Id", "unknown"))
    for unit in result["required_units"]:
        if unit.get("ActiveState") != "active":
            errors.append("Required service is not active: " + unit.get("Id", "unknown"))
    if result["process_references"]: errors.append("Active process references candidate path")
    if result["configuration_references"]: errors.append("Installed configuration references candidate path")
    if any(not x["exists"] for x in result["protected_paths"].values()):
        errors.append("Protected runtime path is missing")
    # Capture expanded Nginx config without returning any private request-header values.
    nginx = run(["nginx", "-T"])
    result["nginx_test_passed"] = nginx.returncode == 0
    result["nginx_candidate_references"] = candidate_mentions(nginx.stdout)
    if nginx.returncode != 0: errors.append("nginx -T failed")
    if result["nginx_candidate_references"]: errors.append("Loaded Nginx references candidate path")
    result["compat_vhosts"] = {}
    for host in ("cdk.lanting.live", "cdk-admin.lanting.live"):
        text = pathlib.Path("/etc/nginx/conf.d/" + host + ".conf").read_text()
        ok = "proxy_pass https://121.40.64.63:443;" in text and "root /var/www/letsencrypt;" in text
        result["compat_vhosts"][host] = ok
        if not ok: errors.append("Unexpected compat/ACME vhost: " + host)
    for name in CANDIDATES:
        path = pathlib.Path(name)
        unsafe = [str(p) for p in (path,) + tuple(path.parents) if p.is_symlink()]
        record = {"path": name, "exists": path.exists(), "symlink_components": unsafe}
        if unsafe: errors.append("Symlink candidate or parent: " + name)
        if path.exists():
            st = path.lstat()
            record.update({"device": st.st_dev, "inode": st.st_ino,
                           "directory": stat.S_ISDIR(st.st_mode)})
            if not (stat.S_ISDIR(st.st_mode) or stat.S_ISREG(st.st_mode)):
                errors.append("Unexpected candidate file type: " + name)
            usage = run(["du", "-sx", name])
            record["allocated_kib"] = int(usage.stdout.split()[0]) if usage.returncode == 0 else None
            if usage.returncode: errors.append("Could not measure candidate: " + name)
        result["paths"].append(record)
    result["safe_for_code_cleanup"] = not errors
    return result

before = preflight()
execute = sys.argv[1:] == ["--execute", "--acceptance-complete"]
report = {"mode": "execute" if execute else "preflight", "preflight": before,
          "deleted_paths": [], "already_absent_paths": [], "postflight": None,
          "allocated_kib_removed": 0, "execution_error": None,
          "progress_journal": None, "currently_deleting": None,
          "filesystem_free_bytes_before": shutil.disk_usage("/").free}

def persist_progress():
    if not report["progress_journal"]: return
    destination = pathlib.Path(report["progress_journal"])
    temporary = destination.with_name(destination.name + ".tmp")
    fd = os.open(str(temporary), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as handle:
        json.dump(report, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(str(temporary), str(destination))
    directory_fd = os.open(str(destination.parent), os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(directory_fd)
    finally: os.close(directory_fd)

if sys.argv[1:] and not execute:
    before["errors"].append("Invalid execution flags")
    before["safe_for_code_cleanup"] = False
if execute and before["safe_for_code_cleanup"]:
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    journal = pathlib.Path("/var/backups/cdk-sg-compat/cleanup-" + stamp + "-" + str(os.getpid()) + ".json")
    if journal.exists(): raise RuntimeError("Cleanup journal already exists")
    report["progress_journal"] = str(journal)
    persist_progress()
    try:
        for item in before["paths"]:
            path = pathlib.Path(item["path"])
            if not item["exists"]:
                report["already_absent_paths"].append(str(path)); continue
            current = path.lstat()
            if any(p.is_symlink() for p in (path,) + tuple(path.parents)):
                raise RuntimeError("Candidate or parent became a symlink: " + str(path))
            if current.st_dev != item["device"] or current.st_ino != item["inode"]:
                raise RuntimeError("Candidate identity changed before deletion: " + str(path))
            report["currently_deleting"] = str(path)
            persist_progress()
            if item["directory"]: shutil.rmtree(str(path))
            else: path.unlink()
            report["deleted_paths"].append(str(path))
            report["allocated_kib_removed"] += item["allocated_kib"]
            report["currently_deleting"] = None
            persist_progress()
    except (OSError, RuntimeError) as exc:
        report["execution_error"] = str(exc)
        persist_progress()
    after = preflight()
    if report["execution_error"]:
        after["errors"].append("Partial cleanup failed; inspect deleted_paths and execution_error")
    if before["protected_paths"] != after["protected_paths"]:
        after["errors"].append("Protected path identity changed")
    if any(p["exists"] for p in after["paths"]):
        after["errors"].append("Candidate path remains after cleanup")
    after["safe_for_code_cleanup"] = not after["errors"]
    report["postflight"] = after
report["filesystem_free_bytes_after"] = shutil.disk_usage("/").free
persist_progress()
print(json.dumps(report, ensure_ascii=False))
sys.exit(0 if (report["postflight"] or before)["safe_for_code_cleanup"] else 2)
'''

def sha(data):
    return hashlib.sha256(data).hexdigest()

def preservation_check(manifest_path):
    data = manifest_path.read_bytes()
    if sha(data) != EXPECTED_MANIFEST_SHA256:
        raise RuntimeError("Preservation manifest SHA mismatch")
    manifest = json.loads(data)
    entries = {x["filename"]: x for x in manifest["artifacts"]}
    artifacts = {}
    for name, entry in entries.items():
        path = manifest_path.parent / name
        if path.parent != manifest_path.parent or sha(path.read_bytes()) != entry["sha256"]:
            raise RuntimeError("Preservation artifact mismatch: " + name)
        artifacts[name] = entry["sha256"]
    archive = manifest_path.parent / next(n for n in entries if n.endswith(".tgz"))
    expected = {x["path"]: x["sha256"] for x in manifest["files"]}
    with tarfile.open(archive) as tf:
        actual = {}
        for member in tf.getmembers():
            if not member.isfile() or not member.name.startswith("zovo_card_cdk_auto/"):
                raise RuntimeError("Unexpected archive member")
            actual[member.name.split("/", 1)[1]] = sha(tf.extractfile(member).read())
    if actual != expected:
        raise RuntimeError("Preserved worktree differs from per-file manifest")
    # Deployment audit reports can continue changing after this preservation point.
    # Application source and its build inputs must still match the durable snapshot.
    checked = 0
    current_paths = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                                   cwd=ROOT, stdout=subprocess.PIPE, check=True).stdout
    current_core = {x.decode() for x in current_paths.split(b"\0") if x and not x.decode().startswith("deploy/")}
    if current_core != {x for x in expected if not x.startswith("deploy/")}:
        raise RuntimeError("Application source file set changed since preservation")
    for name, digest in expected.items():
        if name.startswith("deploy/"):
            continue
        if sha((ROOT / name).read_bytes()) != digest:
            raise RuntimeError("Application source changed since preservation: " + name)
        checked += 1
    bundle = manifest_path.parent / next(n for n in entries if n.endswith(".bundle"))
    p = subprocess.run(["git", "bundle", "verify", str(bundle)], cwd=ROOT,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if p.returncode:
        raise RuntimeError("Git history bundle verification failed")
    return {"passed": True, "manifest": str(manifest_path),
            "manifest_sha256": sha(data), "artifacts": artifacts,
            "archive_files_verified": len(expected), "current_source_files_verified": checked,
            "deployment_reports_are_preservation_time_snapshot": True}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--acceptance-complete", action="store_true")
    parser.add_argument("--report", type=pathlib.Path, required=True)
    args = parser.parse_args()
    if args.execute != args.acceptance_complete:
        parser.error("Deletion needs both --execute and --acceptance-complete")
    if args.report.exists():
        parser.error("Report already exists; choose a new path")
    preserved = preservation_check(DEFAULT_MANIFEST)
    remote_command = "sudo -n python3 -"
    if args.execute:
        remote_command += " --execute --acceptance-complete"
    p = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=12",
                        "-J", "aliyun-prod", "sg-prod", remote_command],
                       input=REMOTE.encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                       timeout=180)
    try:
        remote = json.loads(p.stdout)
    except ValueError:
        raise RuntimeError("Remote preflight failed without a structured report; SSH exit=" + str(p.returncode))
    report = {"schema": "cdk-sg-code-cleanup-v1", "recorded_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "source_preservation": preserved, "acceptance_confirmed_by_operator": args.acceptance_complete,
              "singapore": remote, "ssh_exit": p.returncode}
    args.report.parent.mkdir(parents=True, exist_ok=True)
    with args.report.open("x") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print(json.dumps({"report": str(args.report), "mode": remote["mode"],
                      "source_preservation_verified": preserved["passed"],
                      "preflight_passed": remote["preflight"]["safe_for_code_cleanup"],
                      "preflight_errors": remote["preflight"]["errors"],
                      "deleted_paths": remote["deleted_paths"],
                      "postflight_passed": remote["postflight"]["safe_for_code_cleanup"] if remote["postflight"] else None},
                     ensure_ascii=False))
    return p.returncode

if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        sys.exit(1)
