#!/usr/bin/env python3
"""aesgcm_bundle.py — D-CERT card 2 cipher helper (host side).

Packs (fullchain.pem, privkey.pem) into one combined PEM stream and encrypts
it AES-256-GCM. The `cryptography` package is present on this host's
/usr/bin/python3, so the GCM path is the real path — no CBC downgrade is
implemented here.

Envelope — MUST match the dot-side decryptor
    (internal/feature/web/certfetch.go decryptBundle, card 1):
        base64( nonce[12] || AES-256-GCM( combined cert+key PEM ) )
    AES key = SHA-256( passphrase ), where the passphrase is the 64-char
    lowercase hex string of the 32-byte shared key. The dot holds that hex
    string in its `intercom.cert_key` setting; both sides therefore derive
    the identical AES key from the same passphrase. Nonce travels in the
    clear; GCM's tag authenticates.
Decrypt refuses tampered input (GCM tag).

Usage:
  aesgcm_bundle.py encrypt  <certdir> <keyfile> <out.bin>
  aesgcm_bundle.py decrypt  <in.bin> <keyfile> <outdir>
  aesgcm_bundle.py selftest
"""
import sys
import os
import io
import re
import hashlib
import base64
from datetime import datetime, timezone

try:
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
except ImportError:  # pragma: no cover
    AESGCM = None


def _require_aesgcm() -> None:
    """Raise after main()'s guard so Pyright sees AESGCM as non-None (it may be None at import if the package is absent)."""
    if AESGCM is None:
        raise SystemExit("cryptography package not importable — install python3-cryptography")


def _load_key(path: str) -> bytes:
    """Load the shared 32-byte key material (hex digits or raw)."""
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


def _passphrase(keyfile: str) -> bytes:
    """The dot's decryptBundle builds its AES key as sha256 of the cert_key
    passphrase string, which card 3 stages as the keyfile's lowercase hex.
    Accept either form so the helper works with either keyfile style."""
    with open(keyfile, "rb") as f:
        raw = f.read().strip()
    if len(raw) == 64:
        try:
            int(raw.decode(), 16)
            return raw  # already hex digits
        except ValueError:
            pass
    if len(raw) == 32:
        return raw.hex().encode()
    raise SystemExit(f"key file must contain 32 raw bytes or 64 hex chars, got {len(raw)}")


def _derive_aes_key(keyfile: str) -> bytes:
    return hashlib.sha256(_passphrase(keyfile)).digest()


def _combined_pem(certdir: str) -> bytes:
    """Concatenate fullchain.pem + privkey.pem into one PEM stream — the exact
    plaintext the dot's decryptBundle expects (it PEM-decodes block by block,
    no tar)."""
    out = io.BytesIO()
    for name in ("fullchain.pem", "privkey.pem"):
        with open(os.path.join(certdir, name), "rb") as f:
            out.write(f.read())
    return out.getvalue()


def cmd_encrypt(certdir: str, keyfile: str, out: str) -> None:
    import hashlib as _h  # noqa: F401  (local alias, keeps Pyright narrow)
    if AESGCM is None:
        raise SystemExit("cryptography package not importable")
    key = _derive_aes_key(keyfile)
    plaintext = _combined_pem(certdir)
    nonce = os.urandom(12)
    ct = AESGCM(key).encrypt(nonce, plaintext, None)  # dot side passes nil AAD
    blob = base64.b64encode(nonce + ct)
    with open(out, "wb") as f:
        f.write(blob + b"\n")
    os.chmod(out, 0o600)
    digest = hashlib.sha256(open(out, "rb").read()).hexdigest()
    print(f"bundle written: {out} sha256={digest}")


def _split_pem(pem_bytes: bytes) -> tuple[bytes, bytes]:
    """Same split the dot's persistCertPair does: CERTIFICATE blocks vs
    PRIVATE KEY blocks."""
    blocks = re.findall(
        rb"-----BEGIN [A-Z ]+?-----\n.*?\n-----END [A-Z ]+?-----\n?", pem_bytes, re.S
    )
    cert = b"".join(b for b in blocks if b"CERTIFICATE" in b)
    priv = b"".join(b for b in blocks if b"PRIVATE KEY" in b)
    return cert, priv


def cmd_decrypt(src: str, keyfile: str, outdir: str) -> None:
    if AESGCM is None:
        raise SystemExit("cryptography package not importable")
    key = _derive_aes_key(keyfile)
    blob = open(src, "rb").read().strip()
    raw = base64.b64decode(blob, validate=True)
    if len(raw) < 12 + 16:
        raise SystemExit("bundle shorter than nonce+ct+tag")
    nonce, ct = raw[:12], raw[12:]
    pt = AESGCM(key).decrypt(nonce, ct, None)  # raises on tamper
    os.makedirs(outdir, exist_ok=True)
    cert, priv = _split_pem(pt)
    for name, data in (("fullchain.pem", cert), ("privkey.pem", priv)):
        with open(os.path.join(outdir, name), "wb") as f:
            f.write(data)
        os.chmod(os.path.join(outdir, name), 0o600)
    print(f"decrypted to {outdir}")


def cmd_selftest() -> None:
    import tempfile
    with tempfile.TemporaryDirectory() as d:
        key = os.urandom(32)
        keyf = os.path.join(d, "k")
        open(keyf, "wb").write(key)  # raw 32 bytes — passphrase path covers this
        certd = os.path.join(d, "certs")
        os.makedirs(certd)
        open(os.path.join(certd, "fullchain.pem"), "w").write(
            "-----BEGIN CERTIFICATE-----\nCA==\n-----END CERTIFICATE-----\n")
        open(os.path.join(certd, "privkey.pem"), "w").write(
            "-----BEGIN PRIVATE KEY-----\nSw==\n-----END PRIVATE KEY-----\n")
        bundle = os.path.join(d, "b.bin")
        cmd_encrypt(certd, keyf, bundle)
        outd = os.path.join(d, "out")
        cmd_decrypt(bundle, keyf, outd)

        def rc(p):
            return open(os.path.join(outd, p)).read()

        assert rc("fullchain.pem") == open(os.path.join(certd, "fullchain.pem")).read()
        assert rc("privkey.pem") == open(os.path.join(certd, "privkey.pem")).read()
        # tamper check
        blob = bytearray(open(bundle, "rb").read().strip())
        blob[-2] ^= 1
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
    print(f"selftest OK (roundtrip + tamper-refused + wrong-key-refused) at {datetime.now(timezone.utc).isoformat()}")


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