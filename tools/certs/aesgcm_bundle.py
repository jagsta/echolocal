#!/usr/bin/env python3
"""aesgcm_bundle.py — D-CERT card 2 cipher helper (host side).

Packs (fullchain.pem, privkey.pem) into a tar.gz and encrypts it AES-256-GCM
with a shared 32-byte key (D-CERT-1). The `cryptography` package is present on
this host's /usr/bin/python3, so the GCM path is the real path — no CBC
downgrade is implemented here.

Envelope (little-endian, all lengths 4 bytes):
    magic  "ECD1"
    nonce  12 bytes (recorded as u32) || ciphertext || tag, ciphertext as u32 length
    ...    ciphertext || tag
Decrypt refuses tampered input (GCM tag).

Usage:
  aesgcm_bundle.py encrypt  <certdir> <keyfile> <out.bin>
  aesgcm_bundle.py decrypt  <in.bin> <keyfile> <outdir>
  aesgcm_bundle.py selftest
"""
import sys
import os
import io
import tarfile
import struct
from datetime import datetime, timezone

try:
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
except ImportError:  # pragma: no cover
    AESGCM = None

MAGIC = b"ECD1"


def _require_aesgcm() -> None:
    """Raise after main()'s guard so Pyright sees AESGCM as non-None (it may be None at import if the package is absent)."""
    if AESGCM is None:
        raise SystemExit("cryptography package not importable — install python3-cryptography")


def _load_key(path: str) -> bytes:
    with open(path, "rb") as f:
        raw = f.read().strip()
    # accept hex (64 chars) or raw 32 bytes
    if len(raw) == 64:
        try:
            return bytes.fromhex(raw.decode())
        except ValueError:
            pass
    if len(raw) != 32:
        raise SystemExit(f"key file must contain 32 raw bytes or 64 hex chars, got {len(raw)}")
    return raw


def _tar_certdir(certdir: str) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tf:
        for name in ("fullchain.pem", "privkey.pem"):
            tf.add(os.path.join(certdir, name), arcname=name)
    return buf.getvalue()


def cmd_encrypt(certdir: str, keyfile: str, out: str) -> None:
    _require_aesgcm()
    key = _load_key(keyfile)
    plaintext = _tar_certdir(certdir)
    nonce = os.urandom(12)
    ct = AESGCM(key).encrypt(nonce, plaintext, MAGIC)
    with open(out, "wb") as f:
        f.write(MAGIC)
        f.write(struct.pack("<I", len(nonce)))
        f.write(nonce)
        f.write(struct.pack("<I", len(ct)))
        f.write(ct)
    os.chmod(out, 0o600)
    import hashlib
    digest = hashlib.sha256(open(out, "rb").read()).hexdigest()
    print(f"bundle written: {out} sha256={digest}")


def cmd_decrypt(src: str, keyfile: str, outdir: str) -> None:
    _require_aesgcm()
    key = _load_key(keyfile)
    blob = open(src, "rb").read()
    if blob[:4] != MAGIC:
        raise SystemExit("bad magic — not an ECD1 bundle")
    off = 4
    (nlen,) = struct.unpack("<I", blob[off:off + 4]); off += 4
    nonce = blob[off:off + nlen]; off += nlen
    (clen,) = struct.unpack("<I", blob[off:off + 4]); off += 4
    ct = blob[off:off + clen]
    pt = AESGCM(key).decrypt(nonce, ct, MAGIC)  # raises on tamper
    os.makedirs(outdir, exist_ok=True)
    with tarfile.open(fileobj=io.BytesIO(pt), mode="r:gz") as tf:
        for member in tf.getmembers():
            member.mode = 0o600
            tf.extract(member, outdir, filter="data")
    print(f"decrypted to {outdir}")


def cmd_selftest() -> None:
    import tempfile
    with tempfile.TemporaryDirectory() as d:
        key = os.urandom(32)
        keyf = os.path.join(d, "k")
        open(keyf, "wb").write(key)
        certd = os.path.join(d, "certs")
        os.makedirs(certd)
        open(os.path.join(certd, "fullchain.pem"), "w").write("CERTTEST")
        open(os.path.join(certd, "privkey.pem"), "w").write("KEYTEST")
        bundle = os.path.join(d, "b.bin")
        cmd_encrypt(certd, keyf, bundle)
        outd = os.path.join(d, "out")
        cmd_decrypt(bundle, keyf, outd)

        def rc(p):
            return open(os.path.join(outd, p)).read()

        assert rc("fullchain.pem") == "CERTTEST" and rc("privkey.pem") == "KEYTEST"
        # tamper check
        blob = bytearray(open(bundle, "rb").read())
        blob[-1] ^= 1
        bad = os.path.join(d, "bad.bin")
        open(bad, "wb").write(blob)
        try:
            cmd_decrypt(bad, keyf, os.path.join(d, "out2"))
            raise SystemExit("selftest FAILED: tampered bundle decrypted")
        except SystemExit:
            pass
        except Exception as e:
            if "InvalidTag" not in repr(e) and "tag" not in str(e).lower():
                raise
        # wrong key check
        open(keyf, "wb").write(os.urandom(32))
        try:
            cmd_decrypt(bundle, keyf, os.path.join(d, "out3"))
            raise SystemExit("selftest FAILED: wrong key decrypted")
        except SystemExit:
            pass
        except Exception as e:
            if "InvalidTag" not in repr(e) and "tag" not in str(e).lower():
                raise
    nonce_pub = datetime.now(timezone.utc)
    print(f"selftest OK (roundtrip + tamper-refused + wrong-key-refused) at {nonce_pub.isoformat()}")


def main() -> None:
    if AESGCM is None:
        raise SystemExit("cryptography package not importable — install python3-cryptography")
    if len(sys.argv) < 2:
        print(__doc__)
        raise SystemExit(2)
    cmd, args = sys.argv[1], sys.argv[2:]
    if cmd == "selftest" and not args:
        cmd_selftest()
    elif cmd == "encrypt" and len(args) == 3:
        cmd_encrypt(*args)
    elif cmd == "decrypt" and len(args) == 3:
        cmd_decrypt(*args)
    else:
        print(__doc__)
        raise SystemExit(2)


if __name__ == "__main__":
    main()