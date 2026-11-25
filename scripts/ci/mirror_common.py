"""Target configuration and file transfer shared by the air-gap mirror scripts.

One configuration drives every script, from flags or environment:

  --oci-registry     MIRROR_REGISTRY      host[/prefix] of any OCI registry
  --files-url        MIRROR_FILES_URL     base of an HTTP file server (PUT), s3://bucket/prefix
                                          (needs boto3, optional), or a local directory
  --rpm-url          MIRROR_RPM_URL       base of the RPM repository (same forms)
  --provider-mirror  MIRROR_PROVIDER_URL  where the OpenTofu network mirror tree goes
                                          (default: <files-url>/providers)
  credentials        MIRROR_USER / MIRROR_PASSWORD
  CA bundle          MIRROR_CA_CERT       path of a PEM file

Every uploaded file gets a `<file>.sha256` sidecar, written after the file; an upload is
skipped when the sidecar already holds the digest of the local file.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry

SIDECAR = ".sha256"
UPLOAD_TIMEOUT = 600


def add_target_args(parser: argparse.ArgumentParser, *, oci: bool = False, files: bool = False,
                    rpm: bool = False, providers: bool = False) -> None:
    group = parser.add_argument_group("mirror target (flags, or the MIRROR_* environment)")
    if oci:
        group.add_argument("--oci-registry", default=os.environ.get("MIRROR_REGISTRY", ""),
                           help="OCI registry, host[/prefix] (env MIRROR_REGISTRY)")
    if files:
        group.add_argument("--files-url", default=os.environ.get("MIRROR_FILES_URL", ""),
                           help="HTTP file server base URL, s3://bucket/prefix (boto3 needed) "
                                "or a directory (env MIRROR_FILES_URL)")
    if rpm:
        group.add_argument("--rpm-url", default=os.environ.get("MIRROR_RPM_URL", ""),
                           help="RPM repository base URL, same forms as --files-url "
                                "(env MIRROR_RPM_URL)")
    if providers:
        group.add_argument("--provider-mirror", default=os.environ.get("MIRROR_PROVIDER_URL", ""),
                           help="where the OpenTofu network mirror tree is uploaded "
                                "(default: <files-url>/providers; env MIRROR_PROVIDER_URL)")


def credentials() -> tuple[str, str]:
    return os.environ.get("MIRROR_USER", ""), os.environ.get("MIRROR_PASSWORD", "")


def ca_cert() -> str:
    return os.environ.get("MIRROR_CA_CERT", "")


def sha256_of(path: str | Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def join_url(base: str, *parts: str) -> str:
    segments = [base.rstrip("/")] + [p.strip("/") for p in parts if p.strip("/")]
    return "/".join(segments)


def registry_host(registry: str) -> str:
    return re.sub(r"^https?://", "", registry).split("/", 1)[0]


def registry_ref(registry: str, path: str) -> str:
    """host[/prefix]/path, without scheme."""
    return join_url(re.sub(r"^https?://", "", registry), path)


def source_session() -> requests.Session:
    session = requests.Session()
    retry = Retry(total=3, connect=3, read=3, backoff_factor=2,
                  status_forcelist=(429, 500, 502, 503, 504),
                  allowed_methods=frozenset(["GET", "HEAD"]), raise_on_status=False)
    session.mount("https://", HTTPAdapter(max_retries=retry))
    session.mount("http://", HTTPAdapter(max_retries=retry))
    return session


class Store:
    """Somewhere files go: a relative path under a base."""

    def put(self, rel: str, local: str | Path) -> None:
        raise NotImplementedError

    def read_text(self, rel: str) -> str | None:
        raise NotImplementedError

    def describe(self, rel: str) -> str:
        raise NotImplementedError

    def put_checked(self, rel: str, local: str | Path) -> bool:
        """Upload unless the sidecar matches; True when something was uploaded."""
        digest = sha256_of(local)
        remote = self.read_text(rel + SIDECAR)
        if remote is not None and remote.split()[:1] == [digest]:
            return False
        self.put(rel, local)
        with tempfile.TemporaryDirectory() as tmp:
            sidecar = Path(tmp) / (Path(rel).name + SIDECAR)
            sidecar.write_text(f"{digest}  {Path(rel).name}\n")
            self.put(rel + SIDECAR, sidecar)
        return True


class HttpStore(Store):
    def __init__(self, base: str):
        self.base = base.rstrip("/")
        self.session = requests.Session()
        user, password = credentials()
        if user and password:
            self.session.auth = (user, password)
        if ca_cert():
            self.session.verify = ca_cert()

    def describe(self, rel: str) -> str:
        return join_url(self.base, rel)

    def put(self, rel: str, local: str | Path) -> None:
        with open(local, "rb") as f:
            resp = self.session.put(self.describe(rel), data=f, timeout=UPLOAD_TIMEOUT)
        resp.raise_for_status()

    def read_text(self, rel: str) -> str | None:
        resp = self.session.get(self.describe(rel), timeout=30)
        return resp.text if resp.status_code == 200 else None


class DirStore(Store):
    def __init__(self, base: str):
        self.root = Path(re.sub(r"^file://", "", base))

    def describe(self, rel: str) -> str:
        return str(self.root / rel)

    def put(self, rel: str, local: str | Path) -> None:
        dest = self.root / rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(local, dest)

    def read_text(self, rel: str) -> str | None:
        path = self.root / rel
        return path.read_text() if path.is_file() else None


class S3Store(Store):
    def __init__(self, base: str):
        try:
            import boto3  # noqa: PLC0415 (optional dependency)
        except ImportError:
            raise SystemExit("ERROR: s3:// targets need boto3 (pip install boto3); it is optional "
                             "and only imported for s3:// URLs") from None
        match = re.match(r"^s3://([^/]+)/?(.*)$", base)
        if not match:
            raise SystemExit(f"ERROR: invalid S3 URL {base!r}, want s3://bucket/prefix")
        self.bucket, self.prefix = match.group(1), match.group(2).strip("/")
        kwargs = {}
        if os.environ.get("MIRROR_S3_ENDPOINT"):
            kwargs["endpoint_url"] = os.environ["MIRROR_S3_ENDPOINT"]
        if ca_cert():
            kwargs["verify"] = ca_cert()
        user, password = credentials()
        if user and password:
            kwargs["aws_access_key_id"], kwargs["aws_secret_access_key"] = user, password
        self.client = boto3.client("s3", **kwargs)

    def _key(self, rel: str) -> str:
        return "/".join(p for p in (self.prefix, rel.strip("/")) if p)

    def describe(self, rel: str) -> str:
        return f"s3://{self.bucket}/{self._key(rel)}"

    def put(self, rel: str, local: str | Path) -> None:
        self.client.upload_file(str(local), self.bucket, self._key(rel))

    def read_text(self, rel: str) -> str | None:
        try:
            obj = self.client.get_object(Bucket=self.bucket, Key=self._key(rel))
        except self.client.exceptions.NoSuchKey:
            return None
        return obj["Body"].read().decode()


def store_for(url: str) -> Store:
    if url.startswith("s3://"):
        return S3Store(url)
    if re.match(r"^https?://", url):
        return HttpStore(url)
    return DirStore(url)


def sub_store(url: str, sub: str) -> Store:
    return store_for(join_url(url, sub))


# OCI tooling -------------------------------------------------------------------------------

_CERT_DIRS: dict[str, str] = {}


def cert_dir() -> str:
    """A directory holding MIRROR_CA_CERT as ca.crt, the layout skopeo's --*-cert-dir reads."""
    ca = ca_cert()
    if ca not in _CERT_DIRS:
        directory = Path(tempfile.mkdtemp(prefix="mirror-ca-"))
        shutil.copyfile(ca, directory / "ca.crt")
        _CERT_DIRS[ca] = str(directory)
    return _CERT_DIRS[ca]


def dest_cert_args() -> list[str]:
    return ["--dest-cert-dir", cert_dir()] if ca_cert() else []


def dest_creds_args() -> list[str]:
    user, password = credentials()
    return ["--dest-creds", f"{user}:{password}"] if user and password else []


def helm_login(registry: str) -> None:
    """helm registry login with the password on stdin; no-op without credentials."""
    user, password = credentials()
    if not (user and password):
        return
    cmd = ["helm", "registry", "login", registry_host(registry), "--username", user,
           "--password-stdin"]
    if ca_cert():
        cmd += ["--ca-file", ca_cert()]
    subprocess.run(cmd, input=password, text=True, check=True, capture_output=True)


def helm_tls_args() -> list[str]:
    return ["--ca-file", ca_cert()] if ca_cert() else []


def die(message: str) -> None:
    print(f"ERROR: {message}", file=sys.stderr)
    sys.exit(1)
