import { getWalletOriginCapabilities as Bn } from "hd-wallet-wasm";
const Ni = Object.freeze({
  cm: 0.01,
  km: 1e3,
  m: 1,
  mm: 1e-3
}), Ti = Object.freeze({
  metersPerSourceUnit: Ni,
  quaternionNormTolerance: 1e-6,
  scaleComponentExclusiveMin: 0,
  scaleComponentInclusiveMax: 1e6,
  translationComponentAbsMax: 1e6,
  upAxes: Object.freeze(["X_UP", "Y_UP", "Z_UP"])
}), Ci = Object.freeze({ reviewedTransform: Ti, schemaVersion: 1 }), Se = new TextEncoder(), Vn = Object.getPrototypeOf(Uint8Array.prototype), _i = Object.getOwnPropertyDescriptor(Vn, "buffer").get, xi = Object.getOwnPropertyDescriptor(Vn, "length").get, Di = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength").get, hn = typeof SharedArrayBuffer > "u" ? null : Object.getOwnPropertyDescriptor(SharedArrayBuffer.prototype, "byteLength").get, Ee = "sdn-bip32-slip10-purpose-v1", mt = "password-scrypt-v2", Pe = "ed25519-over-sha256-jcs-v1", yn = "ed25519-raw-32-v1", Mn = "https://review.spacedatanetwork.org", $n = "sdn-asset-review-v1", Pi = "asset-review:assets.ipfs.01", ki = "asset-review-authority:assets.ipfs.01", Vt = "sdn-login:sdn.spaceaware.io", Ui = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, le = /^[0-9a-f]{64}$/u, Fn = /^[0-9a-f]{128}$/u, We = /^sha256:[0-9a-f]{64}$/u, ji = /^[1-9A-HJ-NP-Za-km-z]+$/u, Bi = /^b[a-z2-7]{58}$/u, Mt = "abcdefghijklmnopqrstuvwxyz234567", Wn = 131072, Vi = [
  "accountFingerprint",
  "accountIndex",
  "accountLabel",
  "accountPeerId",
  "accountXpub",
  "identityScheme",
  "keys",
  "schemaVersion",
  "seedProfile"
], Mi = [
  "bip32Fingerprint",
  "curve",
  "derivation",
  "encoding",
  "identityScheme",
  "keyId",
  "path",
  "publicKeyHex",
  "purpose",
  "seedProfile",
  "signatureProfile"
], $i = [
  "connectionExpiresAt",
  "event",
  "identity",
  "schemaVersion"
], Fi = [
  "algorithm",
  "encoding",
  "identityScheme",
  "keyId",
  "schemaVersion",
  "signatureHex",
  "signatureProfile"
], Wi = [
  "algorithm",
  "canonicalEnvelope",
  "encoding",
  "identityScheme",
  "keyId",
  "schemaVersion",
  "signatureHex",
  "signatureProfile",
  "signedDigestSha256"
], gt = [
  "audience",
  "clientId",
  "expiresAt",
  "identityScheme",
  "issuedAt",
  "keyId",
  "nonce",
  "protocolVersion",
  "publicKeyHex",
  "purpose",
  "requestOrigin",
  "serviceInstance",
  "signatureProfile"
], $t = [
  "audience",
  "candidateKey",
  "challengeId",
  "clientId",
  "decision",
  "expiresAt",
  "issuedAt",
  "metadataSha256",
  "modelBytes",
  "modelCid",
  "modelSha256",
  "nonce",
  "previousDecisionHead",
  "protocolVersion",
  "requestOrigin"
], zi = [
  "metersPerSourceUnit",
  "rotation",
  "scale",
  "sourceUnits",
  "translation",
  "upAxis"
];
function y(e) {
  throw new TypeError(e);
}
function Ot(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function ue(e, t) {
  typeof e != "string" && y(`${t} must be a string`);
  for (let n = 0; n < e.length; n += 1) {
    const i = e.charCodeAt(n);
    let r = i;
    if (i >= 55296 && i <= 56319) {
      const s = e.charCodeAt(n + 1);
      s >= 56320 && s <= 57343 || y(`${t} contains an unpaired surrogate`), r = 65536 + (i - 55296 << 10) + (s - 56320), n += 1;
    } else i >= 56320 && i <= 57343 && y(`${t} contains an unpaired surrogate`);
    ((r & 65535) === 65534 || (r & 65535) === 65535 || r >= 64976 && r <= 65007) && y(`${t} contains a Unicode noncharacter`);
  }
  return e;
}
function zn(e) {
  typeof e != "string" && y("wire JSON must be a string"), Se.encode(e).byteLength > Wn && y("wire JSON is too large"), e.charCodeAt(0) === 65279 && y("wire JSON must not contain a BOM");
  let t = 0, n = 0;
  const i = () => {
    for (; t < e.length && /[\u0009\u000a\u000d\u0020]/u.test(e[t]); ) t += 1;
  }, r = () => {
    e[t] !== '"' && y("invalid JSON string");
    const o = t;
    for (t += 1; t < e.length; ) {
      const c = e.charCodeAt(t);
      if (c === 34) {
        t += 1;
        let l;
        try {
          l = JSON.parse(e.slice(o, t));
        } catch {
          y("invalid JSON string");
        }
        return ue(l, "JSON string");
      }
      c < 32 && y("invalid JSON control character"), c === 92 && (t += 1, t >= e.length && y("unterminated JSON escape")), t += 1;
    }
    y("unterminated JSON string");
  }, s = (o = 0) => {
    o > 32 && y("wire JSON nesting is too deep"), n += 1, n > 4096 && y("wire JSON contains too many values"), i();
    const c = e[t];
    if (c === '"') return r();
    if (c === "{") {
      t += 1;
      const u = /* @__PURE__ */ Object.create(null), f = /* @__PURE__ */ new Set();
      if (i(), e[t] === "}")
        return t += 1, u;
      for (; t < e.length; ) {
        i();
        const p = r();
        if (f.has(p) && y(`duplicate JSON field: ${p}`), f.add(p), i(), e[t] !== ":" && y("missing JSON object colon"), t += 1, u[p] = s(o + 1), i(), e[t] === "}")
          return t += 1, u;
        e[t] !== "," && y("invalid JSON object separator"), t += 1;
      }
      y("unterminated JSON object");
    }
    if (c === "[") {
      t += 1;
      const u = [];
      if (i(), e[t] === "]")
        return t += 1, u;
      for (; t < e.length; ) {
        if (u.push(s(o + 1)), i(), e[t] === "]")
          return t += 1, u;
        e[t] !== "," && y("invalid JSON array separator"), t += 1;
      }
      y("unterminated JSON array");
    }
    for (const [u, f] of [["true", !0], ["false", !1], ["null", null]])
      if (e.startsWith(u, t))
        return t += u.length, f;
    const l = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(e.slice(t));
    l || y("invalid JSON value"), t += l[0].length;
    const d = Number(l[0]);
    return Number.isFinite(d) || y("wire JSON number must be finite"), d;
  };
  i();
  const a = s();
  return i(), t !== e.length && y("wire JSON contains trailing bytes"), a;
}
function qn(e) {
  return typeof e == "string" ? zn(e) : e;
}
function qi(e, t) {
  if (e.byteLength !== t.byteLength) return !1;
  let n = 0;
  for (let i = 0; i < e.byteLength; i += 1) n |= e[i] ^ t[i];
  return n === 0;
}
function ee(e, t, n) {
  const i = qn(e);
  Ot(i) || y(`${n} must be a JSON object`), Object.getOwnPropertySymbols(i).length !== 0 && y(`${n} has an unknown symbol field`);
  const r = Object.getOwnPropertyNames(i).sort(), s = [...t].sort(), a = Se.encode(r.join("\0")), o = Se.encode(s.join("\0"));
  qi(a, o) || y(`${n} has missing or unknown fields`);
  for (const c of r) {
    const l = Object.getOwnPropertyDescriptor(i, c);
    (!l || !l.enumerable || !("value" in l)) && y(`${n}.${c} must be an enumerable data field`), l.value === void 0 && y(`${n}.${c} must not be undefined`);
  }
  return i;
}
function ot(e) {
  if (!e || typeof e != "object" || Object.isFrozen(e)) return e;
  for (const t of Object.values(e)) ot(t);
  return Object.freeze(e);
}
function de(e) {
  return ot(Object.fromEntries([...e].sort(([t], [n]) => t < n ? -1 : t > n ? 1 : 0)));
}
function Jt(e, t, n) {
  (!Array.isArray(e) || Object.getPrototypeOf(e) !== Array.prototype || e.length !== t) && y(`${n} must be a plain array with exactly ${t} values`);
  const i = [...Array.from({ length: t }, (s, a) => String(a)), "length"], r = Reflect.ownKeys(e);
  (r.length !== i.length || r.some((s, a) => s !== i[a])) && y(`${n} must be a dense plain array`);
  for (let s = 0; s < t; s += 1) {
    const a = Object.getOwnPropertyDescriptor(e, String(s));
    (!a || !a.enumerable || !("value" in a) || a.value === void 0) && y(`${n} must contain enumerable data values`);
  }
}
function Hi(e) {
  const t = ee(
    e,
    ["reviewedTransform", "schemaVersion"],
    "asset review protocol policy"
  );
  v(t.schemaVersion, 1, "asset review protocol schemaVersion");
  const n = ee(t.reviewedTransform, [
    "metersPerSourceUnit",
    "quaternionNormTolerance",
    "scaleComponentExclusiveMin",
    "scaleComponentInclusiveMax",
    "translationComponentAbsMax",
    "upAxes"
  ], "asset review transform policy"), i = ee(
    n.metersPerSourceUnit,
    ["cm", "km", "m", "mm"],
    "asset review unit policy"
  );
  for (const [r, s] of Object.entries(i))
    (typeof s != "number" || !Number.isFinite(s) || s <= 0) && y(`asset review unit policy ${r} must be positive and finite`);
  for (const [r, s] of [
    ["quaternionNormTolerance", n.quaternionNormTolerance],
    ["scaleComponentExclusiveMin", n.scaleComponentExclusiveMin],
    ["scaleComponentInclusiveMax", n.scaleComponentInclusiveMax],
    ["translationComponentAbsMax", n.translationComponentAbsMax]
  ])
    (typeof s != "number" || !Number.isFinite(s)) && y(`asset review transform policy ${r} must be finite`);
  return (n.quaternionNormTolerance <= 0 || n.scaleComponentInclusiveMax <= n.scaleComponentExclusiveMin || n.translationComponentAbsMax <= 0) && y("asset review transform policy bounds are inconsistent"), Jt(n.upAxes, 3, "asset review up-axis policy"), n.upAxes.join("\0") !== "X_UP\0Y_UP\0Z_UP" && y("asset review up-axis policy is invalid"), de([
    ["metersPerSourceUnit", de(Object.entries(i))],
    ["quaternionNormTolerance", n.quaternionNormTolerance],
    ["scaleComponentExclusiveMin", n.scaleComponentExclusiveMin],
    ["scaleComponentInclusiveMax", n.scaleComponentInclusiveMax],
    ["translationComponentAbsMax", n.translationComponentAbsMax],
    ["upAxes", Object.freeze(Array.from({ length: 3 }, (r, s) => n.upAxes[s]))]
  ]);
}
const xe = Hi(Ci);
function v(e, t, n) {
  return e !== t && y(`${n} must equal ${JSON.stringify(t)}`), e;
}
function Qt(e, t, n) {
  return t.includes(e) || y(`${n} is not an allowed value`), e;
}
function q(e, t, n) {
  return ue(e, n), t.test(e) || y(`${n} has an invalid encoding`), e;
}
function we(e, t) {
  return ue(e, t), (!Ui.test(e) || new Date(e).toISOString() !== e) && y(`${t} must be exact RFC3339 milliseconds UTC`), e;
}
function Nt(e, t) {
  const n = Date.parse(e), i = Date.parse(t);
  i > n && i - n <= 3e5 || y("request lifetime must be in (0, 300] seconds");
}
function Gi(e, t) {
  (!ArrayBuffer.isView(e) || !(e instanceof Uint8Array) || Object.getPrototypeOf(e) !== Uint8Array.prototype) && y(`${t} must be a plain Uint8Array`), xi.call(e) !== 32 && y(`${t} must be exactly 32 bytes`);
  const i = Array.from({ length: 32 }, (d, u) => String(u)), r = Reflect.ownKeys(e);
  (r.length !== i.length || r.some((d, u) => d !== i[u])) && y(`${t} Uint8Array must contain only 32 indexed bytes`);
  const s = _i.call(e);
  let a = !1;
  if (hn)
    try {
      hn.call(s), a = !0;
    } catch {
      a = !1;
    }
  a && y(`${t} must not use SharedArrayBuffer storage`);
  try {
    Di.call(s);
  } catch {
    y(`${t} must use ArrayBuffer storage`);
  }
  const o = new Uint8Array(32);
  for (let d = 0; d < 32; d += 1) {
    const u = Object.getOwnPropertyDescriptor(e, String(d));
    (!u || !u.enumerable || !("value" in u) || !Number.isInteger(u.value) || u.value < 0 || u.value > 255) && y(`${t} Uint8Array contains an invalid indexed byte`), o[d] = e[d];
  }
  let c = "";
  for (let d = 0; d < 32; d += 1) c += String.fromCharCode(o[d]);
  const l = globalThis.btoa(c).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/gu, "");
  return l.length !== 43 && y(`${t} did not encode to 43 base64url characters`), l;
}
function Hn(e, t) {
  ue(e, t), /^[A-Za-z0-9_-]{43}$/u.test(e) || y(`${t} must be canonical unpadded base64url`);
  let n;
  try {
    n = globalThis.atob(`${e.replace(/-/gu, "+").replace(/_/gu, "/")}=`);
  } catch {
    y(`${t} must be valid base64url`);
  }
  const i = Uint8Array.from(n, (r) => r.charCodeAt(0));
  return (i.byteLength !== 32 || Gi(i, t) !== e) && y(`${t} must canonically encode exactly 32 bytes`), e;
}
function Ki(e) {
  let t = 0, n = 0, i = "";
  for (const r of e)
    for (t = t << 8 | r, n += 8; n >= 5; )
      n -= 5, i += Mt[t >>> n & 31], t &= (1 << n) - 1;
  return n !== 0 && (i += Mt[t << 5 - n & 31]), i;
}
function Ji(e, t) {
  ue(e, "modelCid"), Bi.test(e) || y("modelCid must be canonical CIDv1 raw sha2-256 base32");
  let n = 0, i = 0;
  const r = [];
  for (const o of e.slice(1)) {
    const c = Mt.indexOf(o);
    c < 0 && y("modelCid contains invalid base32"), n = n << 5 | c, i += 5, i >= 8 && (i -= 8, r.push(n >>> i & 255), n &= (1 << i) - 1);
  }
  (i !== 0 && n !== 0 || r.length !== 36) && y("modelCid has noncanonical base32 tail bits or length");
  const s = Uint8Array.from(r);
  (s[0] !== 1 || s[1] !== 85 || s[2] !== 18 || s[3] !== 32) && y("modelCid must encode CIDv1 raw sha2-256 with a 32-byte digest"), `b${Ki(s)}` !== e && y("modelCid base32 is not canonical");
  let a = 0;
  for (let o = 0; o < 32; o += 1) {
    const c = Number.parseInt(t.slice(o * 2, o * 2 + 2), 16);
    a |= s[o + 4] ^ c;
  }
  return a !== 0 && y("modelCid digest does not match modelSha256"), e;
}
function Ft(e) {
  return e === null || typeof e == "boolean" ? JSON.stringify(e) : typeof e == "string" ? (ue(e, "canonical JSON string"), JSON.stringify(e)) : typeof e == "number" ? (Number.isFinite(e) || y("canonical JSON numbers must be finite"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(Ft).join(",")}]` : (Ot(e) || y("canonical JSON contains an unsupported value"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${Ft(e[t])}`).join(",")}}`);
}
function Yt(e, t) {
  for (const n of ["identityScheme", "keyId", "signatureProfile"])
    e[n] !== t[n] && y(`canonicalEnvelope ${n} must match the signature result`);
}
function Qi(e, t) {
  const i = ee(e, [
    "audience",
    "challengeSha256",
    "clientId",
    "expiresAt",
    "identityScheme",
    "issuedAt",
    "keyId",
    "kind",
    "nonce",
    "protocolVersion",
    "requestOrigin",
    "signatureProfile"
  ], "SDN login canonicalEnvelope");
  Yt(i, t), v(i.audience, Vt, "SDN login envelope audience"), q(i.challengeSha256, le, "SDN login envelope challengeSha256"), v(i.clientId, "sdn-node-console-v1", "SDN login envelope clientId"), we(i.issuedAt, "SDN login envelope issuedAt"), we(i.expiresAt, "SDN login envelope expiresAt"), Nt(i.issuedAt, i.expiresAt), v(i.identityScheme, Ee, "SDN login envelope identityScheme"), q(i.keyId, We, "SDN login envelope keyId"), v(i.kind, "sdn-login", "SDN login envelope kind"), q(i.nonce, le, "SDN login envelope nonce"), v(i.protocolVersion, 2, "SDN login envelope protocolVersion"), v(i.requestOrigin, "https://sdn.spaceaware.io", "SDN login envelope requestOrigin"), v(i.signatureProfile, Pe, "SDN login envelope signatureProfile");
}
function Yi(e, t) {
  const n = ee(
    e,
    [...gt, "kind"],
    "authority activation canonicalEnvelope"
  );
  Yt(n, t), v(n.kind, "asset-review-authority-activation", "activation envelope kind"), Kn(Object.fromEntries(gt.map((i) => [i, n[i]])));
}
function Xi(e, t) {
  const n = e?.decision === "approve" ? ["note", "reviewedTransform"] : e?.decision === "disapprove" ? ["reason"] : [], i = [...$t, ...n], r = ee(e, [
    ...i,
    "identityScheme",
    "keyId",
    "kind",
    "purpose",
    "signatureProfile"
  ], "asset review decision canonicalEnvelope");
  Yt(r, t), v(r.identityScheme, Ee, "decision envelope identityScheme"), q(r.keyId, We, "decision envelope keyId"), v(r.kind, "asset-review-attestation", "decision envelope kind"), v(r.purpose, "asset-review-approval", "decision envelope purpose"), v(r.signatureProfile, Pe, "decision envelope signatureProfile"), Jn(Object.fromEntries(i.map((s) => [s, r[s]])));
}
function Zi(e, t, n) {
  ue(e, "canonicalEnvelope"), Se.encode(e).byteLength > Wn && y("canonicalEnvelope is too large");
  const i = zn(e);
  return (!Ot(i) || i.kind !== t) && y("canonicalEnvelope kind does not match the operation"), Ft(i) !== e && y("canonicalEnvelope must be exact JCS"), t === "sdn-login" ? Qi(i, n) : t === "asset-review-authority-activation" ? Yi(i, n) : t === "asset-review-attestation" ? Xi(i, n) : y("canonicalEnvelope operation is not registered"), e;
}
function er(e, t) {
  const n = ee(e, Mi, `identity key ${t.purpose}`);
  return v(n.purpose, t.purpose, "key purpose"), v(n.identityScheme, Ee, "key identityScheme"), v(n.seedProfile, mt, "key seedProfile"), v(n.signatureProfile, t.signatureProfile, "key signatureProfile"), v(n.curve, t.curve, "key curve"), v(n.derivation, "slip10", "key derivation"), v(n.path, t.path, "key path"), v(n.encoding, "raw", "key encoding"), q(n.publicKeyHex, le, "key publicKeyHex"), v(n.bip32Fingerprint, null, "key bip32Fingerprint"), q(n.keyId, We, "key keyId"), de([
    ["bip32Fingerprint", null],
    ["curve", n.curve],
    ["derivation", "slip10"],
    ["encoding", "raw"],
    ["identityScheme", Ee],
    ["keyId", n.keyId],
    ["path", n.path],
    ["publicKeyHex", n.publicKeyHex],
    ["purpose", n.purpose],
    ["seedProfile", mt],
    ["signatureProfile", n.signatureProfile]
  ]);
}
function tr(e) {
  const t = ee(e, Vi, "WalletPublicIdentity");
  v(t.schemaVersion, 1, "identity schemaVersion"), v(t.identityScheme, Ee, "identity identityScheme"), v(t.seedProfile, mt, "identity seedProfile"), t.accountIndex !== 0 && y("public wallet identity must use account 0"), v(t.accountLabel, null, "identity accountLabel"), ue(t.accountXpub, "identity accountXpub"), /^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(t.accountXpub) || y("identity accountXpub is invalid"), ue(t.accountPeerId, "identity accountPeerId"), (!t.accountPeerId.startsWith("16Uiu2H") || t.accountPeerId.length < 40 || t.accountPeerId.length > 64 || !ji.test(t.accountPeerId)) && y("identity accountPeerId is invalid"), q(t.accountFingerprint, /^[0-9a-f]{8}$/u, "identity accountFingerprint"), Jt(t.keys, 3, "public identity keys array");
  const n = [
    {
      purpose: "asset-review-approval",
      curve: "ed25519",
      signatureProfile: Pe,
      path: "m/44'/0'/0'/2'/0'"
    },
    {
      purpose: "contact-encryption",
      curve: "x25519",
      signatureProfile: null,
      path: "m/44'/0'/0'/1'/0'"
    },
    {
      purpose: "sdn-authentication",
      curve: "ed25519",
      signatureProfile: Pe,
      path: "m/44'/0'/0'/0'/0'"
    }
  ], i = Array.from(
    { length: 3 },
    (r, s) => er(t.keys[s], n[s])
  );
  return de([
    ["accountFingerprint", t.accountFingerprint],
    ["accountIndex", 0],
    ["accountLabel", null],
    ["accountPeerId", t.accountPeerId],
    ["accountXpub", t.accountXpub],
    ["identityScheme", Ee],
    ["keys", ot(i)],
    ["schemaVersion", 1],
    ["seedProfile", mt]
  ]);
}
function Gn(e, t) {
  const n = ee(e, $i, `${t} result`);
  v(n.schemaVersion, 1, "connection result schemaVersion"), Qt(n.event, ["connected", "disconnected"], "connection event"), t === "connect" && n.event !== "connected" && y("connect result must be connected");
  let i = null, r = null;
  return n.event === "connected" ? (i = tr(n.identity), r = we(n.connectionExpiresAt, "connectionExpiresAt")) : (n.identity !== null || n.connectionExpiresAt !== null) && y("disconnected result must clear identity and expiry"), de([
    ["connectionExpiresAt", r],
    ["event", n.event],
    ["identity", i],
    ["schemaVersion", 1]
  ]);
}
function nr(e) {
  const t = ee(e, Fi, "raw signature");
  return v(t.schemaVersion, 1, "signature schemaVersion"), q(t.keyId, We, "signature keyId"), Qt(t.identityScheme, [
    "sdn-fast-password-auth-v1-legacy",
    "sdn-bip39-auth-v1-legacy"
  ], "raw signature identityScheme"), v(t.algorithm, "ed25519", "signature algorithm"), v(t.encoding, "raw", "signature encoding"), v(t.signatureProfile, yn, "signature profile"), q(t.signatureHex, Fn, "signatureHex"), de([
    ["algorithm", "ed25519"],
    ["encoding", "raw"],
    ["identityScheme", t.identityScheme],
    ["keyId", t.keyId],
    ["schemaVersion", 1],
    ["signatureHex", t.signatureHex],
    ["signatureProfile", yn]
  ]);
}
function Xt(e, t) {
  const n = ee(e, Wi, "canonical signature");
  return v(n.schemaVersion, 1, "signature schemaVersion"), q(n.keyId, We, "signature keyId"), v(n.identityScheme, Ee, "signature identityScheme"), v(n.algorithm, "ed25519", "signature algorithm"), v(n.encoding, "raw", "signature encoding"), v(n.signatureProfile, Pe, "signature profile"), Zi(n.canonicalEnvelope, t, n), q(n.signedDigestSha256, le, "signedDigestSha256"), q(n.signatureHex, Fn, "signatureHex"), de([
    ["algorithm", "ed25519"],
    ["canonicalEnvelope", n.canonicalEnvelope],
    ["encoding", "raw"],
    ["identityScheme", Ee],
    ["keyId", n.keyId],
    ["schemaVersion", 1],
    ["signatureHex", n.signatureHex],
    ["signatureProfile", Pe],
    ["signedDigestSha256", n.signedDigestSha256]
  ]);
}
function ir(e) {
  const t = ee(e, ["challengeBase64url", "protocolVersion"], "SDN login v1 request");
  return v(t.protocolVersion, 1, "SDN login v1 protocolVersion"), Hn(t.challengeBase64url, "challengeBase64url"), de([
    ["challengeBase64url", t.challengeBase64url],
    ["protocolVersion", 1]
  ]);
}
function rr(e) {
  const t = ee(e, [
    "audience",
    "challengeBase64url",
    "expiresAt",
    "issuedAt",
    "nonce",
    "protocolVersion"
  ], "SDN login v2 request");
  return v(t.protocolVersion, 2, "SDN login v2 protocolVersion"), v(t.audience, Vt, "SDN login v2 audience"), Hn(t.challengeBase64url, "challengeBase64url"), q(t.nonce, le, "SDN login nonce"), we(t.issuedAt, "SDN login issuedAt"), we(t.expiresAt, "SDN login expiresAt"), Nt(t.issuedAt, t.expiresAt), de([
    ["audience", Vt],
    ["challengeBase64url", t.challengeBase64url],
    ["expiresAt", t.expiresAt],
    ["issuedAt", t.issuedAt],
    ["nonce", t.nonce],
    ["protocolVersion", 2]
  ]);
}
function Kn(e) {
  const t = ee(e, gt, "authority activation request");
  return v(t.protocolVersion, 1, "activation protocolVersion"), v(t.audience, ki, "activation audience"), v(t.requestOrigin, Mn, "activation requestOrigin"), v(t.clientId, $n, "activation clientId"), v(t.serviceInstance, "assets.ipfs.01/asset-review-attestation", "activation serviceInstance"), v(t.purpose, "asset-review-authority-activation", "activation purpose"), q(t.nonce, le, "activation nonce"), we(t.issuedAt, "activation issuedAt"), we(t.expiresAt, "activation expiresAt"), Nt(t.issuedAt, t.expiresAt), q(t.publicKeyHex, le, "activation publicKeyHex"), q(t.keyId, We, "activation keyId"), v(t.identityScheme, Ee, "activation identityScheme"), v(t.signatureProfile, Pe, "activation signatureProfile"), de(gt.map((n) => [n, t[n]]));
}
function Dt(e, t, n) {
  Jt(e, t, `${n} array`);
  const i = Array.from({ length: t }, (r, s) => {
    const a = e[s];
    return (typeof a != "number" || !Number.isFinite(a)) && y(`${n} values must be finite numbers`), a;
  });
  return ot(i);
}
function sr(e) {
  const t = ee(e, zi, "reviewedTransform"), n = Dt(t.translation, 3, "translation"), i = Dt(t.rotation, 4, "rotation"), r = Dt(t.scale, 3, "scale");
  n.some((a) => Math.abs(a) > xe.translationComponentAbsMax) && y("translation values exceed the reviewed transform policy"), r.some((a) => a <= xe.scaleComponentExclusiveMin || a > xe.scaleComponentInclusiveMax) && y("scale values exceed the reviewed transform policy");
  const s = Math.hypot(...i);
  return Math.abs(s - 1) > xe.quaternionNormTolerance && y("rotation must be a unit quaternion within the reviewed tolerance"), Qt(t.upAxis, xe.upAxes, "upAxis"), ue(t.sourceUnits, "sourceUnits"), Object.hasOwn(xe.metersPerSourceUnit, t.sourceUnits) || y("sourceUnits is not allowed by the reviewed transform policy"), (typeof t.metersPerSourceUnit != "number" || !Number.isFinite(t.metersPerSourceUnit) || t.metersPerSourceUnit !== xe.metersPerSourceUnit[t.sourceUnits]) && y("metersPerSourceUnit must exactly match sourceUnits"), de([
    ["metersPerSourceUnit", t.metersPerSourceUnit],
    ["rotation", i],
    ["scale", r],
    ["sourceUnits", t.sourceUnits],
    ["translation", n],
    ["upAxis", t.upAxis]
  ]);
}
function mn(e) {
  return e >= 9 && e <= 13 || e === 32 || e === 160 || e === 5760 || e >= 8192 && e <= 8202 || e === 8232 || e === 8233 || e === 8239 || e === 8287 || e === 12288 || e === 65279;
}
function gn(e, t, n) {
  if (n && e === null) return null;
  ue(e, t), (e.length === 0 || Se.encode(e).byteLength > 2e3) && y(`${t} has an invalid length`);
  const i = Array.from(e, (r) => r.codePointAt(0));
  return (mn(i[0]) || mn(i[i.length - 1])) && y(`${t} must already be trimmed`), e;
}
function Jn(e) {
  const t = qn(e);
  Ot(t) || y("asset review decision request must be a JSON object");
  const n = Object.getOwnPropertyDescriptor(t, "decision");
  (!n || !n.enumerable || !("value" in n) || n.value === void 0) && y("asset review decision request has an invalid decision field");
  const i = n.value, r = i === "approve" ? ["note", "reviewedTransform"] : i === "disapprove" ? ["reason"] : [], s = ee(t, [...$t, ...r], "asset review decision request");
  v(s.protocolVersion, 1, "decision protocolVersion"), v(s.audience, Pi, "decision audience"), v(s.requestOrigin, Mn, "decision requestOrigin"), v(s.clientId, $n, "decision clientId"), q(s.challengeId, le, "challengeId"), q(s.nonce, le, "decision nonce"), we(s.issuedAt, "decision issuedAt"), we(s.expiresAt, "decision expiresAt"), Nt(s.issuedAt, s.expiresAt), q(s.modelSha256, le, "modelSha256"), q(s.metadataSha256, le, "metadataSha256"), s.previousDecisionHead !== null && q(s.previousDecisionHead, le, "previousDecisionHead"), Ji(s.modelCid, s.modelSha256), (!Number.isSafeInteger(s.modelBytes) || s.modelBytes <= 0) && y("modelBytes must be a positive safe integer"), ue(s.candidateKey, "candidateKey");
  const a = "asset-review:", o = `:${s.modelSha256}`;
  (!s.candidateKey.startsWith(a) || !s.candidateKey.endsWith(o) || Se.encode(s.candidateKey).byteLength > 206) && y("candidateKey does not bind the model digest");
  const c = s.candidateKey.slice(a.length, -o.length);
  (!/^[a-z0-9-]+\/[a-z0-9][a-z0-9-]*$/u.test(c) || Se.encode(c).byteLength > 128) && y("candidateKey entityId is invalid");
  const l = $t.map((u) => [u, s[u]]);
  return i === "approve" ? (l.push(["note", gn(s.note, "note", !0)]), l.push(["reviewedTransform", sr(s.reviewedTransform)])) : l.push(["reason", gn(s.reason, "reason", !1)]), Object.values(s).filter((u) => typeof u == "string").reduce((u, f) => u + Se.encode(f).byteLength, 0) > 16384 && y("decision request strings exceed 16 KiB"), de(l);
}
function Qn(e, t) {
  return ee(e, [], t), ot({});
}
function or(e) {
  return Qn(e, "wallet connect request");
}
function ar(e) {
  return Gn(e, "connect");
}
function cr(e) {
  return Qn(e, "wallet account request");
}
function Yn(e) {
  return Gn(e, "account");
}
function lr(e) {
  return ir(e);
}
function ur(e) {
  return nr(e);
}
function dr(e) {
  return rr(e);
}
function fr(e) {
  return Xt(e, "sdn-login");
}
function pr(e) {
  return Kn(e);
}
function hr(e) {
  return Xt(e, "asset-review-authority-activation");
}
function yr(e) {
  return Jn(e);
}
function mr(e) {
  return Xt(e, "asset-review-attestation");
}
const gr = Object.freeze([
  "callbackUri",
  "clientDisplayName",
  "clientId",
  "expiresAt",
  "operation",
  "registryVersion",
  "request",
  "requestOrigin",
  "requestSha256",
  "resultToken",
  "schemaVersion",
  "state",
  "transactionId"
]), Pt = /^[0-9a-f]{64}$/u, Xn = /^[A-Za-z0-9_-]{43}$/u, Ar = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, Er = new TextEncoder(), Zn = Object.freeze({
  "sdn.auth.jcs-envelope.v2": Object.freeze({
    parseRequest: dr,
    buildResult: fr,
    sign(e, t, n, i) {
      return e.signSdnLoginV2(t, n, i.registryRow);
    }
  }),
  "sdn.auth.raw-challenge.v1": Object.freeze({
    parseRequest: lr,
    buildResult: ur,
    sign(e, t, n) {
      return e.signSdnLoginV1(t, Rr(n.challengeBase64url));
    }
  }),
  "sdn.asset-review.authority-activation.v1": Object.freeze({
    parseRequest: pr,
    buildResult: hr,
    sign(e, t, n, i) {
      return e.signAssetReviewAuthorityActivation(t, n, i.registryRow);
    }
  }),
  "sdn.asset-review.decision.v1": Object.freeze({
    parseRequest: yr,
    buildResult: mr,
    sign(e, t, n, i) {
      return e.signAssetReviewDecision(t, n, i.registryRow);
    }
  }),
  "sdn.wallet.account.v1": Object.freeze({
    parseRequest: cr,
    buildResult: Yn,
    connect: !0
  }),
  "sdn.wallet.connect.v1": Object.freeze({
    parseRequest: or,
    buildResult: ar,
    connect: !0
  })
});
class Le extends Error {
  constructor(t) {
    super(t), this.name = "WalletOperationError", this.code = t;
  }
}
function $(e) {
  throw new Le(e);
}
function Zt(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function wr(e, t, n = "INVALID_TRANSACTION") {
  Zt(e) || $(n);
  let i;
  try {
    i = Reflect.ownKeys(e);
  } catch {
    $(n);
  }
  i.some((o) => typeof o != "string") && $(n);
  const r = [...i].sort(), s = [...t].sort();
  (r.length !== s.length || r.some((o, c) => o !== s[c])) && $(n);
  const a = /* @__PURE__ */ Object.create(null);
  for (const o of r) {
    let c;
    try {
      c = Object.getOwnPropertyDescriptor(e, o);
    } catch {
      $(n);
    }
    (!c?.enumerable || !("value" in c) || c.value === void 0) && $(n), a[o] = c.value;
  }
  return a;
}
function br(e, t = "INVALID_TRANSACTION") {
  (typeof e != "string" || !Ar.test(e)) && $(t);
  const n = Date.parse(e);
  return (!Number.isFinite(n) || new Date(n).toISOString() !== e) && $(t), n;
}
function tt(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(tt));
  if (!Zt(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = tt(e[n]);
  return Object.freeze(t);
}
function At(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" ? JSON.stringify(e) : typeof e == "number" ? (Number.isFinite(e) || $("INVALID_TRANSACTION"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(At).join(",")}]` : (Zt(e) || $("INVALID_TRANSACTION"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${At(e[t])}`).join(",")}}`);
}
async function Ir(e, t) {
  typeof t != "function" && $("CRYPTO_UNAVAILABLE");
  let n;
  try {
    n = await t(Er.encode(At(e)));
  } catch {
    $("CRYPTO_UNAVAILABLE");
  }
  return (!(n instanceof Uint8Array) || n.byteLength !== 32) && $("CRYPTO_UNAVAILABLE"), Array.from(n, (i) => i.toString(16).padStart(2, "0")).join("");
}
function vr(e, t) {
  if (typeof e != "string" || typeof t != "string") return !1;
  const n = Math.max(e.length, t.length);
  let i = e.length ^ t.length;
  for (let r = 0; r < n; r += 1)
    i |= (e.charCodeAt(r) || 0) ^ (t.charCodeAt(r) || 0);
  return i === 0;
}
function Sr(e, t) {
  try {
    if (typeof e?.resolveRegistryBinding == "function")
      return e.resolveRegistryBinding(t);
    if (typeof e?.resolve == "function") return e.resolve(t);
    if (typeof e == "function") return e(t);
  } catch (n) {
    if (n instanceof Le) throw n;
    $("UNREGISTERED_TRANSACTION");
  }
  $("UNREGISTERED_TRANSACTION");
}
function oe({ document: e, window: t }) {
  let n = !1;
  try {
    n = t?.top === t;
  } catch {
    n = !1;
  }
  (!n || e?.visibilityState !== "visible" || typeof e?.hasFocus != "function" || e.hasFocus() !== !0) && $("WALLET_CONTEXT_UNTRUSTED");
}
async function kt(e, {
  registry: t,
  relay: n,
  sha256: i,
  window: r,
  now: s = () => Date.now(),
  expectedTransactionId: a = null
}) {
  const o = wr(e, gr);
  (o.schemaVersion !== 1 || typeof o.clientDisplayName != "string" || o.clientDisplayName.length < 1 || o.clientDisplayName.length > 80 || !Pt.test(o.transactionId) || !Pt.test(o.state) || !Pt.test(o.requestSha256) || !Xn.test(o.resultToken)) && $("INVALID_TRANSACTION"), a !== null && o.transactionId !== a && $("INVALID_TRANSACTION");
  const c = Zn[o.operation];
  c || $("UNREGISTERED_TRANSACTION");
  const l = Sr(t, {
    clientId: o.clientId,
    operation: o.operation,
    requestOrigin: o.requestOrigin
  });
  (l.registryReleaseSha256 !== o.registryVersion || l.callbackUri !== o.callbackUri || l.clientDisplayName !== o.clientDisplayName || l.clientId !== o.clientId || l.operation !== o.operation || l.requestOrigin !== o.requestOrigin) && $("REGISTRY_BINDING_MISMATCH");
  const d = s(), u = br(o.expiresAt);
  (!Number.isFinite(d) || u <= d || u - d > l.maxLifetimeSeconds * 1e3) && $("TRANSACTION_EXPIRED");
  let f;
  try {
    f = c.parseRequest(o.request);
  } catch {
    $("INVALID_TRANSACTION");
  }
  let p;
  try {
    p = await Ir(f, i);
  } catch (A) {
    if (A instanceof Le) throw A;
    $("CRYPTO_UNAVAILABLE");
  }
  vr(p, o.requestSha256) || $("REQUEST_HASH_MISMATCH");
  const g = { ...o, request: f };
  return Object.freeze({
    binding: tt(l),
    operation: c,
    request: tt(f),
    transaction: tt(g)
  });
}
function Rr(e) {
  Xn.test(e) || $("INVALID_TRANSACTION");
  let t;
  try {
    t = globalThis.atob(`${e.replace(/-/gu, "+").replace(/_/gu, "/")}=`);
  } catch {
    $("INVALID_TRANSACTION");
  }
  const n = Uint8Array.from(t, (i) => i.charCodeAt(0));
  return n.byteLength !== 32 && $("INVALID_TRANSACTION"), n;
}
function pe(e, t, n, i) {
  const r = e.createElement("div");
  r.className = "wallet-confirmation-row";
  const s = e.createElement("strong");
  s.textContent = `${n}: `;
  const a = e.createElement("span");
  a.textContent = i === null ? "null" : typeof i == "string" ? i : At(i), r.append(s, a), t.append(r);
}
function Lr(e, {
  binding: t,
  document: n,
  identity: i = null,
  request: r,
  transaction: s = null
}) {
  e.replaceChildren();
  const a = n.createElement("h1");
  a.id = "wallet-confirmation-heading", a.textContent = "Confirm wallet action", e.append(a), pe(n, e, "Client", t.clientDisplayName), pe(n, e, "Requesting origin", t.requestOrigin), pe(n, e, "Operation", t.operation), t.audience !== void 0 && pe(n, e, "Audience", t.audience), t.callbackUri !== void 0 && pe(n, e, "Callback URI", t.callbackUri), s && (pe(n, e, "Transaction ID", s.transactionId), pe(n, e, "Request hash", s.requestSha256), pe(n, e, "Registry release", s.registryVersion), pe(n, e, "Transaction expiry", s.expiresAt));
  const o = t.operation.startsWith("sdn.asset-review.") ? "asset-review-approval" : t.operation.startsWith("sdn.auth.") ? "sdn-authentication" : null, c = o && Array.isArray(i?.keys) ? i.keys.find((l) => l?.purpose === o) : null;
  c?.keyId && pe(n, e, "Signing key ID", c.keyId);
  for (const l of Object.keys(r).sort()) pe(n, e, l, r[l]);
  return e;
}
function Or({ binding: e, document: t, identity: n = null, request: i, transaction: r = null }) {
  const s = t.activeElement ?? null, a = t.createElement("section");
  a.className = "wallet-confirmation", a.setAttribute?.("role", "dialog"), a.setAttribute?.("aria-modal", "true"), a.setAttribute?.("aria-labelledby", "wallet-confirmation-heading"), a.tabIndex = -1, Lr(a, { binding: e, document: t, identity: n, request: i, transaction: r });
  const o = t.createElement("div");
  o.className = "wallet-confirmation-actions";
  const c = t.createElement("button");
  c.type = "button", c.dataset.walletAction = "confirm", c.textContent = i?.decision === "approve" ? "Approve" : i?.decision === "disapprove" ? "Disapprove" : e.operation === "sdn.asset-review.authority-activation.v1" ? "Activate" : "Confirm";
  const l = t.createElement("button");
  l.type = "button", l.dataset.walletAction = "cancel", l.textContent = "Cancel", o.append(c, l), a.append(o), t.body.append(a);
  let d = !1, u, f;
  const p = new Promise((O, I) => {
    u = O, f = I;
  }), g = (O, I) => {
    d || I?.isTrusted !== !0 || (d = !0, c.disabled = !0, l.disabled = !0, O ? u() : f(new Le("USER_CANCELLED")));
  }, A = (O) => g(!0, O), h = (O) => g(!1, O), E = (O) => {
    if (O?.isTrusted !== !0) return;
    if (O.key === "Escape") {
      O.preventDefault?.(), g(!1, O);
      return;
    }
    if (O.key !== "Tab") return;
    O.preventDefault?.();
    const I = t.activeElement;
    O.shiftKey === !0 ? (I === c ? l : c).focus?.() : (I === l ? c : l).focus?.();
  }, m = (O) => {
    let I = !1;
    try {
      I = a.contains?.(O?.target) === !0;
    } catch {
      I = !1;
    }
    I || c.focus?.();
  };
  c.addEventListener("click", A), l.addEventListener("click", h), a.addEventListener("keydown", E), t.addEventListener?.("focusin", m);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({
    promise: p,
    cancel(O = "STALE_CONTROLLER") {
      d || (d = !0, f(new Le(O)));
    },
    destroy() {
      c.removeEventListener?.("click", A), l.removeEventListener?.("click", h), a.removeEventListener?.("keydown", E), t.removeEventListener?.("focusin", m), a.remove();
      try {
        s?.focus?.();
      } catch {
      }
    }
  });
}
async function Nr({
  assertCurrent: e = () => {
  },
  binding: t,
  handle: n,
  identity: i,
  transaction: r,
  wasm: s
}) {
  const a = s?.sdn ?? s, o = Zn[r.operation];
  if ((!o || !a) && $("OPERATION_NOT_ALLOWED"), o.connect)
    return o.buildResult({
      connectionExpiresAt: r.expiresAt,
      event: "connected",
      identity: i,
      schemaVersion: 1
    });
  let c;
  try {
    c = await o.sign(a, n, r.request, t);
  } catch (l) {
    throw l;
  }
  e();
  try {
    return o.buildResult(c);
  } catch {
    $("INVALID_WALLET_RESULT");
  }
}
function Tr() {
  return Yn({
    connectionExpiresAt: null,
    event: "disconnected",
    identity: null,
    schemaVersion: 1
  });
}
const An = "webauthn-prf-hkdf-sha256-aes256gcm-v2", Cr = "sdn-bip32-slip10-purpose-v1", _r = "password-scrypt-v2", xr = Object.freeze([
  "aad",
  "canonicalUsername",
  "ciphertextBase64url",
  "createdAt",
  "credentialIdBase64url",
  "hkdfSaltBase64url",
  "nonceBase64url",
  "prfInputBase64url",
  "schemaVersion",
  "storageProfile"
]), Dr = Object.freeze([
  "credentialIdBase64url",
  "identityScheme",
  "schemaVersion",
  "seedProfile",
  "storageProfile",
  "usernameSha256"
]), Pr = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, kr = /^[0-9a-f]{64}$/u, Ur = /^[A-Za-z0-9_-]+$/u, jr = new TextEncoder(), ae = "sdn.wallet.remembered.v2", Re = `${ae}.pending`, pt = 16 * 1024, ei = Object.freeze([
  "wallet_storage_metadata",
  "wallet_storage_encrypted",
  "wallet_storage_passkey_credential",
  "encrypted_wallet",
  "passkey_credential",
  "passkey_wallet"
]), ti = /* @__PURE__ */ new Set([
  ae,
  Re,
  ...ei
]), Wt = /* @__PURE__ */ new WeakSet();
class at extends Error {
  constructor(t) {
    super(t), this.name = "WalletStorageError", this.code = t;
  }
}
function C(e) {
  throw new at(e);
}
function ni(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function En(e, t) {
  ni(e) || C("INVALID_REMEMBERED_WALLET");
  let n;
  try {
    n = Reflect.ownKeys(e);
  } catch {
    C("INVALID_REMEMBERED_WALLET");
  }
  n.some((a) => typeof a != "string") && C("INVALID_REMEMBERED_WALLET");
  const i = [...t].sort(), r = [...n].sort();
  (r.length !== i.length || r.some((a, o) => a !== i[o])) && C("INVALID_REMEMBERED_WALLET");
  const s = {};
  for (const a of i) {
    let o;
    try {
      o = Object.getOwnPropertyDescriptor(e, a);
    } catch {
      C("INVALID_REMEMBERED_WALLET");
    }
    (!o?.enumerable || !("value" in o) || o.value === void 0) && C("INVALID_REMEMBERED_WALLET"), s[a] = o.value;
  }
  return s;
}
function wn(e) {
  if (typeof e != "string") return !1;
  for (let t = 0; t < e.length; t += 1) {
    const n = e.charCodeAt(t);
    if (n >= 55296 && n <= 56319) {
      const i = e.charCodeAt(++t);
      if (!(i >= 56320 && i <= 57343)) return !1;
    } else if (n >= 56320 && n <= 57343)
      return !1;
  }
  return !0;
}
function Br(e) {
  let t = "";
  for (let n = 0; n < e.length; n += 32768)
    t += String.fromCharCode(...e.subarray(n, n + 32768));
  return btoa(t).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
}
function Ae(e, { minimum: t = 0, maximum: n = 65536, exact: i = null } = {}) {
  (typeof e != "string" || e.length === 0 || !Ur.test(e)) && C("INVALID_REMEMBERED_WALLET");
  const r = e.length % 4;
  r === 1 && C("INVALID_REMEMBERED_WALLET");
  const s = e.replace(/-/gu, "+").replace(/_/gu, "/") + "=".repeat((4 - r) % 4);
  let a;
  try {
    a = atob(s);
  } catch {
    C("INVALID_REMEMBERED_WALLET");
  }
  const o = new Uint8Array(a.length);
  for (let c = 0; c < a.length; c += 1) o[c] = a.charCodeAt(c);
  return (Br(o) !== e || o.length < t || o.length > n || i !== null && o.length !== i) && C("INVALID_REMEMBERED_WALLET"), o;
}
function en(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" || typeof e == "number" && Number.isFinite(e) ? JSON.stringify(e) : (ni(e) || C("INVALID_REMEMBERED_WALLET"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${en(e[t])}`).join(",")}}`);
}
function Vr(e) {
  return Object.freeze({
    aad: Object.freeze({ ...e.aad }),
    canonicalUsername: e.canonicalUsername,
    ciphertextBase64url: e.ciphertextBase64url,
    createdAt: e.createdAt,
    credentialIdBase64url: e.credentialIdBase64url,
    hkdfSaltBase64url: e.hkdfSaltBase64url,
    nonceBase64url: e.nonceBase64url,
    prfInputBase64url: e.prfInputBase64url,
    schemaVersion: e.schemaVersion,
    storageProfile: e.storageProfile
  });
}
function ii(e) {
  const t = En(e, xr), n = En(t.aad, Dr), i = wn(t.canonicalUsername) ? jr.encode(t.canonicalUsername) : null;
  return (t.schemaVersion !== 2 || t.storageProfile !== An || n.schemaVersion !== 2 || n.storageProfile !== An || n.identityScheme !== Cr || n.seedProfile !== _r || !i || i.length < 3 || i.length > 64 || !/^[a-z0-9][a-z0-9._-]*$/u.test(t.canonicalUsername) || !kr.test(n.usernameSha256) || t.credentialIdBase64url !== n.credentialIdBase64url || !wn(t.createdAt) || !Pr.test(t.createdAt) || new Date(t.createdAt).toISOString() !== t.createdAt) && C("INVALID_REMEMBERED_WALLET"), Ae(t.credentialIdBase64url, { minimum: 1, maximum: 1024 }), Ae(t.ciphertextBase64url, { minimum: 17, maximum: 1024 }), Ae(t.hkdfSaltBase64url, { exact: 32 }), Ae(t.nonceBase64url, { exact: 12 }), Ae(t.prfInputBase64url, { exact: 32 }), Vr({ ...t, aad: n });
}
function Mr(e) {
  return en(ii(e));
}
function tn(e) {
  (typeof e != "string" || e.length === 0 || e.length > 131072) && C("INVALID_REMEMBERED_WALLET");
  let t;
  try {
    t = JSON.parse(e);
  } catch {
    C("INVALID_REMEMBERED_WALLET");
  }
  const n = ii(t);
  return en(n) !== e && C("INVALID_REMEMBERED_WALLET"), n;
}
function zt(e, t, { pending: n = !1 } = {}) {
  (!e || typeof e.getItem != "function") && C("STORAGE_UNAVAILABLE");
  let i;
  try {
    i = e.getItem(t);
  } catch {
    C("STORAGE_UNAVAILABLE");
  }
  if (i === null) return Object.freeze({ raw: null, record: null, status: "empty" });
  typeof i != "string" && C("STORAGE_UNAVAILABLE");
  const r = i.length > pt;
  if (n || r)
    return Object.freeze({
      exportable: !r,
      oversized: r,
      raw: r ? null : i,
      rawLength: i.length,
      record: null,
      status: "quarantined"
    });
  try {
    const s = tn(i);
    return Object.freeze({
      raw: i,
      record: s,
      status: n ? "quarantined" : "valid"
    });
  } catch {
    return Object.freeze({
      exportable: !0,
      oversized: !1,
      raw: i,
      rawLength: i.length,
      record: null,
      status: "quarantined"
    });
  }
}
function ri(e) {
  const t = zt(e, Re, { pending: !0 }), n = zt(e, ae), i = t.status === "empty", r = n.status === "empty" || n.status === "valid";
  return Object.freeze({
    active: n,
    canRestore: i && n.status === "valid",
    canSetup: i && r,
    pending: t
  });
}
function $r(e, t) {
  const n = ri(e);
  n.canSetup || C("STORAGE_QUARANTINED");
  const i = Mr(t);
  try {
    e.setItem(Re, i);
  } catch (a) {
    throw a;
  }
  let r;
  try {
    r = e.getItem(Re);
  } catch {
    C("STORAGE_UNAVAILABLE");
  }
  r !== i && C("STORAGE_WRITE_FAILED"), tn(r);
  const s = Object.freeze({
    previousActiveRaw: n.active.raw,
    serialized: i,
    storage: e
  });
  return Wt.add(s), s;
}
function Fr(e, t) {
  (!Wt.has(t) || t.storage !== e) && C("INVALID_STORAGE_TRANSACTION"), Wt.delete(t);
  let n, i;
  try {
    n = e.getItem(Re), i = e.getItem(ae);
  } catch {
    C("STORAGE_UNAVAILABLE");
  }
  (n !== t.serialized || i !== t.previousActiveRaw) && C("STORAGE_COLLISION"), e.removeItem(Re), e.getItem(Re) !== null && C("STORAGE_WRITE_FAILED"), e.setItem(ae, t.serialized);
}
function nn(e, t) {
  ti.has(t) || C("INVALID_STORAGE_KEY"), (!e || typeof e.getItem != "function") && C("STORAGE_UNAVAILABLE");
  let n;
  try {
    n = e.getItem(t);
  } catch {
    C("STORAGE_UNAVAILABLE");
  }
  if (n === null) return null;
  if (typeof n != "string" && C("STORAGE_UNAVAILABLE"), t === ae && n.length <= pt)
    try {
      tn(n), C("NOT_QUARANTINED");
    } catch (i) {
      if (i instanceof at && i.code === "NOT_QUARANTINED") throw i;
    }
  return Object.freeze({
    exportable: n.length <= pt,
    key: t,
    oversized: n.length > pt,
    raw: n,
    rawLength: n.length
  });
}
function Wr(e) {
  return Object.freeze({
    exportable: e.exportable,
    key: e.key,
    oversized: e.oversized,
    rawLength: e.rawLength
  });
}
function zr(e) {
  const t = [];
  for (const n of [
    ae,
    Re,
    ...ei
  ]) {
    let i;
    try {
      i = nn(e, n);
    } catch (r) {
      if (r instanceof at && r.code === "NOT_QUARANTINED") continue;
      throw r;
    }
    i && t.push(Wr(i));
  }
  return Object.freeze(t);
}
function qr(e, t) {
  const n = nn(e, t);
  return n || C("QUARANTINE_NOT_FOUND"), n.exportable || C("QUARANTINE_EXPORT_TOO_LARGE"), n.raw;
}
function Hr(e, t, n) {
  ti.has(t) || C("INVALID_STORAGE_KEY"), n !== t && C("CONFIRMATION_REQUIRED"), nn(e, t) || C("QUARANTINE_NOT_FOUND");
  try {
    e.removeItem(t), e.getItem(t) !== null && C("STORAGE_WRITE_FAILED");
  } catch (r) {
    if (r instanceof at) throw r;
    C("STORAGE_UNAVAILABLE");
  }
}
function Gr(e, t) {
  t !== ae && C("CONFIRMATION_REQUIRED");
  const n = zt(e, ae);
  n.status === "empty" && C("REMEMBER_UNAVAILABLE"), n.status !== "valid" && C("STORAGE_QUARANTINED");
  try {
    e.removeItem(ae), e.getItem(ae) !== null && C("STORAGE_WRITE_FAILED");
  } catch (i) {
    if (i instanceof at) throw i;
    C("STORAGE_UNAVAILABLE");
  }
}
const Et = "sdn-bip32-slip10-purpose-v1", wt = "password-scrypt-v2", qt = "m/44'/0'/0'/2'/0'", rn = "Approval unavailable — migrate to the new wallet profile", bn = Object.freeze({
  "bip39-mnemonic-v1-legacy": "sdn-bip39-auth-v1-legacy",
  "password-fast-v1-legacy": "sdn-fast-password-auth-v1-legacy"
}), In = new TextEncoder(), Kr = Uint8Array.prototype.fill, sn = Object.freeze([
  "accountFingerprint",
  "accountIndex",
  "accountLabel",
  "accountPeerId",
  "accountXpub",
  "identityScheme",
  "keys",
  "schemaVersion",
  "seedProfile"
]), on = Object.freeze([
  "bip32Fingerprint",
  "curve",
  "derivation",
  "encoding",
  "identityScheme",
  "keyId",
  "path",
  "publicKeyHex",
  "purpose",
  "seedProfile",
  "signatureProfile"
]), bt = Object.freeze([
  Object.freeze({
    curve: "ed25519",
    path: qt,
    purpose: "asset-review-approval",
    signatureProfile: "ed25519-over-sha256-jcs-v1"
  }),
  Object.freeze({
    curve: "x25519",
    path: "m/44'/0'/0'/1'/0'",
    purpose: "contact-encryption",
    signatureProfile: null
  }),
  Object.freeze({
    curve: "ed25519",
    path: "m/44'/0'/0'/0'/0'",
    purpose: "sdn-authentication",
    signatureProfile: "ed25519-over-sha256-jcs-v1"
  })
]);
class si extends Error {
  constructor(t) {
    super(t === "APPROVAL_UNAVAILABLE" ? rn : t), this.name = "WalletAccountError", this.code = t;
  }
}
function M(e) {
  throw new si(e);
}
function vn(e) {
  if (e instanceof Uint8Array)
    try {
      Kr.call(e, 0);
    } catch {
    }
}
function ke(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(ke));
  if (!e || typeof e != "object") return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = ke(e[n]);
  return Object.freeze(t);
}
function Jr(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function It(e, t) {
  Jr(e) || M("INVALID_PUBLIC_IDENTITY");
  let n;
  try {
    n = Reflect.ownKeys(e);
  } catch {
    M("INVALID_PUBLIC_IDENTITY");
  }
  n.some((a) => typeof a != "string") && M("INVALID_PUBLIC_IDENTITY");
  const i = [...n].sort(), r = [...t].sort();
  (i.length !== r.length || i.some((a, o) => a !== r[o])) && M("INVALID_PUBLIC_IDENTITY");
  const s = {};
  for (const a of i) {
    let o;
    try {
      o = Object.getOwnPropertyDescriptor(e, a);
    } catch {
      M("INVALID_PUBLIC_IDENTITY");
    }
    (!o?.enumerable || !("value" in o) || o.value === void 0) && M("INVALID_PUBLIC_IDENTITY"), s[a] = o.value;
  }
  return s;
}
function Me(e) {
  if (typeof e != "string") return !1;
  for (let t = 0; t < e.length; t += 1) {
    const n = e.charCodeAt(t);
    if (n >= 55296 && n <= 56319) {
      const i = e.charCodeAt(++t);
      if (!(i >= 56320 && i <= 57343)) return !1;
    } else if (n >= 56320 && n <= 57343) return !1;
  }
  return !0;
}
function Qr(e) {
  if (!Array.isArray(e) || Object.getPrototypeOf(e) !== Array.prototype || e.length !== bt.length) return !1;
  let t;
  try {
    t = Reflect.ownKeys(e);
  } catch {
    return !1;
  }
  const n = ["0", "1", "2", "length"];
  if (t.length !== n.length || t.some((i, r) => i !== n[r])) return !1;
  for (let i = 0; i < bt.length; i += 1) {
    const r = Object.getOwnPropertyDescriptor(e, String(i));
    if (!r?.enumerable || !("value" in r)) return !1;
  }
  return !0;
}
function Yr(e) {
  if (!Array.isArray(e) || Object.getPrototypeOf(e) !== Array.prototype || e.length !== 1) return !1;
  let t;
  try {
    t = Reflect.ownKeys(e);
  } catch {
    return !1;
  }
  if (t.length !== 2 || t[0] !== "0" || t[1] !== "length") return !1;
  const n = Object.getOwnPropertyDescriptor(e, "0");
  return n?.enumerable === !0 && "value" in n;
}
function me(e) {
  const t = It(e, sn);
  (t.schemaVersion !== 1 || t.identityScheme !== Et || t.seedProfile !== wt || t.accountIndex !== 0 || t.accountLabel !== null || !/^[0-9a-f]{8}$/u.test(t.accountFingerprint) || !Me(t.accountXpub) || !/^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(t.accountXpub) || !Me(t.accountPeerId) || !/^16Uiu2H[1-9A-HJ-NP-Za-km-z]{33,57}$/u.test(t.accountPeerId) || !Qr(t.keys)) && M("INVALID_PUBLIC_IDENTITY");
  const n = bt.map((i, r) => {
    const s = It(t.keys[r], on);
    return (s.bip32Fingerprint !== null || s.curve !== i.curve || s.derivation !== "slip10" || s.encoding !== "raw" || s.identityScheme !== Et || s.path !== i.path || s.purpose !== i.purpose || s.seedProfile !== wt || s.signatureProfile !== i.signatureProfile || !/^[0-9a-f]{64}$/u.test(s.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(s.keyId)) && M("INVALID_PUBLIC_IDENTITY"), ke(s);
  });
  return ke({ ...t, keys: n });
}
function Xr(e, { accountIndex: t = 0, profile: n } = {}) {
  const i = Object.hasOwn(bn, n) ? bn[n] : null;
  (!i || t !== 0) && M("INVALID_LEGACY_PROFILE");
  const r = It(e, sn);
  (r.schemaVersion !== 1 || r.identityScheme !== i || r.seedProfile !== n || r.accountIndex !== t || r.accountLabel !== null || !/^[0-9a-f]{8}$/u.test(r.accountFingerprint) || !Me(r.accountXpub) || !/^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(r.accountXpub) || !Me(r.accountPeerId) || !/^16Uiu2H[1-9A-HJ-NP-Za-km-z]{33,57}$/u.test(r.accountPeerId) || !Yr(r.keys)) && M("INVALID_PUBLIC_IDENTITY");
  const s = It(r.keys[0], on);
  return (s.bip32Fingerprint !== null || s.curve !== "ed25519" || s.derivation !== "bip32-scalar-as-ed25519-seed" || s.encoding !== "raw" || s.identityScheme !== i || s.path !== `m/44'/0'/${t}'/0/0` || s.purpose !== "sdn-authentication" || s.seedProfile !== n || s.signatureProfile !== "ed25519-raw-32-v1" || !/^[0-9a-f]{64}$/u.test(s.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(s.keyId)) && M("INVALID_PUBLIC_IDENTITY"), ke({ ...r, keys: [s] });
}
function Sn(e) {
  if (!(!e || typeof e != "object")) {
    try {
      e.value = "";
    } catch {
    }
    try {
      e.defaultValue = "";
    } catch {
    }
    try {
      e.disabled = !0;
    } catch {
    }
    try {
      e.inert = !0;
    } catch {
    }
    try {
      e.removeAttribute?.("name");
    } catch {
    }
    try {
      e.removeAttribute?.("autocomplete");
    } catch {
    }
    try {
      e.setSelectionRange?.(0, 0);
    } catch {
    }
    try {
      e.setCustomValidity?.("");
    } catch {
    }
  }
}
function Rn(e, t) {
  if (typeof e != "string" || typeof t != "string") return !1;
  const n = Math.max(e.length, t.length);
  let i = e.length ^ t.length;
  for (let r = 0; r < n; r += 2) {
    const s = Number.parseInt(e.slice(r, r + 2), 16), a = Number.parseInt(t.slice(r, r + 2), 16);
    i |= (Number.isNaN(s) ? 256 : s) ^ (Number.isNaN(a) ? 256 : a);
  }
  return i === 0;
}
function Zr(e) {
  return !e || typeof e != "object" || !Array.isArray(e.keys) ? null : e.keys.find((t) => t?.purpose === "asset-review-approval") ?? null;
}
function Ut(e, t) {
  try {
    e = me(e), t = me(t);
  } catch {
    return !1;
  }
  let n = !0;
  for (const i of sn.filter((r) => r !== "keys"))
    n = e[i] === t[i] && n;
  for (let i = 0; i < bt.length; i += 1) {
    const r = e.keys[i], s = t.keys[i];
    for (const a of on.filter((o) => o !== "publicKeyHex" && o !== "keyId"))
      n = r[a] === s[a] && n;
    n = Rn(r.publicKeyHex, s.publicKeyHex) && n, n = Rn(r.keyId.slice(7), s.keyId.slice(7)) && n;
  }
  return n;
}
function Be(e, t, n, i) {
  const r = e.createElement("div");
  r.className = "wallet-account-row";
  const s = e.createElement("strong");
  s.textContent = `${n}: `;
  const a = e.createElement("span");
  a.textContent = i == null ? "" : String(i), r.append(s, a), t.append(r);
}
function oi(e) {
  (e?.identityScheme !== Et || e?.seedProfile !== wt || e?.accountIndex !== 0) && M("APPROVAL_UNAVAILABLE");
  const t = Zr(e);
  return (!t || t.identityScheme !== e.identityScheme || t.seedProfile !== e.seedProfile || t.signatureProfile !== "ed25519-over-sha256-jcs-v1" || t.curve !== "ed25519" || t.derivation !== "slip10" || t.path !== qt || t.encoding !== "raw" || !/^[0-9a-f]{64}$/u.test(t.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(t.keyId)) && M("APPROVAL_UNAVAILABLE"), ke({
    algorithm: "Ed25519",
    derivationPath: qt,
    encoding: "raw-32-byte",
    identityScheme: e.identityScheme,
    keyId: t.keyId,
    publicKeyHex: t.publicKeyHex,
    purpose: "asset-review-approval",
    schemaVersion: 1,
    seedProfile: e.seedProfile,
    signatureProfile: "ed25519-over-sha256-jcs-v1"
  });
}
function es(e) {
  return JSON.stringify(e, null, 2);
}
function ts(e, t, { document: n = e.ownerDocument } = {}) {
  e.replaceChildren();
  const i = n.createElement("h1");
  if (i.textContent = "Account", e.append(i), Be(n, e, "Username / account", t?.accountLabel ?? "account 0"), Be(n, e, "Account xpub", t?.accountXpub), Be(n, e, "Peer ID", t?.accountPeerId), Be(n, e, "Fingerprint", t?.accountFingerprint), t?.identityScheme !== Et || t?.seedProfile !== wt) {
    const s = n.createElement("p");
    return s.textContent = rn, e.append(s), Object.freeze({ approvalAvailable: !1 });
  }
  const r = oi(t);
  return Be(n, e, "Asset approval public key", r.publicKeyHex), Be(n, e, "Asset approval key ID", r.keyId), Object.freeze({ approvalAvailable: !0, configuration: r });
}
function ns(e, { document: t = e.ownerDocument } = {}) {
  e.replaceChildren();
  const n = t.createElement("h2");
  n.textContent = "Stored wallet";
  const i = t.createElement("p");
  i.textContent = "Forgetting removes the saved unlock record but keeps this account signed in.";
  const r = t.createElement("button");
  r.type = "button", r.dataset.walletAction = "forget-stored-wallet", r.textContent = "Forget stored wallet";
  const s = t.createElement("div");
  s.hidden = !0;
  const a = t.createElement("p");
  a.textContent = `Type ${ae} to confirm.`;
  const o = t.createElement("input");
  o.type = "text", o.autocomplete = "off", o.spellcheck = !1, o.dataset.walletForgetConfirmation = "exact-storage-key";
  const c = t.createElement("button");
  c.type = "button", c.dataset.walletAction = "confirm-forget-stored-wallet", c.textContent = "Confirm forget";
  const l = t.createElement("button");
  l.type = "button", l.dataset.walletAction = "cancel-forget-stored-wallet", l.textContent = "Cancel";
  const d = t.createElement("p");
  return d.dataset.walletForgetStatus = "true", d.setAttribute?.("aria-live", "polite"), s.append(a, o, c, l), e.append(n, i, r, s, d), Object.freeze({
    cancel: l,
    confirm: c,
    confirmation: o,
    confirmationGroup: s,
    confirmationKey: ae,
    launch: r,
    status: d
  });
}
function is(e, t, { document: n = e.ownerDocument } = {}) {
  e.replaceChildren();
  const i = n.createElement("h2");
  i.textContent = "Quarantined wallet storage";
  const r = n.createElement("p");
  r.textContent = "These records are never unlocked automatically. Export or delete each exact storage key.";
  const s = n.createElement("div"), a = n.createElement("p");
  a.dataset.walletQuarantineStatus = "true", a.setAttribute?.("aria-live", "polite");
  const o = [];
  for (const c of t) {
    const l = n.createElement("div");
    l.className = "wallet-quarantine-row";
    const d = n.createElement("code");
    d.dataset.walletQuarantineLabel = "true", d.textContent = c.key;
    const u = n.createElement("span");
    u.textContent = c.oversized ? `Export unavailable (${c.rawLength} characters)` : `${c.rawLength} characters`;
    const f = n.createElement("button");
    f.type = "button", f.dataset.walletAction = "export-quarantined-wallet", f.dataset.walletQuarantineKey = c.key, f.textContent = c.exportable ? "Export" : "Export unavailable";
    const p = n.createElement("button");
    p.type = "button", p.dataset.walletAction = "delete-quarantined-wallet", p.dataset.walletQuarantineKey = c.key, p.textContent = "Delete";
    const g = n.createElement("div");
    g.hidden = !0;
    const A = n.createElement("p");
    A.textContent = `Type ${c.key} to confirm deletion.`;
    const h = n.createElement("input");
    h.type = "text", h.autocomplete = "off", h.spellcheck = !1, h.dataset.walletQuarantineConfirmation = c.key;
    const E = n.createElement("button");
    E.type = "button", E.dataset.walletAction = "confirm-delete-quarantined-wallet", E.dataset.walletQuarantineKey = c.key, E.textContent = "Confirm delete";
    const m = n.createElement("button");
    m.type = "button", m.dataset.walletAction = "cancel-delete-quarantined-wallet", m.dataset.walletQuarantineKey = c.key, m.textContent = "Cancel", g.append(A, h, E, m), l.append(d, u, f, p, g), s.append(l), o.push(Object.freeze({
      cancel: m,
      confirm: E,
      confirmation: h,
      confirmationGroup: g,
      deleteButton: p,
      entry: c,
      exportButton: f
    }));
  }
  return e.append(i, r, s, a), Object.freeze({ rows: Object.freeze(o), status: a });
}
async function rs(e, {
  assertCurrent: t = () => {
  },
  clipboard: n = globalThis.navigator?.clipboard,
  container: i,
  document: r = i?.ownerDocument
}) {
  const s = es(e);
  let a = !1;
  try {
    if (!n?.writeText) throw new Error("clipboard unavailable");
    await n.writeText(s), a = !0;
  } catch {
  }
  if (t(), !a && i && r) {
    const o = r.createElement("textarea");
    o.readOnly = !0, o.value = s, o.textContent = s, i.replaceChildren(o);
    try {
      o.select?.();
    } catch {
    }
  }
  return a;
}
async function Ln(e, t, n, {
  assertCurrent: i,
  ownBuffer: r,
  ownHandle: s,
  releaseBuffer: a
}) {
  const o = await t(n), c = o?.usernameControl, l = o?.passwordControl;
  let d, u;
  try {
    i();
    const p = c?.value, g = l?.value;
    (typeof p != "string" || typeof g != "string") && M("CREDENTIAL_CONFIRMATION_MISMATCH"), (!Me(p) || !Me(g)) && M("CREDENTIAL_CONFIRMATION_MISMATCH"), d = In.encode(p), r(d), u = In.encode(g), r(u);
  } finally {
    Sn(c), Sn(l);
    const p = c?.form, g = l?.form;
    try {
      (p?.parentNode ?? p)?.remove?.();
    } catch {
    }
    if (g !== p)
      try {
        (g?.parentNode ?? g)?.remove?.();
      } catch {
      }
  }
  let f;
  try {
    i(), f = await e.derivePasswordIdentity({
      accountIndex: 0,
      passwordUtf8: u,
      usernameUtf8: d
    }), f?.handle || M("CREDENTIAL_CONFIRMATION_MISMATCH"), s(f.handle), i();
    let p;
    try {
      p = me(f.identity);
    } catch {
      M("CREDENTIAL_CONFIRMATION_MISMATCH");
    }
    return { handle: f.handle, identity: p };
  } finally {
    a(d), a(u);
  }
}
class ss {
  #d = null;
  #m;
  #h = !1;
  #a = /* @__PURE__ */ new Set();
  #f;
  #v = 0;
  #e = 0;
  #n = /* @__PURE__ */ new Set();
  #r = /* @__PURE__ */ new Set();
  #t;
  constructor({ wasm: t, credentialRound: n, expectedIdentity: i = null }) {
    this.#t = t?.sdn ?? t, this.#m = n;
    try {
      this.#f = i === null ? null : me(i);
    } catch {
      M("CREDENTIAL_CONFIRMATION_MISMATCH");
    }
    (typeof this.#t?.derivePasswordIdentity != "function" || typeof this.#t?.destroySdnIdentity != "function" || typeof n != "function") && M("CREDENTIAL_CONFIRMATION_MISMATCH");
  }
  get confirmed() {
    return this.#d;
  }
  clear() {
    return this.#d = null, this.#h || (this.#h = !0, this.#v += 1, this.#f = null), this.#o(), this.#R(), this.#e === 0 && this.#n.size === 0;
  }
  destroy() {
    return this.clear();
  }
  async confirm() {
    if (this.#h && M("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#d) return this.#d;
    this.#e += 1;
    const t = this.#v;
    let n, i;
    try {
      this.#R(), this.#n.size !== 0 && M("CREDENTIAL_CONFIRMATION_MISMATCH");
      const s = {
        assertCurrent: () => this.#p(t),
        ownBuffer: (a) => this.#r.add(a),
        ownHandle: (a) => this.#n.add(a),
        releaseBuffer: (a) => {
          vn(a), this.#r.delete(a);
        }
      };
      return n = await Ln(this.#t, this.#m, 1, s), this.#S(n.handle) || M("CREDENTIAL_CONFIRMATION_MISMATCH"), n.handle = null, this.#p(t), i = await Ln(this.#t, this.#m, 2, s), this.#S(i.handle) || M("CREDENTIAL_CONFIRMATION_MISMATCH"), i.handle = null, this.#p(t), (!Ut(n.identity, i.identity) || this.#f !== null && (!Ut(n.identity, this.#f) || !Ut(i.identity, this.#f))) && M("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#n.size !== 0 && M("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#d = oi(i.identity), this.#d;
    } catch (r) {
      if (this.#d = null, r instanceof si && r.code === "CREDENTIAL_CONFIRMATION_MISMATCH") throw r;
      M("CREDENTIAL_CONFIRMATION_MISMATCH");
    } finally {
      this.#o(), this.#R(), this.#e -= 1;
    }
  }
  #p(t) {
    (this.#h || this.#v !== t) && M("CREDENTIAL_CONFIRMATION_MISMATCH");
  }
  #S(t) {
    if (!this.#n.has(t)) return !0;
    if (this.#a.has(t)) return !1;
    this.#a.add(t);
    try {
      return this.#t.destroySdnIdentity(t), this.#n.delete(t), !0;
    } catch {
      return !1;
    } finally {
      this.#a.delete(t);
    }
  }
  #R() {
    for (const t of [...this.#n]) this.#S(t);
  }
  #o() {
    const t = [...this.#r];
    this.#r.clear();
    for (const n of t) vn(n);
  }
}
async function os({
  wasm: e,
  profile: t,
  operation: n,
  credentials: i,
  accountIndex: r = 0,
  assertCurrent: s = () => {
  },
  ownHandle: a = () => {
  }
}) {
  const o = e?.sdn ?? e;
  let c;
  return t === "password-fast-v1-legacy" ? (typeof o?.deriveLegacyPasswordIdentity != "function" && M("INVALID_LEGACY_PROFILE"), c = await o.deriveLegacyPasswordIdentity({ accountIndex: r, ...i })) : t === "bip39-mnemonic-v1-legacy" ? (typeof o?.importLegacyMnemonicIdentity != "function" && M("INVALID_LEGACY_PROFILE"), c = await o.importLegacyMnemonicIdentity({ accountIndex: r, ...i })) : M("INVALID_LEGACY_PROFILE"), c?.handle !== null && c?.handle !== void 0 && a(c.handle), s(), Object.freeze({
    approval: null,
    handle: c.handle,
    identity: ke(c.identity),
    legacy: !0
  });
}
function as(e, { document: t = e.ownerDocument } = {}) {
  e.replaceChildren();
  const n = t.createElement("h2");
  n.textContent = "Migrate legacy wallet";
  const i = t.createElement("p");
  i.textContent = "Select the exact legacy profile and compare its legacy xpub and authentication key.";
  const r = t.createElement("select");
  r.dataset.walletLegacyProfile = "required";
  for (const [o, c] of [
    ["password-fast-v1-legacy", "Legacy fast-password profile"],
    ["bip39-mnemonic-v1-legacy", "Legacy BIP-39 mnemonic import"]
  ]) {
    const l = t.createElement("option");
    l.value = o, l.textContent = c, r.append(l);
  }
  const s = t.createElement("button");
  s.type = "button", s.dataset.walletAction = "launch-legacy-migration", s.textContent = "Compare selected legacy account";
  const a = t.createElement("div");
  return a.className = "wallet-legacy-comparison", e.append(n, i, r, s, a), Object.freeze({ launch: s, result: a, select: r });
}
const vt = Uint8Array, ai = ArrayBuffer, an = Object.getPrototypeOf(vt.prototype), cs = Object.getOwnPropertyDescriptor(an, "buffer").get, ls = Object.getOwnPropertyDescriptor(an, "byteLength").get, us = Object.getOwnPropertyDescriptor(an, "byteOffset").get, ds = Object.getOwnPropertyDescriptor(ai.prototype, "byteLength").get;
class fs extends Error {
  constructor(t = "RNG_FAILURE") {
    super(t), this.name = "WalletRandomError", this.code = t;
  }
}
function ve() {
  throw new fs();
}
function ps(e) {
  if (!(e instanceof vt) || Object.getPrototypeOf(e) !== vt.prototype) return null;
  let t, n, i;
  try {
    t = Reflect.apply(cs, e, []), n = Reflect.apply(ls, e, []), i = Reflect.apply(us, e, []);
  } catch {
    return null;
  }
  let r;
  try {
    r = Reflect.apply(ds, t, []);
  } catch {
    return null;
  }
  return Object.getPrototypeOf(t) !== ai.prototype || i !== 0 || n !== r || n !== 12 && n !== 32 ? null : t;
}
function hs({ getRandomValues: e, observedWrite: t } = {}) {
  if (typeof e != "function")
    return () => ve();
  const n = /* @__PURE__ */ new WeakSet(), i = /* @__PURE__ */ new WeakSet();
  return function(s) {
    const a = ps(s);
    (!a || n.has(s) || i.has(a)) && ve(), n.add(s), i.add(a);
    let o;
    try {
      o = e(s);
    } catch {
      ve();
    }
    if (o !== s && ve(), t !== void 0) {
      typeof t != "function" && ve();
      let c = !1;
      try {
        c = t(s) === !0;
      } catch {
        ve();
      }
      c || ve();
    }
    return s;
  };
}
function Ve(e, t) {
  (typeof e != "function" || t !== 12 && t !== 32) && ve();
  const n = new vt(t);
  return e(n);
}
const nt = new TextEncoder(), ys = new TextDecoder("utf-8", { fatal: !0 }), ms = Uint8Array.prototype.fill, gs = Uint8Array.prototype.slice, As = Uint8Array.prototype.subarray, cn = Object.getPrototypeOf(Uint8Array.prototype), Es = Object.getOwnPropertyDescriptor(cn, "buffer").get, ci = Object.getOwnPropertyDescriptor(cn, "byteLength").get, ws = Object.getOwnPropertyDescriptor(cn, "byteOffset").get, li = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength").get, bs = ArrayBuffer.prototype.slice, On = "webauthn-prf-hkdf-sha256-aes256gcm-v2", Is = "sdn-bip32-slip10-purpose-v1", vs = "password-scrypt-v2", Ss = "Space Data Network Wallet", ui = 12e4;
class Rs extends Error {
  constructor(t) {
    super(t), this.name = "RememberedWalletError", this.code = t;
  }
}
function _(e) {
  throw new Rs(e);
}
function Nn(e) {
  if (typeof e != "string") return "";
  const t = e.trim().toLowerCase();
  return t.endsWith(".") ? t.slice(0, -1) : t;
}
function Ls(e, t) {
  if (e == null || e === "") return;
  const n = Nn(e);
  n === "" && _("INVALID_RP_ID");
  const i = Nn(t);
  if (i === "" || n === i || i.endsWith(`.${n}`)) return n;
  _("INVALID_RP_ID");
}
function D(e) {
  if (e instanceof Uint8Array)
    try {
      ms.call(e, 0);
    } catch {
    }
}
function St(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function di(e, t) {
  if (!St(e)) return !1;
  let n;
  try {
    n = Reflect.ownKeys(e);
  } catch {
    return !1;
  }
  if (n.some((s) => typeof s != "string")) return !1;
  const i = [...n].sort(), r = [...t].sort();
  return i.length === r.length && i.every((s, a) => s === r[a]);
}
function it(e, t) {
  if (!di(e, t)) return null;
  const n = {};
  for (const i of t) {
    let r;
    try {
      r = Object.getOwnPropertyDescriptor(e, i);
    } catch {
      return null;
    }
    if (!r?.enumerable || !("value" in r) || r.value === void 0)
      return null;
    n[i] = r.value;
  }
  return n;
}
function ht(e, t = null) {
  if (!(e instanceof Uint8Array) || Object.getPrototypeOf(e) !== Uint8Array.prototype)
    return !1;
  let n, i, r, s;
  try {
    n = Reflect.apply(Es, e, []), i = Reflect.apply(ci, e, []), r = Reflect.apply(ws, e, []), s = Reflect.apply(li, n, []);
  } catch {
    return !1;
  }
  return Object.getPrototypeOf(n) === ArrayBuffer.prototype && r === 0 && i === s && (t === null || i === t);
}
function fi(e, t, n) {
  if (!(e instanceof ArrayBuffer) || Object.getPrototypeOf(e) !== ArrayBuffer.prototype)
    return !1;
  let i;
  try {
    i = Reflect.apply(li, e, []);
  } catch {
    return !1;
  }
  return i >= t && i <= n;
}
function De(e) {
  try {
    return Reflect.apply(ci, e, []);
  } catch {
    return -1;
  }
}
function Qe(e) {
  let t = "";
  const n = De(e);
  n < 0 && _("INVALID_REMEMBERED_WALLET");
  for (let i = 0; i < n; i += 32768)
    t += String.fromCharCode(...Reflect.apply(
      As,
      e,
      [i, i + 32768]
    ));
  return btoa(t).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
}
function Os(e) {
  let t = "";
  for (const n of e) t += n.toString(16).padStart(2, "0");
  return t;
}
function $e(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" || typeof e == "number" && Number.isFinite(e) ? JSON.stringify(e) : Array.isArray(e) ? `[${e.map($e).join(",")}]` : (St(e) || _("INVALID_REMEMBERED_WALLET"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${$e(e[t])}`).join(",")}}`);
}
function Ns(e, t) {
  if (!(e instanceof Uint8Array) || !(t instanceof Uint8Array)) return !1;
  const n = De(e), i = De(t);
  if (n < 0 || i < 0) return !1;
  const r = Math.max(n, i);
  let s = n ^ i;
  for (let a = 0; a < r; a += 1)
    s |= (e[a] ?? 0) ^ (t[a] ?? 0);
  return s === 0;
}
function Ts(e, t) {
  return $e(e) === $e(t);
}
function pi(e) {
  (!e || typeof e != "object" && typeof e != "function") && _("WEBAUTHN_INVALID_RESPONSE");
  let t, n, i;
  try {
    t = e.type, n = e.rawId, i = e.getClientExtensionResults;
  } catch {
    _("WEBAUTHN_INVALID_RESPONSE");
  }
  return (t !== "public-key" || !fi(n, 1, 1024) || typeof i != "function") && _("WEBAUTHN_INVALID_RESPONSE"), Object.freeze({
    rawId: new Uint8Array(Reflect.apply(bs, n, [0])),
    readExtensionResults() {
      try {
        return Reflect.apply(i, e, []);
      } catch {
        _("WEBAUTHN_PRF_REQUIRED");
      }
    }
  });
}
function Cs(e) {
  const t = it(e.readExtensionResults(), ["prf"]), n = t ? it(t.prf, ["enabled"]) : null;
  (!n || n.enabled !== !0) && _("WEBAUTHN_PRF_REQUIRED");
}
function _s(e, t) {
  const n = pi(e), i = n.rawId;
  Ns(i, t) || (D(i), _("WEBAUTHN_CREDENTIAL_MISMATCH")), D(i);
  const r = it(n.readExtensionResults(), ["prf"]), s = r ? it(r.prf, ["results"]) : null, o = (s ? it(s.results, ["first"]) : null)?.first;
  fi(o, 32, 32) || _("WEBAUTHN_PRF_REQUIRED");
  const c = new Uint8Array(o), l = Reflect.apply(gs, c, []);
  return D(c), l;
}
function xs({ challenge: e, credentialId: t, prfInput: n, rpId: i, signal: r }) {
  return {
    publicKey: {
      allowCredentials: [{
        id: t,
        type: "public-key"
      }],
      challenge: e,
      extensions: { prf: { eval: { first: n } } },
      ...i === void 0 ? {} : { rpId: i },
      timeout: ui,
      userVerification: "required"
    },
    signal: r
  };
}
async function Tn({ assertCurrent: e, credentials: t, credentialId: n, fillRandom: i, prfInput: r, rpId: s, signal: a }) {
  const o = Ve(i, 32);
  let c;
  try {
    c = await t.get(xs({
      challenge: o,
      credentialId: n,
      prfInput: r,
      rpId: s,
      signal: a
    }));
  } catch (l) {
    throw e(), l;
  } finally {
    D(o);
  }
  return e(), _s(c, n);
}
function Ds(e) {
  if (typeof e != "string") return !1;
  for (let t = 0; t < e.length; t += 1) {
    const n = e.charCodeAt(t);
    if (n >= 55296 && n <= 56319) {
      const i = e.charCodeAt(++t);
      if (!(i >= 56320 && i <= 57343)) return !1;
    } else if (n >= 56320 && n <= 57343)
      return !1;
  }
  return !0;
}
function Ps(e) {
  if (typeof e == "string")
    return Ds(e) || _("INVALID_USERNAME"), nt.encode(e).length > 256 && _("INVALID_USERNAME"), e;
  (!ht(e) || De(e) > 256) && _("INVALID_USERNAME");
  try {
    return ys.decode(e);
  } catch {
    _("INVALID_USERNAME");
  }
}
function Ht(e) {
  const n = Ps(e).replace(/^ +/u, "").replace(/ +$/u, "").replace(/[A-Z]/gu, (r) => r.toLowerCase()), i = nt.encode(n);
  return (i.length < 3 || i.length > 64 || !/^[a-z0-9][a-z0-9._-]*$/u.test(n)) && _("INVALID_USERNAME"), n;
}
function ks(e) {
  let t;
  try {
    t = Bn(e?.module);
  } catch {
    _("WASM_UNAVAILABLE");
  }
  return (!di(t, ["sdn", "sha256"]) || typeof t.sha256 != "function" || !t.sdn || typeof t.sdn != "object") && _("WASM_UNAVAILABLE"), t;
}
function Us(e) {
  const t = ks(e), n = t.sdn, i = e?.storage, r = e?.credentials, s = hs(e?.rng), a = e?.createRequestController, o = e?.releaseRequestController, c = e?.ownHandle, l = e?.destroyHandle, d = e?.ownedHandlesClean, u = e?.now ?? (() => /* @__PURE__ */ new Date()), f = e?.rpHostname ?? e?.window?.location?.hostname ?? globalThis?.location?.hostname, p = Ls(e?.rpId, f), g = typeof e?.rpName == "string" && e.rpName !== "" ? e.rpName : Ss, A = () => typeof r?.create == "function" && typeof r?.get == "function", h = () => ri(i), E = () => zr(i), m = (N) => qr(i, N), O = (N, F) => (Hr(i, N, F), E().some((b) => b.key === N) && _("STORAGE_WRITE_FAILED"), !0), I = () => h().active.status === "valid", B = ({ confirmation: N } = {}) => (I() || _("REMEMBER_UNAVAILABLE"), Gr(i, N), h().active.status !== "empty" && _("STORAGE_WRITE_FAILED"), !0), P = async (N) => {
    let F;
    try {
      F = await t.sha256(nt.encode(N));
    } catch {
      _("WASM_FAILURE");
    }
    return ht(F, 32) || _("WASM_FAILURE"), Os(F);
  };
  return Object.freeze({
    canForget: I,
    deleteQuarantine: O,
    exportQuarantine: m,
    forget: B,
    inspect: h,
    listQuarantine: E,
    restore: async ({ assertCurrent: N } = {}) => {
      (typeof N != "function" || !A() || typeof n.importRememberedIdentity != "function" || typeof c != "function" || typeof l != "function" || typeof a != "function" || typeof o != "function") && _("REMEMBER_UNAVAILABLE");
      const F = h();
      (!F.canRestore || !F.active.record) && _("STORAGE_QUARANTINED");
      const b = F.active.record;
      Ht(b.canonicalUsername) !== b.canonicalUsername && _("INVALID_REMEMBERED_WALLET"), await P(b.canonicalUsername) !== b.aad.usernameSha256 && _("INVALID_REMEMBERED_WALLET"), N();
      const T = Ae(b.credentialIdBase64url, {
        minimum: 1,
        maximum: 1024
      }), ie = Ae(b.prfInputBase64url, { exact: 32 }), Q = Ae(b.ciphertextBase64url, {
        minimum: 17,
        maximum: 1024
      }), k = Ae(b.hkdfSaltBase64url, { exact: 32 }), R = Ae(b.nonceBase64url, { exact: 12 }), z = nt.encode(b.canonicalUsername), Y = $e(b.aad), G = typeof a == "function" ? a() : null;
      (!G?.signal || typeof G.abort != "function") && (D(T), D(ie), D(Q), D(k), D(R), D(z), _("WEBAUTHN_UNAVAILABLE"));
      let K, te = null, re = !1;
      try {
        K = await Tn({
          assertCurrent: N,
          credentialId: T,
          credentials: r,
          fillRandom: s,
          prfInput: ie,
          rpId: p,
          signal: G.signal
        }), N();
        let ne;
        const se = K;
        try {
          ne = n.importRememberedIdentity({
            canonicalAad: Y,
            canonicalUsernameUtf8: z,
            ciphertextAndTag: Q,
            hkdfSalt: k,
            nonce: R,
            prfOutput: se
          });
        } finally {
          D(se), K = null;
        }
        St(ne) || _("WASM_FAILURE"), te = ne.handle, (te == null || typeof c != "function") && _("WASM_FAILURE"), c(te);
        const J = me(ne.identity);
        return N(), re = !0, Object.freeze({ handle: te, identity: J });
      } finally {
        !re && te !== null && typeof l == "function" && (l(te) || _("DESTRUCTION_FAILED")), D(K), D(T), D(ie), D(Q), D(k), D(R), D(z), typeof o == "function" && o(G);
      }
    },
    setup: async ({ assertCurrent: N, canonicalUsername: F, handle: b, identity: W, passwordUtf8: T }) => {
      try {
        (typeof N != "function" || b === null || b === void 0 || !ht(T) || De(T) === 0 || De(T) > 256 || Ht(F) !== F || !A() || typeof n.sealRememberedIdentity != "function" || typeof n.importRememberedIdentity != "function" || typeof c != "function" || typeof l != "function" || typeof d != "function" || typeof a != "function" || typeof o != "function") && (D(T), _("REMEMBER_UNAVAILABLE"));
        const ie = me(W);
        h().canSetup || (D(T), _("STORAGE_QUARANTINED"));
        const k = typeof a == "function" ? a() : null;
        (!k?.signal || typeof k.abort != "function") && (D(T), _("WEBAUTHN_UNAVAILABLE"));
        let R, z, Y, G, K, te, re, ne, se, J = null, Oe = null;
        try {
          R = Ve(s, 32), z = Ve(s, 32), Y = Ve(s, 32), G = Ve(s, 32), K = Ve(s, 12), N();
          let Ue;
          try {
            Ue = await r.create({
              publicKey: {
                attestation: "none",
                authenticatorSelection: {
                  residentKey: "preferred",
                  userVerification: "required"
                },
                challenge: R,
                extensions: { prf: {} },
                pubKeyCredParams: [
                  { alg: -7, type: "public-key" },
                  { alg: -257, type: "public-key" }
                ],
                rp: p === void 0 ? { name: g } : { id: p, name: g },
                timeout: ui,
                user: {
                  displayName: F,
                  id: z,
                  name: F
                }
              },
              signal: k.signal
            });
          } catch (Ce) {
            throw N(), Ce;
          }
          N();
          const ze = pi(Ue);
          te = ze.rawId, Cs(ze), re = await Tn({
            assertCurrent: N,
            credentialId: te,
            credentials: r,
            fillRandom: s,
            prfInput: Y,
            rpId: p,
            signal: k.signal
          }), ne = re.slice(), se = re.slice(), D(re), re = null;
          const be = await P(F);
          N();
          const je = Qe(te), qe = Object.freeze({
            credentialIdBase64url: je,
            identityScheme: Is,
            schemaVersion: 2,
            seedProfile: vs,
            storageProfile: On,
            usernameSha256: be
          }), He = $e(qe);
          let Ie;
          try {
            Ie = n.sealRememberedIdentity(b, {
              canonicalAad: He,
              hkdfSalt: G,
              nonce: K,
              passwordUtf8: T,
              prfOutput: ne
            });
          } finally {
            D(T), D(ne);
          }
          T = null, ne = null;
          const Ne = De(Ie);
          (!ht(Ie) || Ne < 17 || Ne > 1024) && _("WASM_FAILURE");
          let Ge;
          try {
            const Ce = u();
            Ge = Ce instanceof Date ? Ce.toISOString() : new Date(Ce).toISOString();
          } catch {
            _("CLOCK_FAILURE");
          }
          const ct = Object.freeze({
            aad: qe,
            canonicalUsername: F,
            ciphertextBase64url: Qe(Ie),
            createdAt: Ge,
            credentialIdBase64url: je,
            hkdfSaltBase64url: Qe(G),
            nonceBase64url: Qe(K),
            prfInputBase64url: Qe(Y),
            schemaVersion: 2,
            storageProfile: On
          });
          N(), Oe = $r(i, ct);
          let Te;
          const Ke = se;
          try {
            Te = n.importRememberedIdentity({
              canonicalAad: He,
              canonicalUsernameUtf8: nt.encode(F),
              ciphertextAndTag: Ie.slice(),
              hkdfSalt: G.slice(),
              nonce: K.slice(),
              prfOutput: Ke
            });
          } finally {
            D(Ke), se = null;
          }
          St(Te) || _("WASM_FAILURE"), J = Te.handle, (J == null || typeof c != "function") && _("WASM_FAILURE"), c(J);
          const lt = me(Te.identity);
          return Ts(ie, lt) || _("IDENTITY_MISMATCH"), N(), (typeof l != "function" || !l(J)) && _("DESTRUCTION_FAILED"), J = null, (typeof d != "function" || !d(b)) && _("DESTRUCTION_FAILED"), N(), Fr(i, Oe), Oe = null, Object.freeze({ remembered: !0 });
        } finally {
          J !== null && typeof l == "function" && l(J), D(T), D(R), D(z), D(re), D(ne), D(se), D(G), D(K), D(te), typeof o == "function" && o(k);
        }
      } finally {
        D(T);
      }
    },
    supported: A
  });
}
const rt = /^[0-9a-f]{64}$/u, js = /^[A-Za-z0-9_-]{43}$/u, Bs = Object.freeze([
  "callbackUri",
  "clientDisplayName",
  "clientId",
  "expiresAt",
  "operation",
  "registryVersion",
  "request",
  "requestOrigin",
  "requestSha256",
  "resultToken",
  "schemaVersion",
  "state",
  "transactionId"
]), Vs = Object.freeze(["redirectUri", "schemaVersion", "transactionId"]), Ms = 64 * 1024, $s = 4 * 1024, Fs = new TextEncoder(), Ws = new TextDecoder("utf-8", { fatal: !0 });
class Rt extends Error {
  constructor() {
    super("RELAY_FAILURE"), this.name = "WalletRelayError", this.code = "RELAY_FAILURE";
  }
}
function x() {
  throw new Rt();
}
function hi(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function yi(e, t) {
  hi(e) || x();
  let n;
  try {
    n = Reflect.ownKeys(e);
  } catch {
    x();
  }
  n.some((s) => typeof s != "string") && x();
  const i = [...n].sort(), r = [...t].sort();
  (i.length !== r.length || i.some((s, a) => s !== r[a])) && x();
  for (const s of i) {
    let a;
    try {
      a = Object.getOwnPropertyDescriptor(e, s);
    } catch {
      x();
    }
    (!a?.enumerable || !("value" in a) || a.value === void 0) && x();
  }
  return e;
}
function Gt(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(Gt));
  if (!hi(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = Gt(e[n]);
  return Object.freeze(t);
}
function zs(e, t) {
  (typeof e != "string" || Fs.encode(e).byteLength > t || e.charCodeAt(0) === 65279) && x();
  let n = 0, i = 0;
  const r = () => {
    for (; n < e.length && /[\u0009\u000a\u000d\u0020]/u.test(e[n]); ) n += 1;
  }, s = () => {
    e[n] !== '"' && x();
    const c = n;
    for (n += 1; n < e.length; ) {
      const l = e.charCodeAt(n);
      if (l === 34) {
        n += 1;
        let d;
        try {
          d = JSON.parse(e.slice(c, n));
        } catch {
          x();
        }
        for (let u = 0; u < d.length; u += 1) {
          const f = d.charCodeAt(u);
          if (f >= 55296 && f <= 56319) {
            const p = d.charCodeAt(u + 1);
            p >= 56320 && p <= 57343 || x(), u += 1;
          } else f >= 56320 && f <= 57343 && x();
        }
        return d;
      }
      l < 32 && x(), l === 92 && (n += 1, n >= e.length && x()), n += 1;
    }
    x();
  }, a = (c = 0) => {
    (c > 32 || ++i > 4096) && x(), r();
    const l = e[n];
    if (l === '"') return s();
    if (l === "{") {
      n += 1;
      const f = /* @__PURE__ */ Object.create(null), p = /* @__PURE__ */ new Set();
      if (r(), e[n] === "}")
        return n += 1, f;
      for (; n < e.length; ) {
        r();
        const g = s();
        if (p.has(g) && x(), p.add(g), r(), e[n] !== ":" && x(), n += 1, f[g] = a(c + 1), r(), e[n] === "}")
          return n += 1, f;
        e[n] !== "," && x(), n += 1;
      }
      x();
    }
    if (l === "[") {
      n += 1;
      const f = [];
      if (r(), e[n] === "]")
        return n += 1, f;
      for (; n < e.length; ) {
        if (f.push(a(c + 1)), r(), e[n] === "]")
          return n += 1, f;
        e[n] !== "," && x(), n += 1;
      }
      x();
    }
    for (const [f, p] of [["true", !0], ["false", !1], ["null", null]])
      if (e.startsWith(f, n))
        return n += f.length, p;
    const d = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(e.slice(n));
    d || x(), n += d[0].length;
    const u = Number(d[0]);
    return Number.isFinite(u) || x(), u;
  };
  r();
  const o = a();
  return r(), n !== e.length && x(), o;
}
function Cn(e) {
  try {
    Promise.resolve(e).catch(() => {
    });
  } catch {
  }
}
function mi(e, t = null) {
  try {
    const n = t?.cancel;
    typeof n == "function" && Cn(Reflect.apply(n, t, []));
  } catch {
  }
  if (!t)
    try {
      const n = e?.body, i = n?.cancel;
      typeof i == "function" && Cn(Reflect.apply(i, n, []));
    } catch {
    }
}
function gi(e, t, n = null) {
  let i;
  try {
    i = Promise.resolve(e);
  } catch {
    return Promise.reject(new Rt());
  }
  return t ? new Promise((r, s) => {
    let a = !1;
    const o = () => {
      try {
        t.removeEventListener?.("abort", l);
      } catch {
      }
    }, c = (d, u) => a ? !1 : (a = !0, o(), d(u), !0), l = () => c(s, new Rt());
    try {
      t.addEventListener?.("abort", l, { once: !0 }), t.aborted === !0 && l();
    } catch {
      l();
    }
    i.then(
      (d) => {
        if (!c(r, d))
          try {
            n?.(d);
          } catch {
          }
      },
      (d) => {
        c(s, d);
      }
    );
  }) : i;
}
async function qs(e, t, n, i) {
  let r = null;
  try {
    (!e || e.status !== t || e.redirected === !0 || e.headers?.get?.("cache-control")?.trim().toLowerCase() !== "no-store" || e.headers?.get?.("content-type")?.trim().toLowerCase() !== "application/json; charset=utf-8" || typeof e.body?.getReader != "function") && x(), r = e.body.getReader(), (!r || typeof r.read != "function" || i?.aborted === !0) && x();
    const s = [];
    let a = 0;
    for (; ; ) {
      i?.aborted === !0 && x();
      const { done: l, value: d } = await gi(r.read(), i);
      if (i?.aborted === !0 && x(), l) break;
      d instanceof Uint8Array || x(), a += d.byteLength, a > n && x(), s.push(d);
    }
    const o = new Uint8Array(a);
    let c = 0;
    for (const l of s)
      o.set(l, c), c += l.byteLength;
    return zs(Ws.decode(o), n);
  } catch (s) {
    if (mi(e, r), s instanceof Rt) throw s;
    x();
  } finally {
    try {
      r?.releaseLock?.();
    } catch {
    }
  }
}
function _n(e, t) {
  return yi(e, Bs), (e.schemaVersion !== 1 || e.transactionId !== t || !rt.test(e.transactionId) || !rt.test(e.state) || !rt.test(e.requestSha256) || !js.test(e.resultToken) || typeof e.callbackUri != "string" || typeof e.clientDisplayName != "string" || typeof e.clientId != "string" || typeof e.expiresAt != "string" || typeof e.operation != "string" || typeof e.registryVersion != "string" || typeof e.requestOrigin != "string") && x(), Gt(e);
}
function Kt(e, t) {
  yi(t, Vs), (t.schemaVersion !== 1 || t.transactionId !== e.transactionId || typeof t.redirectUri != "string") && x();
  const n = `${e.callbackUri}#code=`, i = `&state=${e.state}`;
  (!t.redirectUri.startsWith(n) || !t.redirectUri.endsWith(i)) && x();
  const r = t.redirectUri.slice(n.length, t.redirectUri.length - i.length);
  return (!rt.test(r) || t.redirectUri !== `${n}${r}${i}`) && x(), Object.freeze({
    redirectUri: t.redirectUri,
    schemaVersion: 1,
    transactionId: t.transactionId
  });
}
function xn(e, t, n = void 0) {
  const i = {
    cache: "no-store",
    credentials: "omit",
    headers: e === "POST" ? { Accept: "application/json", "Content-Type": "application/json" } : { Accept: "application/json" },
    method: e,
    mode: "same-origin",
    redirect: "error",
    referrerPolicy: "no-referrer",
    signal: t
  };
  return n !== void 0 && (i.body = JSON.stringify(n)), i;
}
function Hs({ fetch: e, location: t }) {
  (typeof e != "function" || typeof t?.replace != "function") && x();
  const n = /* @__PURE__ */ new Set(), i = async (r, s, a, o) => {
    let c;
    try {
      c = await gi(e(r, s), s.signal, (l) => mi(l));
    } catch {
      x();
    }
    return qs(c, a, o, s.signal);
  };
  return Object.freeze({
    async fetchTransaction(r, { signal: s } = {}) {
      (typeof r != "string" || !rt.test(r)) && x();
      const a = await i(
        `/relay/v1/transactions/${r}`,
        xn("GET", s),
        200,
        Ms
      );
      return _n(a, r);
    },
    async publishResult(r, s, { signal: a } = {}) {
      const o = _n(r, r?.transactionId), c = await i(
        `/relay/v1/transactions/${o.transactionId}/result`,
        xn("POST", a, {
          result: s,
          resultToken: o.resultToken,
          schemaVersion: 1,
          transactionId: o.transactionId
        }),
        201,
        $s
      ), l = Kt(o, c);
      return n.clear(), n.add(l.redirectUri), l;
    },
    navigate(r) {
      (typeof r != "string" || !n.delete(r)) && x();
      try {
        Reflect.apply(t.replace, t, [r]);
      } catch {
        x();
      }
    },
    revokeNow() {
      n.clear();
    },
    async destroy() {
      n.clear();
    }
  });
}
const Ai = Uint8Array, Gs = Ai.prototype.fill, Ye = new TextEncoder();
class V extends Error {
  constructor(t) {
    super(t), this.name = "WalletOriginError", this.code = t;
  }
}
function w(e) {
  throw new V(e);
}
function Xe(e) {
  throw e instanceof Le ? new V(e.code) : e;
}
function he(e) {
  if (e instanceof Ai)
    try {
      Gs.call(e, 0);
    } catch {
    }
}
function Ze(e) {
  if (typeof e != "string") return !1;
  for (let t = 0; t < e.length; t += 1) {
    const n = e.charCodeAt(t);
    if (n >= 55296 && n <= 56319) {
      const i = e.charCodeAt(++t);
      if (!(i >= 56320 && i <= 57343)) return !1;
    } else if (n >= 56320 && n <= 57343) return !1;
  }
  return !0;
}
function Dn(e) {
  if (!(!e || typeof e != "object")) {
    try {
      e.type === "checkbox" && (e.checked = !1);
    } catch {
    }
    try {
      e.type === "checkbox" && (e.defaultChecked = !1);
    } catch {
    }
    for (const t of ["value", "defaultValue"])
      try {
        e[t] = "";
      } catch {
      }
    try {
      e.disabled = !0;
    } catch {
    }
    try {
      e.inert = !0;
    } catch {
    }
    for (const t of ["name", "autocomplete"])
      try {
        e.removeAttribute?.(t);
      } catch {
      }
    try {
      e.setSelectionRange?.(0, 0);
    } catch {
    }
    try {
      e.setCustomValidity?.("");
    } catch {
    }
    for (const t of ["onbeforeinput", "onchange", "oninput", "onkeydown", "onkeyup", "onpaste"])
      try {
        e[t] = null;
      } catch {
      }
  }
}
function et(e, t) {
  const n = /* @__PURE__ */ new Set();
  for (const i of t) {
    let r = null;
    try {
      r = i?.form ?? i?.closest?.("form") ?? null;
    } catch {
      r = null;
    }
    r && n.add(r), Dn(i);
  }
  for (const i of n) {
    let r = [];
    try {
      r = i.querySelectorAll?.("input, textarea") ?? [];
    } catch {
      r = [];
    }
    for (const s of r) Dn(s);
    try {
      i.inert = !0, i.setAttribute?.("aria-hidden", "true");
      const s = e.createElement("div");
      s.dataset.walletCredentialState = "cleared", i.replaceWith?.(s), i.replaceChildren?.();
    } catch {
      try {
        i.remove?.();
      } catch {
      }
    }
  }
}
function Ks(e) {
  let t;
  try {
    t = Bn(e);
  } catch {
    w("WASM_UNAVAILABLE");
  }
  const n = t?.sdn, i = t?.sha256;
  return (!n || typeof n.derivePasswordIdentity != "function" || typeof n.destroySdnIdentity != "function" || typeof i != "function") && w("WASM_UNAVAILABLE"), Object.freeze({ capabilities: n, sha256: i });
}
function dt(e) {
  const t = e?.AbortController ?? globalThis.AbortController;
  return typeof t == "function" ? new t() : null;
}
class Js {
  #d = !1;
  #m;
  #h = null;
  #a = /* @__PURE__ */ new Set();
  #f = /* @__PURE__ */ new Set();
  #v = null;
  #e = !1;
  #n;
  #r = 0;
  #t = null;
  #p = null;
  #S = [];
  #R = /* @__PURE__ */ new Set();
  #o = /* @__PURE__ */ new Set();
  #g = null;
  #C = null;
  #D = /* @__PURE__ */ new WeakSet();
  #P = /* @__PURE__ */ new WeakMap();
  #u = null;
  #_;
  #y;
  #c;
  #V = !1;
  #A = null;
  #i = !1;
  #L = 0;
  #l = /* @__PURE__ */ new Set();
  #x;
  #O = /* @__PURE__ */ new Set();
  #s;
  constructor({
    credentials: t,
    document: n,
    now: i,
    registry: r,
    relay: s,
    rng: a,
    rpId: o,
    rpName: c,
    storage: l,
    wasm: d,
    window: u
  }) {
    const f = Ks(d);
    this.#m = f.capabilities, this.#x = f.sha256, this.#_ = r, this.#c = s, this.#n = n, this.#s = u, this.#y = Us({
      createRequestController: () => {
        const p = dt(this.#s);
        return p && (this.#a.add(p), this.#O.add(p)), p;
      },
      credentials: t ?? u?.navigator?.credentials ?? globalThis.navigator?.credentials,
      rpHostname: u?.location?.hostname ?? globalThis?.location?.hostname,
      rpId: o,
      rpName: c,
      destroyHandle: (p) => this.#E(p),
      module: d,
      now: i,
      ownHandle: (p) => this.#o.add(p),
      ownedHandlesClean: (p) => this.#o.size === 1 && this.#o.has(p) && this.#t === p,
      releaseRequestController: (p) => {
        this.#O.delete(p), this.#a.delete(p);
      },
      rng: a ?? {
        getRandomValues: u?.crypto?.getRandomValues?.bind(u.crypto) ?? globalThis.crypto?.getRandomValues?.bind(globalThis.crypto)
      },
      storage: l ?? u?.localStorage ?? globalThis.localStorage
    }), this.#F();
  }
  get generation() {
    return this.#r;
  }
  #k() {
    return this.#r = this.#r >= Number.MAX_SAFE_INTEGER ? 1 : this.#r + 1, this.#r;
  }
  #F() {
    const t = (n, i, r) => {
      n?.addEventListener?.(i, r), this.#S.push([n, i, r]);
    };
    t(this.#s, "pagehide", () => this.revokeNow("pagehide")), t(this.#n, "freeze", () => this.revokeNow("freeze")), t(this.#s, "beforeunload", () => this.revokeNow("beforeunload")), t(this.#s, "pageshow", (n) => {
      if (n?.persisted === !0) {
        this.revokeNow("bfcache-restore");
        try {
          this.#s.location?.reload?.();
        } catch {
        }
      }
    });
  }
  #W() {
    for (const [t, n, i] of this.#S)
      try {
        t?.removeEventListener?.(n, i);
      } catch {
      }
    this.#S = [];
  }
  #E(t = this.#t) {
    if (t == null) return !0;
    if (this.#o.add(t), this.#R.has(t)) return !1;
    this.#R.add(t);
    let n = !1;
    try {
      this.#m.destroySdnIdentity(t), n = !0;
    } catch {
      n = !1;
    } finally {
      this.#R.delete(t);
    }
    return n ? (this.#o.delete(t), this.#t === t && (this.#t = null, this.#p = null), !0) : !1;
  }
  #w() {
    let t = !0;
    for (const n of [...this.#o])
      this.#E(n) || (t = !1);
    return t;
  }
  #M() {
    const t = [...this.#a];
    this.#a.clear(), this.#O.clear();
    for (const n of t)
      try {
        n.abort();
      } catch {
      }
  }
  #z() {
    const t = [...this.#O];
    this.#O.clear();
    for (const n of t) {
      this.#a.delete(n);
      try {
        n.abort();
      } catch {
      }
    }
  }
  #U() {
    const t = [...this.#l];
    this.#l.clear();
    for (const n of t) he(n);
  }
  #N(t = []) {
    if (this.#f.size === 0) return;
    const n = new Set(t), i = [];
    for (const r of this.#f)
      n.has(r) || i.push(r);
    this.#f = new Set(
      [...this.#f].filter((r) => n.has(r))
    ), et(this.#n, i);
  }
  #j(t = []) {
    (this.#e || this.#i) && w("STALE_CONTROLLER");
    const n = this.#k();
    this.#N(t), this.#z(), this.#U();
    let i = this.#E();
    return i && (i = this.#w() && this.#o.size === 0), i || (this.revokeNow("native-destruction-failed"), this.#w(), w("DESTRUCTION_FAILED")), this.#A = null, n;
  }
  #B(t, n, {
    allowPublication: i = !1,
    retainIdentity: r = !1
  } = {}) {
    const s = !this.#e && !this.#i && this.#r === n && this.#t === t, a = this.#t === t || this.#o.has(t);
    if (!s) {
      const f = !a || this.#E(t);
      return f || (this.revokeNow("native-destruction-failed"), this.#w()), Object.freeze({ destroyed: f, permit: null });
    }
    const o = this.#L, c = i ? Object.freeze({}) : null;
    this.#k(), this.#i = !0, this.#d = !1, this.#u = c, this.#o.add(t), this.#t = null, this.#p = null, r || (this.#A = null);
    const l = this.#h;
    this.#h = null;
    let d = this.#E(t);
    d && (d = this.#w() && this.#o.size === 0), d || (this.#u = null, this.#A = null);
    try {
      l?.cancel("STALE_CONTROLLER");
    } catch {
    }
    try {
      l?.destroy();
    } catch {
    }
    this.#N(), this.#U(), this.#M(), d || this.#w();
    const u = d && this.#L === o && this.#u === c ? c : null;
    return u === null && this.#u === c && (this.#u = null), Object.freeze({ destroyed: d, permit: u });
  }
  #b(t, n) {
    (this.#e || this.#i || this.#r !== n || this.#t !== t) && w("STALE_CONTROLLER");
  }
  #T(t) {
    (this.#e || this.#i || this.#r !== t) && w("STALE_CONTROLLER");
  }
  #I(t, n) {
    (this.#e || this.#L !== t || this.#o.size !== 0 || n === null || this.#u !== n) && w("STALE_CONTROLLER");
  }
  #$(t) {
    if (this.#C) return this.#C;
    const n = this.#g;
    if (!n || t !== "connected" && t !== "disconnected")
      return Promise.reject(new V("STALE_CONTROLLER"));
    this.#g = null, this.#A = null;
    let i, r;
    const s = new Promise((a, o) => {
      i = a, r = o;
    });
    return this.#C = s, (async () => {
      const a = dt(this.#s);
      a && this.#a.add(a);
      try {
        this.#I(n.epoch, n.permit), oe({ document: this.#n, window: this.#s }), typeof this.#c?.publishResult != "function" && w("RELAY_UNAVAILABLE");
        const o = t === "connected" ? n.connectedResult : Tr();
        let c;
        try {
          c = await this.#c.publishResult(n.transaction, o, {
            signal: a?.signal
          });
        } catch (l) {
          throw this.#I(n.epoch, n.permit), l;
        }
        if (this.#I(n.epoch, n.permit), oe({ document: this.#n, window: this.#s }), typeof this.#c?.navigate == "function") {
          const l = Kt(n.transaction, c);
          this.#I(n.epoch, n.permit), this.#u = null, this.#c.navigate(l.redirectUri);
        } else
          this.#u = null;
        i(c);
      } catch (o) {
        this.#u === n.permit && (this.#u = null);
        try {
          this.#c?.revokeNow?.("account-publication-failed");
        } catch {
        }
        r(o);
      } finally {
        a && this.#a.delete(a);
      }
    })(), s;
  }
  registerCredentialControls({ usernameControl: t, passwordControl: n }) {
    (this.#e || this.#i) && w("STALE_CONTROLLER"), (!t || !n) && w("INVALID_CREDENTIAL_FORM"), this.#f.add(t), this.#f.add(n);
  }
  copyPublicIdentity() {
    const t = this.#p ?? this.#A;
    t || w("STALE_CONTROLLER");
    try {
      return me(t);
    } catch {
      w("WASM_FAILURE");
    }
  }
  supportsRememberedWallet() {
    return !this.#e && !this.#i && this.#y.supported();
  }
  canRestoreRememberedWallet() {
    if (!this.supportsRememberedWallet()) return !1;
    try {
      return this.#y.inspect().canRestore === !0;
    } catch {
      return !1;
    }
  }
  isUiGenerationCurrent(t) {
    return Number.isSafeInteger(t) && t === this.#r && !this.#e && (!this.#i || this.#g !== null);
  }
  listQuarantinedWalletRecords() {
    (this.#e || this.#i && !this.#g) && w("STALE_CONTROLLER");
    try {
      return this.#y.listQuarantine();
    } catch (t) {
      throw typeof t?.code == "string" ? new V(t.code) : t;
    }
  }
  exportQuarantinedWalletRecord(t) {
    (this.#e || this.#i && !this.#g) && w("STALE_CONTROLLER");
    try {
      return this.#y.exportQuarantine(t);
    } catch (n) {
      throw typeof n?.code == "string" ? new V(n.code) : n;
    }
  }
  deleteQuarantinedWalletRecord(t, n) {
    (this.#e || this.#i && !this.#g) && w("STALE_CONTROLLER");
    try {
      return this.#y.deleteQuarantine(t, n);
    } catch (i) {
      throw typeof i?.code == "string" ? new V(i.code) : i;
    }
  }
  canForgetRememberedWallet() {
    if (this.#e || !this.#A || !this.#g) return !1;
    try {
      return this.#y.canForget() === !0;
    } catch {
      return !1;
    }
  }
  forgetRememberedWallet(t) {
    this.canForgetRememberedWallet() || w("REMEMBER_UNAVAILABLE");
    try {
      return this.#y.forget({ confirmation: t });
    } catch (n) {
      throw typeof n?.code == "string" ? new V(n.code) : n;
    }
  }
  async unlockPassword({
    accountIndex: t = 0,
    passwordControl: n,
    rememberControl: i = null,
    rememberStatus: r = null,
    usernameControl: s
  }) {
    (this.#e || this.#i) && w("STALE_CONTROLLER"), t !== 0 && w("INVALID_ACCOUNT"), (!s || !n) && w("INVALID_CREDENTIAL_FORM");
    try {
      oe({ document: this.#n, window: this.#s });
    } catch (h) {
      et(
        this.#n,
        [s, n, i].filter(Boolean)
      ), Xe(h);
    }
    let a;
    try {
      a = this.#j([s, n]);
    } catch (h) {
      throw et(
        this.#n,
        [s, n, i].filter(Boolean)
      ), h;
    }
    this.registerCredentialControls({ passwordControl: n, usernameControl: s });
    let o, c, l, d, u, f, p = null, g = !1;
    try {
      try {
        oe({ document: this.#n, window: this.#s });
      } catch (E) {
        Xe(E);
      }
      this.#T(a);
      const h = s?.value;
      p = n?.value, Ze(h) || w("INVALID_USERNAME"), Ze(p) || w("INVALID_PASSWORD");
      try {
        o = Ht(h);
      } catch {
        w("INVALID_USERNAME");
      }
      c = Ye.encode(h), f = Ye.encode(p), g = i?.checked === !0 && i?.disabled !== !0 && this.#y.supported(), g ? ((f.length === 0 || f.length > 256) && w("INVALID_PASSWORD"), l = f.slice(), d = f.slice(), u = d, he(f), f = null) : (l = f, f = null), this.#l.add(c), this.#l.add(l), d && this.#l.add(d);
    } finally {
      if (p = null, i && typeof i == "object") {
        try {
          i.checked = !1;
        } catch {
        }
        try {
          i.defaultChecked = !1;
        } catch {
        }
        try {
          i.disabled = !0;
        } catch {
        }
      }
      he(f), this.#N();
    }
    let A;
    try {
      A = await this.#m.derivePasswordIdentity({
        accountIndex: t,
        passwordUtf8: l,
        usernameUtf8: c
      }), (!A || typeof A != "object") && w("WASM_FAILURE");
      const h = A.handle;
      h == null && w("WASM_FAILURE"), this.#o.add(h);
      let E;
      try {
        E = me(A.identity);
      } catch {
        const m = this.#E(h);
        A = null, m || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("WASM_FAILURE");
      }
      if ((this.#e || this.#i || this.#r !== a) && (this.#E(h) || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("STALE_CONTROLLER")), this.#t = h, this.#p = E, g)
        try {
          await this.#y.setup({
            assertCurrent: () => this.#b(h, a),
            canonicalUsername: o,
            handle: h,
            identity: E,
            passwordUtf8: d
          }), this.#b(h, a), this.#l.delete(u), d = null;
          try {
            r && (r.textContent = "Wallet remembered on this device.");
          } catch {
          }
        } catch {
          he(d), this.#l.delete(u), d = null, (this.#e || this.#i || this.#r !== a || this.#t !== h) && w("STALE_CONTROLLER"), this.#o.size === 1 && this.#o.has(h) && this.#t === h || (this.revokeNow("remembered-wallet-cleanup-failed"), w("DESTRUCTION_FAILED"));
          try {
            r && (r.textContent = "Wallet was not remembered.");
          } catch {
          }
        }
      return this.#b(h, a), E;
    } catch (h) {
      throw h instanceof V && h.code === "DESTRUCTION_FAILED" || (this.#e || this.#i || this.#r !== a) && w("STALE_CONTROLLER"), h;
    } finally {
      he(c), he(l), he(d), he(u), this.#l.delete(c), this.#l.delete(l), this.#l.delete(u);
    }
  }
  async unlockRemembered({ accountIndex: t = 0 } = {}) {
    (this.#e || this.#i) && w("STALE_CONTROLLER"), t !== 0 && w("INVALID_ACCOUNT"), this.canRestoreRememberedWallet() || w("REMEMBER_UNAVAILABLE");
    try {
      oe({ document: this.#n, window: this.#s });
    } catch (i) {
      Xe(i);
    }
    const n = this.#j();
    try {
      const i = await this.#y.restore({
        assertCurrent: () => this.#T(n)
      }), r = i?.handle;
      (r == null || !this.#o.has(r)) && w("WASM_FAILURE");
      let s;
      try {
        s = me(i.identity);
      } catch {
        w("WASM_FAILURE");
      }
      return (this.#e || this.#i || this.#r !== n) && (this.#E(r) || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("STALE_CONTROLLER")), this.#t = r, this.#p = s, s;
    } catch (i) {
      throw i instanceof V && i.code === "DESTRUCTION_FAILED" || ((this.#e || this.#i || this.#r !== n) && w("STALE_CONTROLLER"), this.#o.size > 0 && (this.#w(), this.#o.size > 0 && (this.revokeNow("remembered-wallet-cleanup-failed"), w("DESTRUCTION_FAILED"))), i instanceof V) ? i : typeof i?.code == "string" ? new V(i.code) : i;
    }
  }
  async unlockLegacy({
    accountIndex: t = 0,
    mnemonicControl: n = null,
    operation: i,
    passwordControl: r = null,
    profile: s,
    usernameControl: a = null
  }) {
    (this.#e || this.#i) && w("STALE_CONTROLLER"), i !== "sdn.auth.raw-challenge.v1" && w("OPERATION_NOT_ALLOWED"), t !== 0 && w("INVALID_ACCOUNT");
    const o = s === "password-fast-v1-legacy";
    !o && !(s === "bip39-mnemonic-v1-legacy") && w("INVALID_LEGACY_PROFILE"), o ? (!a || !r) && w("INVALID_CREDENTIAL_FORM") : (!n || typeof n != "object") && w("INVALID_CREDENTIAL_FORM");
    const l = o ? [a, r] : [n];
    try {
      oe({ document: this.#n, window: this.#s });
    } catch (A) {
      et(this.#n, l), Xe(A);
    }
    let d;
    try {
      d = this.#j(l);
    } catch (A) {
      throw et(this.#n, l), A;
    }
    o ? this.registerCredentialControls({ passwordControl: r, usernameControl: a }) : this.#f.add(n);
    let u, f, p;
    try {
      try {
        oe({ document: this.#n, window: this.#s });
      } catch (A) {
        Xe(A);
      }
      if (this.#T(d), o) {
        const A = a?.value, h = r?.value;
        Ze(A) || w("INVALID_USERNAME"), Ze(h) || w("INVALID_PASSWORD"), u = Ye.encode(A), f = Ye.encode(h), this.#l.add(u), this.#l.add(f);
      } else {
        const A = n?.value;
        Ze(A) || w("INVALID_MNEMONIC"), p = Ye.encode(A), this.#l.add(p);
      }
    } finally {
      this.#N();
    }
    let g;
    try {
      g = o ? await this.#m.deriveLegacyPasswordIdentity({
        accountIndex: t,
        passwordUtf8: f,
        usernameUtf8: u
      }) : await this.#m.importLegacyMnemonicIdentity({ accountIndex: t, mnemonicUtf8: p }), (!g || typeof g != "object") && w("WASM_FAILURE");
      const A = g.handle;
      A == null && w("WASM_FAILURE"), this.#o.add(A);
      let h;
      try {
        h = Xr(g.identity, { accountIndex: t, profile: s });
      } catch {
        const E = this.#E(A);
        g = null, E || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("WASM_FAILURE");
      }
      return (this.#e || this.#i || this.#r !== d) && (this.#E(A) || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("STALE_CONTROLLER")), this.#t = A, this.#p = h, h;
    } catch (A) {
      throw A instanceof V && A.code === "DESTRUCTION_FAILED" || (this.#e || this.#i || this.#r !== d) && w("STALE_CONTROLLER"), A;
    } finally {
      he(u), he(f), he(p), this.#l.delete(u), this.#l.delete(f), this.#l.delete(p);
    }
  }
  async prepare(t) {
    (this.#e || this.#i) && w("STALE_CONTROLLER"), this.#d && w("TRANSACTION_IN_PROGRESS"), this.#d = !0;
    const n = this.#r, i = typeof t == "string" ? t : t?.transactionId ?? null, r = dt(this.#s);
    r && this.#a.add(r);
    try {
      const s = typeof this.#c?.fetchTransaction == "function" ? await this.#c.fetchTransaction(t, { signal: r?.signal }) : t;
      this.#T(n);
      const a = await kt(s, {
        expectedTransactionId: i,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      });
      return this.#T(n), oe({ document: this.#n, window: this.#s }), this.#D.add(a), r && this.#P.set(a, r), a;
    } catch (s) {
      const a = this.#e || this.#i || this.#r !== n;
      throw this.#d = !1, r && this.#a.delete(r), a && w("STALE_CONTROLLER"), s instanceof V ? s : s instanceof Le ? new V(s.code) : s;
    }
  }
  async executePrepared(t) {
    (!t || typeof t != "object" || !this.#D.has(t)) && w("INVALID_TRANSACTION"), this.#D.delete(t);
    const n = this.#P.get(t) ?? null;
    this.#P.delete(t), (this.#e || this.#i || this.#t === null) && (n && this.#a.delete(n), this.#d = !1, w("STALE_CONTROLLER"));
    const i = this.#t, r = this.#p, s = this.#r;
    let a = !1;
    try {
      this.#b(i, s);
      let o = await kt(t.transaction, {
        expectedTransactionId: t.transaction.transactionId,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      });
      this.#b(i, s), oe({ document: this.#n, window: this.#s }), this.#h = Or({
        binding: o.binding,
        document: this.#n,
        identity: r,
        request: o.request,
        transaction: o.transaction
      }), await this.#h.promise, this.#b(i, s), oe({ document: this.#n, window: this.#s }), o = await kt(t.transaction, {
        expectedTransactionId: t.transaction.transactionId,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      }), this.#b(i, s), oe({ document: this.#n, window: this.#s });
      const c = await Nr({
        assertCurrent: () => this.#b(i, s),
        binding: o.binding,
        handle: i,
        identity: r,
        transaction: o.transaction,
        wasm: this.#m
      });
      this.#b(i, s), oe({ document: this.#n, window: this.#s });
      const l = o.transaction.operation === "sdn.wallet.account.v1";
      l && (this.#A = me(r));
      const d = this.#B(i, s, {
        allowPublication: !0,
        retainIdentity: l
      });
      a = !0, d.destroyed || (this.#A = null, w("DESTRUCTION_FAILED"));
      const u = this.#L, f = d.permit;
      if (l)
        return this.#I(u, f), this.#g = Object.freeze({
          connectedResult: c,
          epoch: u,
          permit: f,
          transaction: o.transaction
        }), Object.freeze({ accountReady: !0 });
      try {
        this.#I(u, f), typeof this.#c?.publishResult != "function" && w("RELAY_UNAVAILABLE");
        const p = dt(this.#s);
        p && this.#a.add(p);
        let g;
        try {
          try {
            g = await this.#c.publishResult(o.transaction, c, {
              signal: p?.signal
            });
          } catch (A) {
            throw this.#I(u, f), A;
          }
        } finally {
          p && this.#a.delete(p);
        }
        if (this.#I(u, f), oe({ document: this.#n, window: this.#s }), typeof this.#c?.navigate == "function") {
          const A = Kt(o.transaction, g);
          this.#I(u, f), this.#u = null, this.#c.navigate(A.redirectUri);
        } else
          this.#u = null;
        return g;
      } finally {
        this.#u === f && (this.#u = null);
      }
    } catch (o) {
      throw !a && (this.#r !== s || this.#t !== i) && w("STALE_CONTROLLER"), o instanceof V ? o : o instanceof Le ? new V(o.code) : o;
    } finally {
      n && this.#a.delete(n), a || this.#B(i, s);
    }
  }
  async execute(t) {
    (this.#e || this.#i || this.#t === null) && w("STALE_CONTROLLER");
    const n = this.#t, i = this.#r;
    try {
      const r = await this.prepare(t);
      return await this.executePrepared(r);
    } catch (r) {
      throw !this.#i && this.#t === n && this.#B(n, i), r;
    }
  }
  async logout() {
    if (this.#g || this.#C)
      return this.#$("disconnected");
    this.revokeNow("logout"), this.#A = null;
  }
  returnToSite() {
    return this.#$("connected");
  }
  revokeNow(t) {
    if (this.#L = this.#L >= Number.MAX_SAFE_INTEGER ? 1 : this.#L + 1, this.#u = null, this.#g = null, this.#e) {
      this.#w();
      return;
    }
    !this.#i && (this.#i = !0, this.#k()), this.#d = !1, this.#A = null, this.#t !== null && this.#t !== void 0 && this.#o.add(this.#t), this.#t = null, this.#p = null;
    const i = this.#h;
    this.#h = null;
    const r = !this.#V;
    r && (this.#V = !0);
    try {
      i?.cancel("STALE_CONTROLLER");
    } catch {
    }
    try {
      i?.destroy();
    } catch {
    }
    if (this.#N(), this.#U(), this.#M(), this.#w(), r)
      try {
        this.#c?.revokeNow?.(t);
      } catch {
      }
  }
  destroy(t = "destroy") {
    if (this.#v)
      return this.#w(), this.#v;
    let n;
    const i = new Promise((r) => {
      n = r;
    });
    return this.#v = i, (async () => {
      try {
        this.revokeNow(t), this.#e = !0, this.#W();
        try {
          await this.#c?.destroy?.(t);
        } catch {
        }
      } finally {
        n();
      }
    })(), i;
  }
}
const Qs = [
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://spacedatanetwork.org/wallet-callback.html",
    clientDisplayName: "Space Data Network",
    clientId: "sdn-landing-web-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://spacedatanetwork.org"
  },
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://spacedatastandards.org/wallet-callback.html",
    clientDisplayName: "Space Data Standards",
    clientId: "sdn-standards-web-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://spacedatastandards.org"
  },
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://digitalarsenal.github.io/flatbuffers/wallet-callback.html",
    clientDisplayName: "FlatBuffers Documentation",
    clientId: "sdn-flatbuffers-pages-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://digitalarsenal.github.io"
  },
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://digitalarsenal.github.io/flatsql/wallet-callback.html",
    clientDisplayName: "FlatSQL Documentation",
    clientId: "sdn-flatsql-pages-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://digitalarsenal.github.io"
  },
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://digitalarsenal.github.io/space-data-module-sdk/wallet-callback.html",
    clientDisplayName: "Space Data Module SDK",
    clientId: "sdn-module-sdk-pages-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://digitalarsenal.github.io"
  },
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://spaceaware.io/wallet/callback",
    clientDisplayName: "SpaceAware",
    clientId: "spaceaware-web-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://spaceaware.io"
  },
  {
    allowedOperations: [
      "sdn.auth.jcs-envelope.v2",
      "sdn.auth.raw-challenge.v1",
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [
      "sdn-login:sdn.spaceaware.io"
    ],
    callbackUri: "https://sdn.spaceaware.io/wallet/callback",
    clientDisplayName: "SDN Node Console",
    clientId: "sdn-node-console-v1",
    operationBindings: [
      {
        audience: "sdn-login:sdn.spaceaware.io",
        maxLifetimeSeconds: 300,
        operation: "sdn.auth.jcs-envelope.v2",
        registryRow: "sdn-node-console-v2",
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: "sdn-login:sdn.spaceaware.io",
        maxLifetimeSeconds: 300,
        operation: "sdn.auth.raw-challenge.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://sdn.spaceaware.io"
  },
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://digitalarsenal.github.io/OrbPro/wallet-callback.html",
    clientDisplayName: "OrbPro",
    clientId: "orbpro-pages-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://digitalarsenal.github.io"
  },
  {
    allowedOperations: [
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [],
    callbackUri: "https://digitalarsenal.github.io/asset-models/wallet-callback.html",
    clientDisplayName: "SDN Asset Models",
    clientId: "sdn-asset-models-pages-v1",
    operationBindings: [
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://digitalarsenal.github.io"
  },
  {
    allowedOperations: [
      "sdn.asset-review.authority-activation.v1",
      "sdn.asset-review.decision.v1",
      "sdn.wallet.account.v1",
      "sdn.wallet.connect.v1"
    ],
    audiences: [
      "asset-review-authority:assets.ipfs.01",
      "asset-review:assets.ipfs.01"
    ],
    callbackUri: "https://review.spacedatanetwork.org/wallet/callback",
    clientDisplayName: "SDN Asset Review",
    clientId: "sdn-asset-review-v1",
    operationBindings: [
      {
        audience: "asset-review-authority:assets.ipfs.01",
        maxLifetimeSeconds: 300,
        operation: "sdn.asset-review.authority-activation.v1",
        registryRow: "asset-review-authority-activation-v1",
        serviceActivationState: "unactivated",
        serviceInstance: "assets.ipfs.01/asset-review-attestation"
      },
      {
        audience: "asset-review:assets.ipfs.01",
        maxLifetimeSeconds: 300,
        operation: "sdn.asset-review.decision.v1",
        registryRow: "asset-review-decision-v1",
        serviceActivationState: "activated",
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.account.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      },
      {
        audience: null,
        maxLifetimeSeconds: 300,
        operation: "sdn.wallet.connect.v1",
        registryRow: null,
        serviceActivationState: null,
        serviceInstance: null
      }
    ],
    requestOrigin: "https://review.spacedatanetwork.org"
  }
], Ys = "e1ce6fe903c9700484a8a87d96581c8cad97063dabf63030b4518a31a3bdaa93", Xs = 1, Ei = {
  clients: Qs,
  registryReleaseSha256: Ys,
  schemaVersion: Xs
}, Zs = new TextEncoder(), eo = /^[0-9a-f]{64}$/u, to = /^[a-z0-9]+(?:-[a-z0-9]+)*$/u, no = Object.freeze({
  "sdn.wallet.account.v1": Object.freeze({
    audience: null,
    registryRow: null,
    serviceActivationState: null,
    serviceInstance: null
  }),
  "sdn.wallet.connect.v1": Object.freeze({
    audience: null,
    registryRow: null,
    serviceActivationState: null,
    serviceInstance: null
  }),
  "sdn.auth.raw-challenge.v1": Object.freeze({
    audience: "sdn-login:sdn.spaceaware.io",
    registryRow: null,
    serviceActivationState: null,
    serviceInstance: null
  }),
  "sdn.auth.jcs-envelope.v2": Object.freeze({
    audience: "sdn-login:sdn.spaceaware.io",
    registryRow: "sdn-node-console-v2",
    serviceActivationState: null,
    serviceInstance: null
  }),
  "sdn.asset-review.authority-activation.v1": Object.freeze({
    audience: "asset-review-authority:assets.ipfs.01",
    registryRow: "asset-review-authority-activation-v1",
    serviceActivationState: "unactivated",
    serviceInstance: "assets.ipfs.01/asset-review-attestation"
  }),
  "sdn.asset-review.decision.v1": Object.freeze({
    audience: "asset-review:assets.ipfs.01",
    registryRow: "asset-review-decision-v1",
    serviceActivationState: "activated",
    serviceInstance: null
  })
}), ge = Object.freeze([
  "sdn.wallet.account.v1",
  "sdn.wallet.connect.v1"
]), io = Object.freeze([
  "sdn.auth.jcs-envelope.v2",
  "sdn.auth.raw-challenge.v1",
  ...ge
]), ro = Object.freeze([
  "sdn.asset-review.authority-activation.v1",
  "sdn.asset-review.decision.v1",
  ...ge
]), Pn = Object.freeze([
  Object.freeze(["sdn-landing-web-v1", "Space Data Network", "https://spacedatanetwork.org", "https://spacedatanetwork.org/wallet-callback.html", ge]),
  Object.freeze(["sdn-standards-web-v1", "Space Data Standards", "https://spacedatastandards.org", "https://spacedatastandards.org/wallet-callback.html", ge]),
  Object.freeze(["sdn-flatbuffers-pages-v1", "FlatBuffers Documentation", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/flatbuffers/wallet-callback.html", ge]),
  Object.freeze(["sdn-flatsql-pages-v1", "FlatSQL Documentation", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/flatsql/wallet-callback.html", ge]),
  Object.freeze(["sdn-module-sdk-pages-v1", "Space Data Module SDK", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/space-data-module-sdk/wallet-callback.html", ge]),
  Object.freeze(["spaceaware-web-v1", "SpaceAware", "https://spaceaware.io", "https://spaceaware.io/wallet/callback", ge]),
  Object.freeze(["sdn-node-console-v1", "SDN Node Console", "https://sdn.spaceaware.io", "https://sdn.spaceaware.io/wallet/callback", io]),
  Object.freeze(["orbpro-pages-v1", "OrbPro", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/OrbPro/wallet-callback.html", ge]),
  Object.freeze(["sdn-asset-models-pages-v1", "SDN Asset Models", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/asset-models/wallet-callback.html", ge]),
  Object.freeze(["sdn-asset-review-v1", "SDN Asset Review", "https://review.spacedatanetwork.org", "https://review.spacedatanetwork.org/wallet/callback", ro])
]), so = Object.freeze([
  Object.freeze({
    audience: "sdn-login:sdn.spaceaware.io",
    clientId: "sdn-node-console-v1",
    maxLifetimeSeconds: 300,
    operation: "sdn.auth.jcs-envelope.v2",
    requestOrigin: "https://sdn.spaceaware.io",
    registryRow: "sdn-node-console-v2",
    serviceActivationState: null,
    serviceInstance: null
  }),
  Object.freeze({
    audience: "asset-review-authority:assets.ipfs.01",
    clientId: "sdn-asset-review-v1",
    maxLifetimeSeconds: 300,
    operation: "sdn.asset-review.authority-activation.v1",
    requestOrigin: "https://review.spacedatanetwork.org",
    registryRow: "asset-review-authority-activation-v1",
    serviceActivationState: "unactivated",
    serviceInstance: "assets.ipfs.01/asset-review-attestation"
  }),
  Object.freeze({
    audience: "asset-review:assets.ipfs.01",
    clientId: "sdn-asset-review-v1",
    maxLifetimeSeconds: 300,
    operation: "sdn.asset-review.decision.v1",
    requestOrigin: "https://review.spacedatanetwork.org",
    registryRow: "asset-review-decision-v1",
    serviceActivationState: "activated",
    serviceInstance: null
  })
]);
function j(e) {
  throw new TypeError(e);
}
function ln(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function yt(e, t, n) {
  ln(e) || j(`${n} must be an object`);
  const i = Reflect.ownKeys(e);
  i.some((a) => typeof a != "string") && j(`${n} has missing or unknown fields`);
  const r = i.sort(), s = [...t].sort();
  (r.length !== s.length || r.some((a, o) => a !== s[o])) && j(`${n} has missing or unknown fields`);
  for (const a of r) {
    const o = Object.getOwnPropertyDescriptor(e, a);
    (!o || !o.enumerable || !("value" in o) || o.value === void 0) && j(`${n} has an invalid field`);
  }
  return e;
}
function ye(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" ? JSON.stringify(e) : typeof e == "number" ? (Number.isFinite(e) || j("registry contains a non-finite number"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(ye).join(",")}]` : (ln(e) || j("registry contains an unsupported value"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${ye(e[t])}`).join(",")}}`);
}
async function oo(e) {
  const t = globalThis.crypto?.subtle;
  t || j("Web Crypto SHA-256 is unavailable");
  const n = new Uint8Array(await t.digest("SHA-256", Zs.encode(e)));
  return Array.from(n, (i) => i.toString(16).padStart(2, "0")).join("");
}
function Lt(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(Lt));
  if (!ln(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = Lt(e[n]);
  return Object.freeze(t);
}
function ao(e, t) {
  (typeof e != "string" || !e.startsWith("https://") || e.includes("*")) && j(`${t} must be exact HTTPS`);
  let n;
  try {
    n = new URL(e);
  } catch {
    j(`${t} must be a valid URL`);
  }
  (n.protocol !== "https:" || n.username || n.password || n.port || n.origin !== e || n.pathname !== "/" || n.search || n.hash) && j(`${t} must be an exact HTTPS origin`);
}
function co(e, t) {
  (typeof e != "string" || !e.startsWith("https://") || e.includes("*")) && j("callbackUri must be exact HTTPS");
  let n;
  try {
    n = new URL(e);
  } catch {
    j("callbackUri must be a valid URL");
  }
  (n.protocol !== "https:" || n.username || n.password || n.port || n.origin !== t || n.search || n.hash || n.href !== e) && j("callbackUri must be exact and same-origin");
}
function lo(e) {
  yt(e, ["clients", "registryReleaseSha256", "schemaVersion"], "registry"), e.schemaVersion !== 1 && j("registry schemaVersion must be 1"), (typeof e.registryReleaseSha256 != "string" || !eo.test(e.registryReleaseSha256)) && j("registryReleaseSha256 must be lowercase SHA-256"), (!Array.isArray(e.clients) || e.clients.length !== Pn.length) && j("registry must contain exactly the reviewed clients");
  const t = /* @__PURE__ */ new Set(), n = /* @__PURE__ */ new Set();
  e.clients.forEach((i, r) => {
    yt(i, [
      "allowedOperations",
      "audiences",
      "callbackUri",
      "clientDisplayName",
      "clientId",
      "operationBindings",
      "requestOrigin"
    ], `client ${r}`);
    const [s, a, o, c, l] = Pn[r];
    (i.clientId !== s || i.clientDisplayName !== a || i.requestOrigin !== o || i.callbackUri !== c) && j(`client ${r} differs from the reviewed row`), (!to.test(i.clientId) || t.has(i.clientId)) && j("clientId must be unique and canonical"), t.add(i.clientId), (typeof i.clientDisplayName != "string" || i.clientDisplayName.length < 1 || i.clientDisplayName.length > 80) && j("clientDisplayName is invalid"), ao(i.requestOrigin, "requestOrigin"), co(i.callbackUri, i.requestOrigin), (!Array.isArray(i.allowedOperations) || ye(i.allowedOperations) !== ye(l)) && j(`${i.clientId} allowedOperations differ from the reviewed allowlist`), (!Array.isArray(i.operationBindings) || i.operationBindings.length !== l.length) && j(`${i.clientId} operationBindings differ from the reviewed allowlist`), i.operationBindings.forEach((u, f) => {
      yt(u, [
        "audience",
        "maxLifetimeSeconds",
        "operation",
        "registryRow",
        "serviceActivationState",
        "serviceInstance"
      ], `${i.clientId} operation ${f}`);
      const p = l[f];
      u.operation !== p && j(`${i.clientId} operation allowlist order or value changed`);
      const g = no[u.operation];
      g || j("unknown registry operation"), (u.maxLifetimeSeconds !== 300 || u.audience !== g.audience || u.registryRow !== g.registryRow || u.serviceActivationState !== g.serviceActivationState || u.serviceInstance !== g.serviceInstance) && j(`${i.clientId} operation policy differs from the reviewed binding`);
      const A = `${i.clientId}\0${i.requestOrigin}\0${u.operation}`;
      n.has(A) && j("duplicate registry binding"), n.add(A);
    });
    const d = [...new Set(i.operationBindings.map(({ audience: u }) => u).filter((u) => u !== null))].sort();
    (!Array.isArray(i.audiences) || ye(i.audiences) !== ye(d)) && j(`${i.clientId} audiences differ from operationBindings`), ye(i.allowedOperations) !== ye(i.operationBindings.map(({ operation: u }) => u)) && j(`${i.clientId} allowedOperations differ from operationBindings`);
  });
  for (const i of so) {
    const r = e.clients.find((o) => o.clientId === i.clientId && o.requestOrigin === i.requestOrigin), s = r?.operationBindings.find((o) => o.operation === i.operation), a = s && {
      audience: s.audience,
      clientId: r.clientId,
      maxLifetimeSeconds: s.maxLifetimeSeconds,
      operation: s.operation,
      requestOrigin: r.requestOrigin,
      registryRow: s.registryRow,
      serviceActivationState: s.serviceActivationState,
      serviceInstance: s.serviceInstance
    };
    (!a || ye(a) !== ye(i)) && j(`registry projection drifted from compiled row ${i.registryRow}`);
  }
  return Lt(e);
}
const { registryReleaseSha256: uo, ...fo } = Ei, po = await oo(ye(fo));
po !== uo && j("registry release SHA-256 mismatch");
const kn = lo(Ei);
function ho(e) {
  const t = yt(e, ["clientId", "operation", "requestOrigin"], "registry lookup");
  (typeof t.clientId != "string" || typeof t.requestOrigin != "string" || typeof t.operation != "string") && j("registry lookup fields must be strings");
  const n = kn.clients.find((r) => r.clientId === t.clientId && r.requestOrigin === t.requestOrigin), i = n?.operationBindings.find((r) => r.operation === t.operation);
  return (!n || !i) && j("no exact registry binding exists"), Lt({
    audience: i.audience,
    callbackUri: n.callbackUri,
    clientDisplayName: n.clientDisplayName,
    clientId: n.clientId,
    maxLifetimeSeconds: i.maxLifetimeSeconds,
    operation: i.operation,
    requestOrigin: n.requestOrigin,
    registryReleaseSha256: kn.registryReleaseSha256,
    registryRow: i.registryRow,
    serviceActivationState: i.serviceActivationState,
    serviceInstance: i.serviceInstance
  });
}
const yo = /^\/transaction\/([0-9a-f]{64})$/u, jt = new TextEncoder(), mo = Uint8Array.prototype.fill, go = Object.freeze({ resolveRegistryBinding: ho });
function Un(e) {
  if (e instanceof Uint8Array)
    try {
      mo.call(e, 0);
    } catch {
    }
}
function Bt(e) {
  if (typeof e != "string") return !1;
  for (let t = 0; t < e.length; t += 1) {
    const n = e.charCodeAt(t);
    if (n >= 55296 && n <= 56319) {
      const i = e.charCodeAt(++t);
      if (!(i >= 56320 && i <= 57343)) return !1;
    } else if (n >= 56320 && n <= 57343) return !1;
  }
  return !0;
}
function jn() {
  return new V("INVALID_TRANSACTION");
}
function st(e, t, n, i) {
  const r = e.createElement("label"), s = e.createElement("span");
  s.textContent = n, r.append(s, i), t.append(r);
}
function Fe(e, t) {
  for (const n of t) {
    try {
      n.value = "";
    } catch {
    }
    try {
      n.defaultValue = "";
    } catch {
    }
    try {
      n.disabled = !0;
    } catch {
    }
    try {
      n.inert = !0;
    } catch {
    }
    try {
      n.removeAttribute?.("name");
    } catch {
    }
    try {
      n.removeAttribute?.("autocomplete");
    } catch {
    }
    try {
      n.setSelectionRange?.(0, 0);
    } catch {
    }
    try {
      n.setCustomValidity?.("");
    } catch {
    }
  }
  try {
    e.remove?.();
  } catch {
  }
}
function wi({
  clipboard: e,
  controller: t,
  document: n,
  onChanged: i = () => {
  }
}) {
  if (!t || typeof t.listQuarantinedWalletRecords != "function") return null;
  const r = n.createElement("section");
  r.className = "wallet-quarantine-manager";
  const s = t.generation;
  let a = !1, o = 0, c = [], l = null;
  const d = (h = o) => !a && h === o && t.isUiGenerationCurrent?.(s) === !0, u = (h, E = o) => {
    d(E) && l?.status && (l.status.textContent = h);
  }, f = () => {
    o += 1;
    for (const [h, E, m] of c)
      try {
        h.removeEventListener?.(E, m);
      } catch {
      }
    c = [];
    for (const h of l?.rows ?? []) {
      try {
        h.confirmation.value = "";
      } catch {
      }
      try {
        h.confirmation.defaultValue = "";
      } catch {
      }
      try {
        h.confirmation.disabled = !0;
      } catch {
      }
    }
    l?.status && (l.status.textContent = ""), l = null;
  }, p = (h, E, m) => {
    h.addEventListener?.(E, m), c.push([h, E, m]);
  }, g = () => {
    if (f(), a || t.isUiGenerationCurrent?.(s) !== !0)
      return r.replaceChildren(), r.hidden = !0, !1;
    let h;
    try {
      h = t.listQuarantinedWalletRecords();
    } catch {
      h = [];
    }
    if (!Array.isArray(h) || h.length === 0)
      return r.replaceChildren(), r.hidden = !0, !1;
    r.hidden = !1, l = is(r, h, { document: n });
    const E = o;
    for (const m of l.rows) {
      const { entry: O } = m, I = async (H) => {
        if (H?.isTrusted !== !0 || !d(E)) return;
        if (O.exportable !== !0) {
          u("Quarantined record is too large to export.", E);
          return;
        }
        let N;
        try {
          if (N = t.exportQuarantinedWalletRecord(O.key), typeof e?.writeText != "function") throw new Error("clipboard unavailable");
          await e.writeText(N), u("Quarantined record exported to the clipboard.", E);
        } catch {
          u("Quarantined record export failed.", E);
        } finally {
          N = null;
        }
      }, B = (H) => {
        if (!(H?.isTrusted !== !0 || !d(E))) {
          try {
            m.confirmation.value = "";
          } catch {
          }
          try {
            m.confirmation.defaultValue = "";
          } catch {
          }
          m.confirmationGroup.hidden = !1, u(`Type ${O.key} to confirm deletion.`, E);
          try {
            m.confirmation.focus?.();
          } catch {
          }
        }
      }, P = (H) => {
        if (!(H?.isTrusted !== !0 || !d(E))) {
          try {
            m.confirmation.value = "";
          } catch {
          }
          try {
            m.confirmation.defaultValue = "";
          } catch {
          }
          m.confirmationGroup.hidden = !0, u("", E);
        }
      }, L = (H) => {
        if (H?.isTrusted !== !0 || !d(E)) return;
        const N = m.confirmation.value;
        if (N !== O.key) {
          u("Type the exact storage key to confirm deletion.", E);
          return;
        }
        try {
          t.deleteQuarantinedWalletRecord(O.key, N);
        } catch {
          u("Quarantined record deletion failed.", E);
          return;
        }
        try {
          m.confirmation.value = "";
        } catch {
        }
        try {
          m.confirmation.defaultValue = "";
        } catch {
        }
        g(), a || i();
      };
      p(m.exportButton, "click", I), p(m.deleteButton, "click", B), p(m.cancel, "click", P), p(m.confirm, "click", L);
    }
    return !0;
  }, A = g();
  return Object.freeze({
    container: r,
    destroy() {
      a || (a = !0, f(), r.replaceChildren(), r.remove?.());
    },
    hasEntries: A,
    refresh: g
  });
}
function Ao({
  clipboard: e = globalThis.navigator?.clipboard,
  controller: t = null,
  document: n,
  mount: i = null,
  offerRememberedUnlock: r = !0,
  title: s = "Sign in"
}) {
  const a = i ?? n?.body;
  if (!n?.createElement || !a?.append) throw new V("DOM_UNAVAILABLE");
  const o = n.createElement("section");
  o.className = "wallet-login";
  const c = n.createElement("h1");
  c.textContent = s;
  const l = n.createElement("p");
  l.textContent = "Account 0";
  const d = n.createElement("form");
  d.noValidate = !0;
  const u = n.createElement("input");
  u.type = "text", u.name = "username", u.autocomplete = "username", u.required = !0;
  const f = n.createElement("input");
  f.type = "password", f.name = "password", f.autocomplete = "current-password", f.required = !0, st(n, d, "Username", u), st(n, d, "Password", f);
  let p = null;
  if (t && typeof t == "object") {
    p = n.createElement("input"), p.type = "checkbox", p.dataset.walletRemember = "prf-only", p.checked = !1, p.defaultChecked = !1;
    let R = !1;
    try {
      R = t.supportsRememberedWallet?.() === !0;
    } catch {
      R = !1;
    }
    p.disabled = !R, st(n, d, "Remember on this device", p);
  }
  const g = n.createElement("p");
  g.dataset.walletRememberStatus = "true";
  let A = () => {
  };
  const h = wi({
    clipboard: e,
    controller: t,
    document: n,
    onChanged: () => A()
  }), E = n.createElement("div");
  E.className = "wallet-login-actions";
  let m = null, O = !1;
  try {
    O = t?.canRestoreRememberedWallet?.() === !0;
  } catch {
    O = !1;
  }
  O && r && (m = n.createElement("button"), m.type = "button", m.dataset.walletAction = "unlock-remembered", m.textContent = "Unlock remembered wallet");
  const I = n.createElement("button");
  I.type = "submit", I.dataset.walletAction = "login", I.textContent = "Login";
  const B = n.createElement("button");
  B.type = "button", B.dataset.walletAction = "cancel-login", B.textContent = "Cancel", m && E.append(m), E.append(I, B), d.append(E), o.append(c, l), h?.hasEntries && o.append(h.container), o.append(d, g), a.append(o), t?.registerCredentialControls?.({ passwordControl: f, usernameControl: u });
  let P = !1, L, H;
  const N = new Promise((R, z) => {
    L = R, H = z;
  }), F = () => {
    d.removeEventListener?.("submit", ie), B.removeEventListener?.("click", Q), m?.removeEventListener?.("click", k);
  }, b = () => {
    if (h?.destroy?.(), p) {
      try {
        p.checked = !1;
      } catch {
      }
      try {
        p.defaultChecked = !1;
      } catch {
      }
      try {
        p.disabled = !0;
      } catch {
      }
    }
    g.textContent = "";
  }, W = () => {
    F(), Fe(d, [u, f]), b(), o.remove?.();
  }, T = (R) => {
    if (!P) {
      P = !0;
      try {
        t?.revokeNow?.(R);
      } catch {
      }
      W(), H(new V(R));
    }
  }, ie = (R) => {
    R?.preventDefault?.(), !(P || R?.isTrusted !== !0) && (P = !0, F(), h?.destroy?.(), L({ passwordControl: f, rememberControl: p, rememberStatus: g, usernameControl: u }));
  }, Q = (R) => {
    R?.isTrusted === !0 && T("USER_CANCELLED");
  }, k = (R) => {
    P || R?.isTrusted !== !0 || (P = !0, F(), h?.destroy?.(), Fe(d, [u, f]), L({ remembered: !0, rememberStatus: g }));
  };
  A = () => {
    if (P || !r) return;
    let R = !1;
    try {
      R = t?.canRestoreRememberedWallet?.() === !0;
    } catch {
      R = !1;
    }
    R && !m ? (m = n.createElement("button"), m.type = "button", m.dataset.walletAction = "unlock-remembered", m.textContent = "Unlock remembered wallet", E.replaceChildren(m, I, B), m.addEventListener?.("click", k)) : !R && m && (m.removeEventListener?.("click", k), m.remove?.(), m = null);
  }, d.addEventListener("submit", ie), B.addEventListener("click", Q), m?.addEventListener?.("click", k);
  try {
    u.focus?.();
  } catch {
  }
  return Object.freeze({
    cancel() {
      P ? W() : T("STALE_CONTROLLER");
    },
    controls: Object.freeze({ passwordControl: f, rememberControl: p, rememberStatus: g, usernameControl: u }),
    form: d,
    promise: N,
    remove: W
  });
}
function bi({
  document: e,
  mount: t = null,
  submitLabel: n = "Compare legacy account",
  title: i = "Enter the legacy BIP-39 mnemonic"
}) {
  const r = t ?? e?.body;
  if (!e?.createElement || !r?.append) throw new V("DOM_UNAVAILABLE");
  const s = e.createElement("section");
  s.className = "wallet-login wallet-legacy-credentials";
  const a = e.createElement("h1");
  a.textContent = i;
  const o = e.createElement("form");
  o.noValidate = !0;
  const c = e.createElement("textarea");
  c.name = "mnemonic", c.autocomplete = "off", c.required = !0, st(e, o, "Mnemonic", c);
  const l = e.createElement("button");
  l.type = "submit", l.dataset.walletAction = "confirm-legacy-mnemonic", l.textContent = n;
  const d = e.createElement("button");
  d.type = "button", d.dataset.walletAction = "cancel-legacy-migration", d.textContent = "Cancel", o.append(l, d), s.append(a, o), r.append(s);
  let u = !1, f, p;
  const g = new Promise((I, B) => {
    f = I, p = B;
  }), A = () => {
    o.removeEventListener?.("submit", m), d.removeEventListener?.("click", O);
  }, h = () => {
    A(), Fe(o, [c]), s.remove?.();
  }, E = () => {
    u || (u = !0, h(), p(new V("USER_CANCELLED")));
  }, m = (I) => {
    I?.preventDefault?.(), !(u || I?.isTrusted !== !0) && (u = !0, A(), f({ form: o, mnemonicControl: c, section: s }));
  }, O = (I) => {
    I?.isTrusted === !0 && E();
  };
  o.addEventListener("submit", m), d.addEventListener("click", O);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({
    cancel() {
      u ? h() : E();
    },
    promise: g,
    remove: h
  });
}
function Eo({ document: e, mount: t = null, title: n = "Choose legacy wallet profile" }) {
  const i = t ?? e?.body;
  if (!e?.createElement || !i?.append) throw new V("DOM_UNAVAILABLE");
  const r = e.createElement("section");
  r.className = "wallet-login wallet-legacy-profile";
  const s = e.createElement("h1");
  s.textContent = n;
  const a = e.createElement("p");
  a.textContent = "Raw-v1 compatibility login requires the exact legacy profile. It cannot approve assets.";
  const o = e.createElement("form");
  o.noValidate = !0;
  const c = e.createElement("select");
  c.dataset.walletLegacyProfile = "required", c.required = !0, c.value = "";
  for (const [I, B] of [
    ["", "Select a legacy profile"],
    ["password-fast-v1-legacy", "Legacy fast-password profile"],
    ["bip39-mnemonic-v1-legacy", "Legacy BIP-39 mnemonic import"]
  ]) {
    const P = e.createElement("option");
    P.value = I, P.textContent = B, I === "" && (P.disabled = !0, P.selected = !0), c.append(P);
  }
  st(e, o, "Legacy profile", c);
  const l = e.createElement("button");
  l.type = "submit", l.dataset.walletAction = "continue-legacy-login", l.textContent = "Continue";
  const d = e.createElement("button");
  d.type = "button", d.dataset.walletAction = "cancel-legacy-login", d.textContent = "Cancel", o.append(l, d), r.append(s, a, o), i.append(r);
  let u = !1, f, p;
  const g = new Promise((I, B) => {
    f = I, p = B;
  }), A = () => {
    o.removeEventListener?.("submit", m), d.removeEventListener?.("click", O);
  }, h = () => {
    try {
      c.value = "";
    } catch {
    }
    Fe(o, [c]), r.remove?.();
  }, E = () => {
    u || (u = !0, A(), h(), p(new V("USER_CANCELLED")));
  }, m = (I) => {
    if (I?.preventDefault?.(), u || I?.isTrusted !== !0) return;
    const B = c.value;
    B !== "password-fast-v1-legacy" && B !== "bip39-mnemonic-v1-legacy" || (u = !0, A(), h(), f(B));
  }, O = (I) => {
    I?.isTrusted === !0 && E();
  };
  o.addEventListener("submit", m), d.addEventListener("click", O);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({ cancel: E, promise: g });
}
function ft(e, t, n, i) {
  const r = e.createElement("div"), s = e.createElement("strong");
  s.textContent = `${n}: `;
  const a = e.createElement("span");
  a.textContent = String(i ?? ""), r.append(s, a), t.append(r);
}
function Ii(e, t, n, i = null) {
  const r = i ?? e?.body, s = e.createElement("section");
  s.className = "wallet-complete";
  const a = e.createElement("h1");
  a.textContent = "Complete";
  const o = e.createElement("p");
  o.textContent = n;
  const c = e.createElement("button");
  c.type = "button", c.dataset.walletAction = "close", c.textContent = "Close", c.addEventListener("click", (l) => {
    if (l?.isTrusted === !0)
      try {
        t?.close?.();
      } catch {
      }
  }), s.append(a, o, c), r?.replaceChildren?.(s);
}
function vi(e, t = "The wallet request could not be completed. Close this window.", n = null) {
  const i = n ?? e?.body;
  if (!e?.createElement || !i?.replaceChildren) return;
  const r = e.createElement("section");
  r.className = "wallet-terminal-error";
  const s = e.createElement("h1");
  s.textContent = "Wallet request stopped";
  const a = e.createElement("p");
  a.textContent = t, r.append(s, a), i.replaceChildren(r);
}
function wo({
  clipboard: e,
  controller: t,
  document: n,
  identity: i,
  isAppCurrent: r = () => !0,
  makeCredentialPrompt: s,
  mount: a,
  onClear: o,
  wasm: c
}) {
  const l = n.createElement("section");
  l.className = "wallet-account";
  const d = n.createElement("div"), u = ts(d, i, { document: n }), f = n.createElement("section");
  f.className = "wallet-approval-card";
  const p = n.createElement("h2");
  p.textContent = "Asset review approval";
  const g = n.createElement("p"), A = n.createElement("div"), h = n.createElement("button");
  h.type = "button", h.dataset.walletAction = "copy-approval", h.textContent = "Copy approval configuration", h.disabled = !u.approvalAvailable, u.approvalAvailable || (g.textContent = rn), f.append(p, g, h, A);
  const E = n.createElement("button");
  E.type = "button", E.dataset.walletAction = "logout", E.textContent = "Logout";
  const m = n.createElement("button");
  m.type = "button", m.dataset.walletAction = "return-to-site", m.textContent = "Return to site";
  const O = n.createElement("section");
  O.className = "wallet-legacy-migration";
  const I = as(O, { document: n });
  let B = !1;
  try {
    B = t.canForgetRememberedWallet?.() === !0;
  } catch {
    B = !1;
  }
  const P = B ? n.createElement("section") : null, L = P ? ns(P, { document: n }) : null;
  L && (L.launch.dataset.walletAction = "forget-stored-wallet");
  const H = wi({
    clipboard: e,
    controller: t,
    document: n
  }), N = n.createElement("p");
  N.className = "wallet-account-exit-status", l.append(d, f), P && l.append(P), H?.hasEntries && l.append(H.container), l.append(m, E, O), a.replaceChildren(l);
  const F = new ss({
    credentialRound: async (S) => {
      const U = s({
        controller: null,
        document: n,
        mount: a,
        title: `Confirm account (${S} of 2)`
      });
      U?.promise && b.add(U);
      try {
        return U?.promise ? await U.promise : await U;
      } finally {
        U?.promise && b.delete(U);
      }
    },
    expectedIdentity: i,
    wasm: c
  }), b = /* @__PURE__ */ new Set(), W = /* @__PURE__ */ new Set(), T = /* @__PURE__ */ new Set(), ie = /* @__PURE__ */ new Set();
  let Q = !1, k = !1, R = !1, z = null, Y = !1, G = !1, K = !1, te = 0, re = !1, ne = 0;
  const se = (S) => !k && ne === S, J = (S) => {
    if (!se(S)) throw new Error("account surface closed");
  }, Oe = (S) => (S instanceof Uint8Array && ie.add(S), S), Ue = (S) => {
    Un(S), ie.delete(S);
  }, ze = () => {
    const S = [...ie];
    ie.clear();
    for (const U of S) Un(U);
  }, be = () => {
    const S = Q || K || Y;
    m.disabled = R, E.disabled = R, k || (h.disabled = S || !u.approvalAvailable, I.launch.disabled = S, L && (L.launch.disabled = S || G, L.confirm.disabled = S, L.cancel.disabled = S));
  }, je = (S) => {
    if (!W.has(S)) return !0;
    if (T.has(S)) return !1;
    T.add(S);
    try {
      return (c?.sdn ?? c).destroySdnIdentity(S), W.delete(S), !0;
    } catch {
      return !1;
    } finally {
      T.delete(S);
    }
  }, qe = () => {
    for (const S of [...W]) je(S);
    return W.size === 0;
  }, He = async (S) => {
    if (S?.isTrusted !== !0 || Q || K || Y) return;
    const U = ne;
    Q = !0, be(), g.textContent = "Confirm the same account twice to enable Copy.";
    try {
      const fe = await F.confirm();
      J(U);
      const X = await rs(fe, {
        assertCurrent: () => J(U),
        clipboard: e,
        container: A,
        document: n
      });
      J(U), g.textContent = X ? "Approval configuration copied." : "Clipboard unavailable. Copy the exact configuration shown below.";
    } catch {
      se(U) && (g.textContent = "The two entries did not produce the same account.");
    } finally {
      Q = !1, se(U) && be();
    }
  }, Ie = async (S) => {
    if (S?.isTrusted !== !0 || K || Q || Y) return;
    const U = I.select.value || "password-fast-v1-legacy";
    if (U !== "password-fast-v1-legacy" && U !== "bip39-mnemonic-v1-legacy") {
      I.result.textContent = "Legacy profile unavailable.";
      return;
    }
    const fe = ne;
    K = !0, te += 1, be(), I.result.textContent = "Enter the selected legacy credentials to compare accounts.";
    let X = null, _e, Ct, _t, ce = null;
    try {
      let xt;
      if (U === "password-fast-v1-legacy") {
        X = s({
          controller: null,
          document: n,
          mount: a,
          title: "Enter the legacy fast-password account"
        }), X?.promise && b.add(X);
        const Z = X?.promise ? await X.promise : await X;
        J(fe);
        const Je = Z?.usernameControl?.value, fn = Z?.passwordControl?.value;
        if (!Bt(Je) || !Bt(fn)) throw new Error("invalid legacy credentials");
        _e = Oe(jt.encode(Je)), Ct = Oe(jt.encode(fn));
        const pn = Z?.usernameControl?.form ?? Z?.passwordControl?.form ?? X?.form, Oi = pn?.parentNode;
        Fe(pn, [Z?.usernameControl, Z?.passwordControl]), Oi?.remove?.(), xt = { passwordUtf8: Ct, usernameUtf8: _e };
      } else {
        X = bi({ document: n, mount: a }), b.add(X);
        const Z = await X.promise;
        J(fe);
        const Je = Z?.mnemonicControl?.value;
        if (!Bt(Je)) throw new Error("invalid legacy credentials");
        _t = Oe(jt.encode(Je)), Fe(Z.form, [Z.mnemonicControl]), Z.section?.remove?.(), xt = { mnemonicUtf8: _t };
      }
      if (X?.promise && b.delete(X), ce = await os({
        accountIndex: 0,
        credentials: xt,
        operation: "sdn.auth.raw-challenge.v1",
        profile: U,
        wasm: c,
        assertCurrent: () => J(fe),
        ownHandle: (Z) => W.add(Z)
      }), J(fe), !ce?.handle) throw new Error("legacy derivation failed");
      const Si = U === "password-fast-v1-legacy" ? "sdn-fast-password-auth-v1-legacy" : "sdn-bip39-auth-v1-legacy", ut = Array.isArray(ce.identity?.keys) ? ce.identity.keys.find((Z) => Z?.purpose === "sdn-authentication") : null;
      if (ce.identity?.identityScheme !== Si || ce.identity?.seedProfile !== U || typeof ce.identity?.accountXpub != "string" || !ut || typeof ut.publicKeyHex != "string" || !/^[0-9a-f]{64}$/u.test(ut.publicKeyHex))
        throw new Error("legacy identity invalid");
      const Ri = ce.identity;
      if (!je(ce.handle)) throw new Error("legacy destruction failed");
      ce = null, J(fe);
      const Li = i.keys.find((Z) => Z.purpose === "sdn-authentication");
      I.result.replaceChildren(), ft(n, I.result, "Current account xpub", i.accountXpub), ft(n, I.result, "Legacy account xpub", Ri.accountXpub), ft(n, I.result, "Current authentication key", Li?.publicKeyHex), ft(n, I.result, "Legacy authentication key", ut.publicKeyHex);
    } catch {
      se(fe) && (I.result.textContent = "Legacy account comparison could not be completed.");
    } finally {
      ce?.handle && je(ce.handle), X?.promise && b.delete(X), Ue(_e), Ue(Ct), Ue(_t), qe(), te -= 1, K = !1, se(fe) && be();
    }
  }, Ne = () => {
    if (L) {
      try {
        L.confirmation.value = "";
      } catch {
      }
      try {
        L.confirmation.defaultValue = "";
      } catch {
      }
    }
  }, Ge = (S) => {
    if (!(!L || S?.isTrusted !== !0 || Q || K || Y || G || k)) {
      Ne(), L.confirmationGroup.hidden = !1, L.status.textContent = `Type ${L.confirmationKey} to confirm.`;
      try {
        L.confirmation.focus?.();
      } catch {
      }
    }
  }, ct = (S) => {
    !L || S?.isTrusted !== !0 || Y || k || (Ne(), L.confirmationGroup.hidden = !0, L.status.textContent = "Forget cancelled.");
  }, Te = (S) => {
    if (!L || S?.isTrusted !== !0 || Q || K || Y || G || k) return;
    const U = L.confirmation.value;
    if (U !== L.confirmationKey) {
      L.status.textContent = "Type the exact storage key to confirm.";
      return;
    }
    Y = !0, be(), Ne();
    try {
      t.forgetRememberedWallet(U), G = !0, L.confirmationGroup.hidden = !0, L.status.textContent = "Stored wallet forgotten. This account remains signed in.";
    } catch {
      L.status.textContent = "Stored wallet could not be forgotten.";
    } finally {
      Y = !1, k || be();
    }
  }, Ke = (S) => {
    if (k)
      ze();
    else {
      k = !0, ne += 1, h.disabled = !0, I.launch.disabled = !0, L && (L.launch.disabled = !0, L.confirm.disabled = !0, L.cancel.disabled = !0, Ne(), L.launch.removeEventListener?.("click", Ge), L.confirm.removeEventListener?.("click", Te), L.cancel.removeEventListener?.("click", ct)), H?.destroy?.(), ze(), h.removeEventListener?.("click", He), I.launch.removeEventListener?.("click", Ie);
      for (const U of b) U.cancel?.();
      b.clear();
    }
    S && !re && (N.textContent = "Secure cleanup is still pending. Retry Return or Logout.", l.replaceChildren(N, m, E), m.disabled = !1, E.disabled = !1);
  }, lt = () => {
    const S = F.destroy(), U = qe();
    return S && U && te === 0 && !K && !Q;
  }, Ce = () => (Ke(!1), re || (re = !0, m.disabled = !0, E.disabled = !0, m.removeEventListener?.("click", un), E.removeEventListener?.("click", dn), l.remove?.()), lt()), Tt = (S, U, fe, { requireTrustedEvent: X = !0 } = {}) => X && S?.isTrusted !== !0 ? Promise.resolve() : z || (R || re ? Promise.reject(new V("STALE_CONTROLLER")) : (Ke(!0), lt() ? (R = !0, m.disabled = !0, E.disabled = !0, z = (async () => {
    o(), Ii(n, null, fe, a);
    try {
      const _e = await U();
      if (!r()) throw new V("STALE_CONTROLLER");
      return _e;
    } catch (_e) {
      throw r() && vi(n, void 0, a), _e;
    }
  })(), z) : (N.textContent = "Secure cleanup is still pending. Retry Return or Logout.", m.disabled = !1, E.disabled = !1, Promise.reject(new V("DESTRUCTION_FAILED"))))), un = (S) => {
    Tt(
      S,
      () => t.returnToSite(),
      "Returning to the requesting site."
    ).catch(() => {
    });
  }, dn = (S) => {
    Tt(
      S,
      () => t.logout(),
      "Logged out. Returning to the requesting site."
    ).catch(() => {
    });
  };
  return h.addEventListener("click", He), I.launch.addEventListener("click", Ie), L?.launch.addEventListener?.("click", Ge), L?.confirm.addEventListener?.("click", Te), L?.cancel.addEventListener?.("click", ct), m.addEventListener("click", un), E.addEventListener("click", dn), Object.freeze({
    destroy: Ce,
    logout: () => Tt(
      null,
      () => t.logout(),
      "Logged out. Returning to the requesting site.",
      { requireTrustedEvent: !1 }
    )
  });
}
function bo(e) {
  const t = e?.pathname, n = e?.search ?? "", i = e?.hash ?? "";
  if (typeof t != "string" || n !== "" || i !== "") throw jn();
  const r = yo.exec(t);
  if (!r) throw jn();
  return r[1];
}
function Io(e) {
  const t = e?.window ?? globalThis.window, n = e?.document ?? t?.document ?? globalThis.document, i = e?.mount ?? n?.body, r = typeof t?.fetch == "function" ? t.fetch.bind(t) : typeof globalThis.fetch == "function" ? globalThis.fetch.bind(globalThis) : null, s = e?.relay ?? (e?.controller ? null : Hs({
    fetch: e?.fetch ?? r,
    location: e?.location ?? t?.location
  })), a = e?.registry ?? go, o = e?.controller ?? new Js({
    ...e,
    document: n,
    registry: a,
    relay: s,
    window: t
  }), c = e?.credentialPrompt ?? ((b) => Ao(b));
  let l = null, d = null, u = null, f = null, p = null, g = [], A = 0, h = !1;
  const E = () => {
    (l?.destroy?.() ?? !0) && (l = null), d = null;
  }, m = () => {
    try {
      i?.replaceChildren?.();
    } catch {
    }
  }, O = () => (A = A >= Number.MAX_SAFE_INTEGER ? 1 : A + 1, A), I = () => {
    h = !0, O();
  }, B = (b) => !h && A === b, P = (b) => {
    if (!B(b)) throw new V("STALE_CONTROLLER");
  }, L = (b) => {
    try {
      b?.remove?.();
    } catch {
    }
    u === b && (u = null);
  }, H = () => {
    I(), u?.cancel?.(), u = null, E(), m();
  }, N = (b, W, T) => {
    b?.addEventListener?.(W, T), g.push([b, W, T]);
  }, F = () => {
    for (const [b, W, T] of g)
      try {
        b?.removeEventListener?.(W, T);
      } catch {
      }
    g = [];
  };
  return N(t, "pagehide", H), N(n, "freeze", H), N(t, "beforeunload", H), N(t, "pageshow", (b) => {
    b?.persisted === !0 && H();
  }), Object.freeze({
    controller: o,
    logout() {
      return l ? l.logout() : (I(), u?.cancel?.(), u = null, d = null, o.logout());
    },
    start() {
      if (f) return f;
      const b = A;
      return h ? (f = Promise.reject(new V("STALE_CONTROLLER")), f) : (f = (async () => {
        try {
          const W = bo(t?.location);
          if (typeof o.prepare != "function" || typeof o.executePrepared != "function" || typeof o.unlockPassword != "function") {
            const k = await o.execute(W);
            return P(b), k;
          }
          const T = await o.prepare(W);
          if (P(b), T.transaction.operation === "sdn.auth.raw-challenge.v1") {
            const k = Eo({
              document: n,
              mount: i,
              title: `Choose the legacy profile for ${T.binding.clientDisplayName}`
            });
            u = k;
            const R = await k.promise;
            P(b), u === k && (u = null);
            const z = R === "password-fast-v1-legacy" ? c({
              controller: null,
              document: n,
              mount: i,
              title: `Sign in to ${T.binding.clientDisplayName}`,
              transaction: T
            }) : bi({
              document: n,
              mount: i,
              submitLabel: "Continue",
              title: `Sign in to ${T.binding.clientDisplayName}`
            });
            u = z?.promise ? z : null;
            const Y = z?.promise ? await z.promise : await z;
            P(b);
            const G = await o.unlockLegacy({
              ...Y,
              operation: T.transaction.operation,
              profile: R
            });
            P(b), d = G;
          } else {
            let k = !1;
            for (; ; ) {
              const R = c({
                controller: o,
                document: n,
                mount: i,
                offerRememberedUnlock: !k,
                title: `Sign in to ${T.binding.clientDisplayName}`,
                transaction: T
              });
              k && R?.controls?.rememberStatus && (R.controls.rememberStatus.textContent = "Remembered wallet unavailable. Enter username and password."), u = R?.promise ? R : null;
              const z = R?.promise ? await R.promise : await R;
              if (P(b), z?.remembered === !0)
                try {
                  const G = await o.unlockRemembered();
                  P(b), d = G;
                  break;
                } catch {
                  if (!B(b))
                    throw L(R), new V("STALE_CONTROLLER");
                  k = !0, L(R);
                  continue;
                }
              const Y = await o.unlockPassword(z);
              P(b), d = Y;
              break;
            }
          }
          const Q = await o.executePrepared(T);
          return P(b), L(u), T.transaction.operation === "sdn.wallet.account.v1" ? (d = o.copyPublicIdentity(), l = wo({
            clipboard: e?.clipboard ?? globalThis.navigator?.clipboard,
            controller: o,
            document: n,
            identity: d,
            isAppCurrent: () => B(b),
            makeCredentialPrompt: c,
            mount: i,
            onClear: E,
            wasm: e?.wasm
          })) : (d = null, n?.createElement && i?.replaceChildren && Ii(
            n,
            t,
            "The wallet request completed successfully.",
            i
          )), Q;
        } catch (W) {
          const T = !B(b);
          throw u?.cancel?.(), u = null, E(), F(), T || vi(n, W?.code === "USER_CANCELLED" ? "Cancelled. You may close this window." : void 0, i), await (p ?? o.destroy(T ? "stale-startup" : "startup-failure")), T && W?.code !== "DESTRUCTION_FAILED" ? new V("STALE_CONTROLLER") : W;
        }
      })(), f);
    },
    stop(b = "close") {
      I(), u?.cancel?.(), u = null, E(), m();
      const W = o.destroy(b);
      return p || (F(), p = W), p;
    }
  });
}
async function So(e) {
  const t = Io(e);
  return await t.start(), t;
}
export {
  Ao as createPasswordCredentialPrompt,
  Io as createWalletOriginApp,
  So as mountWalletOriginApp,
  bo as transactionIdFromLocation
};
