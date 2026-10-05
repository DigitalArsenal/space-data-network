#!/usr/bin/env python3
"""Encrypted fields in format-4 streams, read with NO FlatSQL or SDN code.

For every P/<TYPE>/*.fsdata under each format-4 root given (the dir holding
fsql4/P), using only the stock `flatbuffers` Python package, the SDS generated
Python readers (spacedatastandards.org lib/py, incl. the generated format-3
FlatbuffersEncryption) and `cryptography` for X25519/HKDF/AES-CTR:

  1. walk the file from byte 0 to EOF as [u32 LE size][bytes] frames;
  2. a FlatBuffer frame: parse it with its SDS root type; for a record of the
     manifest, read each encrypted field with the stock accessor: it must be
     the ciphertext the client sent and not the plaintext; decrypt it with the
     throwaway key (format-3 records through the generated DecryptBuffer /
     crypt_buffer, ecies fields through X25519 + HKDF + AES-CTR) and compare
     with the plaintext;
  3. an SDF1 frame (SDN's at-rest seal, SDN scope): parse its envelope ($ENC,
     $KMF) and the record it carries with the stock readers (the record is a
     valid FlatBuffer whose sealed field holds a second layer of ciphertext),
     open it with the store's throwaway key, check the record SDN was given,
     then decrypt the client's layer;
  4. every index row (off, len, cid) of the feed's .db names a frame whose
     sha256 is the row's CID (sealed frames: the CID names the plaintext).

Usage: enc-reader-check.py --manifest M --keys K --sds-py LIBPY [--json OUT] ROOT...

Part of format4proof (encfields.go, DriveEncryptedFields), which writes the
manifest and the throwaway keys and runs this on the format-4 and migrated
stores.
"""
import argparse, hashlib, hmac, importlib, json, os, re, sqlite3, struct, sys

import flatbuffers
from flatbuffers import table as fbtable
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.kdf.hkdf import HKDF
from cryptography.hazmat.primitives import hashes

SDS_PY = None  # --sds-py: spacedatastandards.org lib/py
STORE_CTX = b"space-data-network/storage/field-encryption/v1"
B32 = "abcdefghijklmnopqrstuvwxyz234567"


def cid_groups(digest):
    b = bytes([0x01, 0x55, 0x12, 0x20]) + digest + b"\x00"
    acc, nb, at, g = 0, 0, 0, []
    for _ in range(58):
        while nb < 5:
            acc = (acc << 8) | b[at]; at += 1; nb += 8
        nb -= 5
        g.append((acc >> nb) & 31)
    return g


def cid_text(digest):
    return "b" + "".join(B32[v] for v in cid_groups(digest))


def cid_index_key(digest):
    g = cid_groups(digest)
    bits = [(g[6] & 7, 3)] + [((g[i] + 6) & 31, 5) for i in range(7, 57)] + [(((g[57] >> 2) + 1) & 7, 3)]
    v = 0
    for val, w in bits:
        v = (v << w) | val
    return v.to_bytes(32, "big")


_SDS_CACHE = {}


def load_sds(code):
    """The SDS generated module for a type. Readers that import their siblings
    by bare name need the type's own directory first on sys.path; readers that
    import relatively (KMF: .FlatbuffersEncryption) load as <code>.<code>
    under the lib/py root (and their directory must NOT be on sys.path, or the
    plain module would shadow the package)."""
    if code in _SDS_CACHE:
        return _SDS_CACHE[code]
    tdir = os.path.join(SDS_PY, code)
    for k in [k for k, m in list(sys.modules.items()) if getattr(m, "__file__", None) and str(m.__file__).startswith(SDS_PY)]:
        del sys.modules[k]
    relative = "from ." in open(os.path.join(tdir, code + ".py")).read()
    if relative:
        sys.path.insert(0, SDS_PY)
        try:
            mod = importlib.import_module(code + "." + code)
        finally:
            sys.path.remove(SDS_PY)
    else:
        sys.path.insert(0, tdir)
        try:
            mod = importlib.import_module(code)
        finally:
            sys.path.remove(tdir)
    _SDS_CACHE[code] = mod
    return mod


def hkdf(secret, info, n=32):
    return HKDF(algorithm=hashes.SHA256(), length=n, salt=None, info=info).derive(secret)


def ctr(key, iv, data):
    c = Cipher(algorithms.AES(key), modes.CTR(iv)).encryptor()
    return c.update(data) + c.finalize()


def field_xor(master, fid, data, rec=0):
    """flatbuffers format-2 per-field primitive (ecies / encfield)."""
    k = hkdf(master, b"flatbuffers-field" + struct.pack(">HI", fid, rec), 32)
    iv = hkdf(master, b"flatbuffers-iv" + struct.pack(">HI", fid, rec), 16)
    return ctr(k, iv, data)


def x25519(priv, pub):
    return X25519PrivateKey.from_private_bytes(priv).exchange(X25519PublicKey.from_public_bytes(pub))


def tab_of(buf):
    return fbtable.Table(bytearray(buf), flatbuffers.encode.Get(flatbuffers.packer.uoffset, buf, 0))


def vec_field(t, slot):
    o = t.Offset(slot)
    if not o:
        return None
    start = t.Vector(o)
    return bytes(t.Bytes[start:start + t.VectorLen(o)])


def sub_table(t, slot):
    o = t.Offset(slot)
    if not o:
        return None
    return fbtable.Table(t.Bytes, t.Indirect(o + t.Pos))


def scalar_field(t, slot, fmt):
    o = t.Offset(slot)
    if not o:
        return None
    return struct.unpack_from("<" + fmt, t.Bytes, t.Pos + o)[0]


# The fields the manifest names, as (vtable slot, kind).
SLOTS = {
    ("KMF.fbs", "KEY_BYTES"): (12, "vec"),
    ("OMM.fbs", "NORAD_CAT_ID"): (58, "I"), ("OMM.fbs", "MEAN_MOTION"): (30, "d"),
    ("OMM.fbs", "OBJECT_ID"): (12, "vec"), ("OMM.fbs", "OBJECT_NAME"): (10, "vec"), ("OMM.fbs", "EPOCH"): (26, "vec"),
    ("MPE.fbs", "ENTITY_ID"): (4, "vec"), ("MPE.fbs", "EPOCH"): (6, "d"),
    ("LGR.fbs", "WRAPPED_CONTENT_KEY_PAYLOAD"): (38, "vec"),
    ("LGR.fbs", "WRAPPED_CONTENT_KEY_PAYLOAD.KMF.KEY_BYTES"): (38, "vec"),
}


def read_field(typ, name, buf):
    slot, kind = SLOTS[(typ, name)]
    t = tab_of(buf)
    if kind == "vec":
        v = vec_field(t, slot)
        if name.endswith(".KMF.KEY_BYTES"):
            v = vec_field(tab_of(v), 12)
        return v
    v = scalar_field(t, slot, kind)
    return struct.pack("<" + kind, v) if v is not None else None


def fb3_decrypt(code, buf, key, rec_idx, program):
    """The generated format-3 API: KMF.DecryptBuffer for KMF, the generated
    crypt_buffer with the client's walk program for the schema variants."""
    kmf = load_sds("KMF")
    if code == "KMF":
        return bytes(kmf.KMF.DecryptBuffer(bytearray(buf), key, rec_idx))
    # the generated helper KMF.py imports (from .FlatbuffersEncryption import ...)
    return bytes(kmf.FlatbuffersEncryption.crypt_buffer(bytearray(buf), key, rec_idx, tuple(program)))


def ecies_open(lgr_buf, priv, field):
    t = tab_of(lgr_buf)
    enc = sub_table(t, 36)  # WRAPPED_CONTENT_KEY_HEADER
    eph = vec_field(enc, 12)
    ctx = vec_field(enc, 18) or b""
    master = hkdf(x25519(priv, eph), ctx)
    ct = read_field("LGR.fbs", field["name"], lgr_buf)
    return field_xor(master, field["field_id"], ct)


def sdf1_parse(frame):
    assert frame[:4] == b"SDF1" and frame[4] == 1
    tag = frame[5:21]
    at = 21
    n = struct.unpack_from("<I", frame, at)[0]; enc = frame[at + 4:at + 4 + n]; at += 4 + n
    n = struct.unpack_from("<I", frame, at)[0]; kmf = frame[at + 4:at + 4 + n]; at += 4 + n
    return tag, enc, kmf, frame[at:]


def sdf1_open(frame, store_priv, code, fields):
    tag, enc, kmf, rec = sdf1_parse(frame)
    et = tab_of(enc)
    eph = vec_field(et, 12)
    ctx = vec_field(et, 18) or STORE_CTX
    master = hkdf(x25519(store_priv, eph), ctx)
    seed = field_xor(master, 4, vec_field(tab_of(kmf), 12))
    if hmac.new(seed, b"encfield/seed-verify/v1", hashlib.sha256).digest()[:16] != tag:
        raise ValueError("seed tag does not verify")
    out = bytearray(rec)
    t = tab_of(rec)
    for fid in fields:
        o = t.Offset(4 + 2 * fid)
        if not o:
            continue
        start = t.Vector(o); n = t.VectorLen(o)
        out[start:start + n] = field_xor(seed, fid, bytes(rec[start:start + n]))
    return enc, kmf, bytes(rec), bytes(out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("roots", nargs="+")
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--keys", required=True)
    ap.add_argument("--json")
    ap.add_argument("--sds-py", default=os.environ.get("P4PROOF_SDS_PY"), help="spacedatastandards.org lib/py")
    a = ap.parse_args()
    global SDS_PY
    SDS_PY = a.sds_py
    if not SDS_PY or not os.path.isdir(SDS_PY):
        ap.error("--sds-py (or P4PROOF_SDS_PY) must name spacedatastandards.org lib/py")
    keys = {k: bytes.fromhex(v) for k, v in json.load(open(a.keys)).items() if k.endswith("_hex")}
    man = {}
    for r in json.load(open(a.manifest)):
        r["bytes"] = __import__("base64").b64decode(r["bytes"])
        for f in r.get("fields") or []:
            f["plain"] = __import__("base64").b64decode(f["plain"]); f["cipher"] = __import__("base64").b64decode(f["cipher"])
        man[r["cid"]] = r
    report, ok = [], True
    for root in a.roots:
        pdir = os.path.join(root, "fsql4", "P")
        for code in sorted(os.listdir(pdir)):
            mod = load_sds(code)
            cls = getattr(mod, code)
            ident = ("$" + code).encode()
            for name in sorted(os.listdir(os.path.join(pdir, code))):
                if not name.endswith(".fsdata"):
                    continue
                path = os.path.join(pdir, code, name)
                data = open(path, "rb").read()
                st = {"stream": os.path.relpath(path, root), "bytes": len(data), "frames": 0, "flatbuffer_frames": 0,
                      "parsed_as_root_type": 0, "sealed_sdf1": 0, "sealed_sdfn": 0, "sealed_record_parsed_as_root_type": 0,
                      "sealed_opened_with_store_key": 0, "manifest_records": 0, "encrypted_fields_still_ciphertext": 0,
                      "decrypted_to_plaintext": 0, "index_rows": 0, "index_rows_cid_ok": 0, "problems": []}
                frames, at = {}, 0
                while at < len(data):
                    if len(data) - at < 4:
                        st["problems"].append(f"torn size at {at}"); break
                    n = struct.unpack_from("<I", data, at)[0]
                    if len(data) - at - 4 < n:
                        st["problems"].append(f"frame at {at} runs past EOF"); break
                    fr = data[at + 4:at + 4 + n]
                    st["frames"] += 1
                    try:
                        if fr[:4] == b"SDFN":
                            st["sealed_sdfn"] += 1
                            frames[at] = (n, None)  # SDN never writes SDFN
                        elif fr[:4] == b"SDF1":
                            st["sealed_sdf1"] += 1
                            enc, kmf, rec, opened = sdf1_open(fr, keys["store_x25519_priv_hex"], code, [4] if code == "KMF" else [4, 5])
                            # the envelope's parts parse with the stock readers
                            load_sds("ENC").ENC.GetRootAs(enc, 0).CONTEXT()
                            km = load_sds("KMF").KMF.GetRootAs(kmf, 0); km.KEY_BYTESLength()
                            r = cls.GetRootAs(rec, 0)
                            if rec[4:8] == ident:
                                st["sealed_record_parsed_as_root_type"] += 1
                            st["sealed_opened_with_store_key"] += 1
                            cid = cid_text(hashlib.sha256(opened).digest())
                            frames[at] = (n, hashlib.sha256(opened).digest())
                            w = man.get(cid)
                            if w is None or w["bytes"] != opened:
                                st["problems"].append(f"sealed frame at {at} opens to a record not in the manifest")
                            else:
                                st["manifest_records"] += 1
                                still = all(read_field(w["type"], f["name"], rec) not in (f["cipher"], f["plain"]) for f in w["fields"]
                                            if (w["type"], f["name"]) in SLOTS) if w["fields"] else True
                                if still:
                                    st["encrypted_fields_still_ciphertext"] += 1
                                if w.get("key") == "kmf":
                                    plain = fb3_decrypt("KMF", opened, keys["kmf_key_hex"], w["rec_idx"], w["program"])
                                    if read_field("KMF.fbs", "KEY_BYTES", plain) == w["fields"][0]["plain"]:
                                        st["decrypted_to_plaintext"] += 1
                                    else:
                                        st["problems"].append(f"sealed frame at {at}: KEY_BYTES does not decrypt to the plaintext")
                                elif w.get("key") == "none":
                                    st["decrypted_to_plaintext"] += 1
                        else:
                            st["flatbuffer_frames"] += 1
                            if fr[4:8] != ident:
                                st["problems"].append(f"frame at {at}: identifier {fr[4:8]!r}")
                            r = cls.GetRootAs(fr, 0)
                            st["parsed_as_root_type"] += 1
                            cid = cid_text(hashlib.sha256(fr).digest())
                            frames[at] = (n, hashlib.sha256(fr).digest())
                            w = man.get(cid)
                            if w is not None:
                                st["manifest_records"] += 1
                                if w["bytes"] != fr:
                                    st["problems"].append(f"frame at {at}: other bytes than written")
                                good = True
                                for f in w["fields"]:
                                    v = read_field(w["type"], f["name"], fr)
                                    if v != f["cipher"] or v == f["plain"]:
                                        good = False
                                        st["problems"].append(f"frame at {at}: {f['name']} is not its ciphertext")
                                if good:
                                    st["encrypted_fields_still_ciphertext"] += 1
                                dec_ok = True
                                if w.get("key") in ("kmf", "field"):
                                    k = keys["kmf_key_hex"] if w["key"] == "kmf" else keys["field_key_hex"]
                                    plain = fb3_decrypt(code, fr, k, w["rec_idx"], w["program"])
                                    for f in w["fields"]:
                                        if read_field(w["type"], f["name"], plain) != f["plain"]:
                                            dec_ok = False
                                elif w.get("key") == "recipient":
                                    for f in w["fields"]:
                                        if ecies_open(fr, keys["recipient_x25519_priv_hex"], f) != f["plain"]:
                                            dec_ok = False
                                if dec_ok:
                                    st["decrypted_to_plaintext"] += 1
                                else:
                                    st["problems"].append(f"frame at {at}: does not decrypt to the plaintext")
                    except Exception as e:  # a frame a stock reader cannot read
                        st["problems"].append(f"frame at {at}: {type(e).__name__}: {e}")
                    at += 4 + n
                st["ends_at_eof"] = at == len(data)
                db = re.sub(r"(\.\d+)?\.fsdata$", ".db", path)
                if os.path.exists(db):
                    p = db.replace("%", "%25").replace("?", "%3F").replace("#", "%23")
                    con = sqlite3.connect(f"file:{p}?mode=ro&immutable=1", uri=True)
                    try:
                        for off, ln, cid in con.execute("SELECT off, len, cid FROM r"):
                            st["index_rows"] += 1
                            fr = frames.get(off)
                            # a sealed frame's CID names the plaintext it opens to
                            if fr and fr[0] == ln and fr[1] is not None and cid_index_key(fr[1]) == bytes(cid):
                                st["index_rows_cid_ok"] += 1
                            else:
                                st["problems"].append(f"index row off={off} len={ln}: no frame with its CID")
                    finally:
                        con.close()
                good = (not st["problems"]) and st["ends_at_eof"] and st["index_rows"] == st["index_rows_cid_ok"]
                ok = ok and good
                report.append(st)
                print(("PASS " if good else "FAIL ") + json.dumps({k: v for k, v in st.items() if k != "problems"}))
                for p in st["problems"][:5]:
                    print("    problem:", p)
    summary = {"flatbuffers_python": getattr(flatbuffers, "__version__", "?"), "streams": len(report),
               "frames": sum(r["frames"] for r in report), "flatbuffer_frames": sum(r["flatbuffer_frames"] for r in report),
               "parsed_as_root_type": sum(r["parsed_as_root_type"] for r in report),
               "sealed_sdf1": sum(r["sealed_sdf1"] for r in report), "sealed_sdfn": sum(r["sealed_sdfn"] for r in report),
               "sealed_record_parsed_as_root_type": sum(r["sealed_record_parsed_as_root_type"] for r in report),
               "manifest_records": sum(r["manifest_records"] for r in report),
               "encrypted_fields_still_ciphertext": sum(r["encrypted_fields_still_ciphertext"] for r in report),
               "decrypted_to_plaintext": sum(r["decrypted_to_plaintext"] for r in report),
               "index_rows": sum(r["index_rows"] for r in report), "index_rows_cid_ok": sum(r["index_rows_cid_ok"] for r in report),
               "all_pass": ok}
    print(json.dumps(summary))
    if a.json:
        json.dump({"summary": summary, "streams": report}, open(a.json, "w"), indent=1)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
