import { getWalletOriginCapabilities as Un } from "hd-wallet-wasm";
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
}), Ci = Object.freeze({ reviewedTransform: Ti, schemaVersion: 1 }), Le = new TextEncoder(), jn = Object.getPrototypeOf(Uint8Array.prototype), _i = Object.getOwnPropertyDescriptor(jn, "buffer").get, xi = Object.getOwnPropertyDescriptor(jn, "length").get, Di = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength").get, pn = typeof SharedArrayBuffer > "u" ? null : Object.getOwnPropertyDescriptor(SharedArrayBuffer.prototype, "byteLength").get, we = "sdn-bip32-slip10-purpose-v1", pt = "password-scrypt-v2", xe = "ed25519-over-sha256-jcs-v1", hn = "ed25519-raw-32-v1", Bn = "https://review.spacedatanetwork.org", Vn = "sdn-asset-review-v1", ki = "asset-review:assets.ipfs.01", Pi = "asset-review-authority:assets.ipfs.01", Ut = "sdn-login:sdn.spaceaware.io", Ui = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, se = /^[0-9a-f]{64}$/u, Mn = /^[0-9a-f]{128}$/u, Me = /^sha256:[0-9a-f]{64}$/u, ji = /^[1-9A-HJ-NP-Za-km-z]+$/u, Bi = /^b[a-z2-7]{58}$/u, jt = "abcdefghijklmnopqrstuvwxyz234567", $n = 131072, Vi = [
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
], ht = [
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
], Bt = [
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
function St(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function oe(e, t) {
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
function Fn(e) {
  typeof e != "string" && y("wire JSON must be a string"), Le.encode(e).byteLength > $n && y("wire JSON is too large"), e.charCodeAt(0) === 65279 && y("wire JSON must not contain a BOM");
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
        return oe(l, "JSON string");
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
function Wn(e) {
  return typeof e == "string" ? Fn(e) : e;
}
function qi(e, t) {
  if (e.byteLength !== t.byteLength) return !1;
  let n = 0;
  for (let i = 0; i < e.byteLength; i += 1) n |= e[i] ^ t[i];
  return n === 0;
}
function Z(e, t, n) {
  const i = Wn(e);
  St(i) || y(`${n} must be a JSON object`), Object.getOwnPropertySymbols(i).length !== 0 && y(`${n} has an unknown symbol field`);
  const r = Object.getOwnPropertyNames(i).sort(), s = [...t].sort(), a = Le.encode(r.join("\0")), o = Le.encode(s.join("\0"));
  qi(a, o) || y(`${n} has missing or unknown fields`);
  for (const c of r) {
    const l = Object.getOwnPropertyDescriptor(i, c);
    (!l || !l.enumerable || !("value" in l)) && y(`${n}.${c} must be an enumerable data field`), l.value === void 0 && y(`${n}.${c} must not be undefined`);
  }
  return i;
}
function it(e) {
  if (!e || typeof e != "object" || Object.isFrozen(e)) return e;
  for (const t of Object.values(e)) it(t);
  return Object.freeze(e);
}
function ae(e) {
  return it(Object.fromEntries([...e].sort(([t], [n]) => t < n ? -1 : t > n ? 1 : 0)));
}
function Ht(e, t, n) {
  (!Array.isArray(e) || Object.getPrototypeOf(e) !== Array.prototype || e.length !== t) && y(`${n} must be a plain array with exactly ${t} values`);
  const i = [...Array.from({ length: t }, (s, a) => String(a)), "length"], r = Reflect.ownKeys(e);
  (r.length !== i.length || r.some((s, a) => s !== i[a])) && y(`${n} must be a dense plain array`);
  for (let s = 0; s < t; s += 1) {
    const a = Object.getOwnPropertyDescriptor(e, String(s));
    (!a || !a.enumerable || !("value" in a) || a.value === void 0) && y(`${n} must contain enumerable data values`);
  }
}
function Hi(e) {
  const t = Z(
    e,
    ["reviewedTransform", "schemaVersion"],
    "asset review protocol policy"
  );
  S(t.schemaVersion, 1, "asset review protocol schemaVersion");
  const n = Z(t.reviewedTransform, [
    "metersPerSourceUnit",
    "quaternionNormTolerance",
    "scaleComponentExclusiveMin",
    "scaleComponentInclusiveMax",
    "translationComponentAbsMax",
    "upAxes"
  ], "asset review transform policy"), i = Z(
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
  return (n.quaternionNormTolerance <= 0 || n.scaleComponentInclusiveMax <= n.scaleComponentExclusiveMin || n.translationComponentAbsMax <= 0) && y("asset review transform policy bounds are inconsistent"), Ht(n.upAxes, 3, "asset review up-axis policy"), n.upAxes.join("\0") !== "X_UP\0Y_UP\0Z_UP" && y("asset review up-axis policy is invalid"), ae([
    ["metersPerSourceUnit", ae(Object.entries(i))],
    ["quaternionNormTolerance", n.quaternionNormTolerance],
    ["scaleComponentExclusiveMin", n.scaleComponentExclusiveMin],
    ["scaleComponentInclusiveMax", n.scaleComponentInclusiveMax],
    ["translationComponentAbsMax", n.translationComponentAbsMax],
    ["upAxes", Object.freeze(Array.from({ length: 3 }, (r, s) => n.upAxes[s]))]
  ]);
}
const Ce = Hi(Ci);
function S(e, t, n) {
  return e !== t && y(`${n} must equal ${JSON.stringify(t)}`), e;
}
function Gt(e, t, n) {
  return t.includes(e) || y(`${n} is not an allowed value`), e;
}
function K(e, t, n) {
  return oe(e, n), t.test(e) || y(`${n} has an invalid encoding`), e;
}
function be(e, t) {
  return oe(e, t), (!Ui.test(e) || new Date(e).toISOString() !== e) && y(`${t} must be exact RFC3339 milliseconds UTC`), e;
}
function Rt(e, t) {
  const n = Date.parse(e), i = Date.parse(t);
  i > n && i - n <= 3e5 || y("request lifetime must be in (0, 300] seconds");
}
function Gi(e, t) {
  (!ArrayBuffer.isView(e) || !(e instanceof Uint8Array) || Object.getPrototypeOf(e) !== Uint8Array.prototype) && y(`${t} must be a plain Uint8Array`), xi.call(e) !== 32 && y(`${t} must be exactly 32 bytes`);
  const i = Array.from({ length: 32 }, (d, u) => String(u)), r = Reflect.ownKeys(e);
  (r.length !== i.length || r.some((d, u) => d !== i[u])) && y(`${t} Uint8Array must contain only 32 indexed bytes`);
  const s = _i.call(e);
  let a = !1;
  if (pn)
    try {
      pn.call(s), a = !0;
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
function zn(e, t) {
  oe(e, t), /^[A-Za-z0-9_-]{43}$/u.test(e) || y(`${t} must be canonical unpadded base64url`);
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
      n -= 5, i += jt[t >>> n & 31], t &= (1 << n) - 1;
  return n !== 0 && (i += jt[t << 5 - n & 31]), i;
}
function Ji(e, t) {
  oe(e, "modelCid"), Bi.test(e) || y("modelCid must be canonical CIDv1 raw sha2-256 base32");
  let n = 0, i = 0;
  const r = [];
  for (const o of e.slice(1)) {
    const c = jt.indexOf(o);
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
function Vt(e) {
  return e === null || typeof e == "boolean" ? JSON.stringify(e) : typeof e == "string" ? (oe(e, "canonical JSON string"), JSON.stringify(e)) : typeof e == "number" ? (Number.isFinite(e) || y("canonical JSON numbers must be finite"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(Vt).join(",")}]` : (St(e) || y("canonical JSON contains an unsupported value"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${Vt(e[t])}`).join(",")}}`);
}
function Kt(e, t) {
  for (const n of ["identityScheme", "keyId", "signatureProfile"])
    e[n] !== t[n] && y(`canonicalEnvelope ${n} must match the signature result`);
}
function Qi(e, t) {
  const i = Z(e, [
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
  Kt(i, t), S(i.audience, Ut, "SDN login envelope audience"), K(i.challengeSha256, se, "SDN login envelope challengeSha256"), S(i.clientId, "sdn-node-console-v1", "SDN login envelope clientId"), be(i.issuedAt, "SDN login envelope issuedAt"), be(i.expiresAt, "SDN login envelope expiresAt"), Rt(i.issuedAt, i.expiresAt), S(i.identityScheme, we, "SDN login envelope identityScheme"), K(i.keyId, Me, "SDN login envelope keyId"), S(i.kind, "sdn-login", "SDN login envelope kind"), K(i.nonce, se, "SDN login envelope nonce"), S(i.protocolVersion, 2, "SDN login envelope protocolVersion"), S(i.requestOrigin, "https://sdn.spaceaware.io", "SDN login envelope requestOrigin"), S(i.signatureProfile, xe, "SDN login envelope signatureProfile");
}
function Yi(e, t) {
  const n = Z(
    e,
    [...ht, "kind"],
    "authority activation canonicalEnvelope"
  );
  Kt(n, t), S(n.kind, "asset-review-authority-activation", "activation envelope kind"), Hn(Object.fromEntries(ht.map((i) => [i, n[i]])));
}
function Xi(e, t) {
  const n = e?.decision === "approve" ? ["note", "reviewedTransform"] : e?.decision === "disapprove" ? ["reason"] : [], i = [...Bt, ...n], r = Z(e, [
    ...i,
    "identityScheme",
    "keyId",
    "kind",
    "purpose",
    "signatureProfile"
  ], "asset review decision canonicalEnvelope");
  Kt(r, t), S(r.identityScheme, we, "decision envelope identityScheme"), K(r.keyId, Me, "decision envelope keyId"), S(r.kind, "asset-review-attestation", "decision envelope kind"), S(r.purpose, "asset-review-approval", "decision envelope purpose"), S(r.signatureProfile, xe, "decision envelope signatureProfile"), Gn(Object.fromEntries(i.map((s) => [s, r[s]])));
}
function Zi(e, t, n) {
  oe(e, "canonicalEnvelope"), Le.encode(e).byteLength > $n && y("canonicalEnvelope is too large");
  const i = Fn(e);
  return (!St(i) || i.kind !== t) && y("canonicalEnvelope kind does not match the operation"), Vt(i) !== e && y("canonicalEnvelope must be exact JCS"), t === "sdn-login" ? Qi(i, n) : t === "asset-review-authority-activation" ? Yi(i, n) : t === "asset-review-attestation" ? Xi(i, n) : y("canonicalEnvelope operation is not registered"), e;
}
function er(e, t) {
  const n = Z(e, Mi, `identity key ${t.purpose}`);
  return S(n.purpose, t.purpose, "key purpose"), S(n.identityScheme, we, "key identityScheme"), S(n.seedProfile, pt, "key seedProfile"), S(n.signatureProfile, t.signatureProfile, "key signatureProfile"), S(n.curve, t.curve, "key curve"), S(n.derivation, "slip10", "key derivation"), S(n.path, t.path, "key path"), S(n.encoding, "raw", "key encoding"), K(n.publicKeyHex, se, "key publicKeyHex"), S(n.bip32Fingerprint, null, "key bip32Fingerprint"), K(n.keyId, Me, "key keyId"), ae([
    ["bip32Fingerprint", null],
    ["curve", n.curve],
    ["derivation", "slip10"],
    ["encoding", "raw"],
    ["identityScheme", we],
    ["keyId", n.keyId],
    ["path", n.path],
    ["publicKeyHex", n.publicKeyHex],
    ["purpose", n.purpose],
    ["seedProfile", pt],
    ["signatureProfile", n.signatureProfile]
  ]);
}
function tr(e) {
  const t = Z(e, Vi, "WalletPublicIdentity");
  S(t.schemaVersion, 1, "identity schemaVersion"), S(t.identityScheme, we, "identity identityScheme"), S(t.seedProfile, pt, "identity seedProfile"), t.accountIndex !== 0 && y("public wallet identity must use account 0"), S(t.accountLabel, null, "identity accountLabel"), oe(t.accountXpub, "identity accountXpub"), /^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(t.accountXpub) || y("identity accountXpub is invalid"), oe(t.accountPeerId, "identity accountPeerId"), (!t.accountPeerId.startsWith("16Uiu2H") || t.accountPeerId.length < 40 || t.accountPeerId.length > 64 || !ji.test(t.accountPeerId)) && y("identity accountPeerId is invalid"), K(t.accountFingerprint, /^[0-9a-f]{8}$/u, "identity accountFingerprint"), Ht(t.keys, 3, "public identity keys array");
  const n = [
    {
      purpose: "asset-review-approval",
      curve: "ed25519",
      signatureProfile: xe,
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
      signatureProfile: xe,
      path: "m/44'/0'/0'/0'/0'"
    }
  ], i = Array.from(
    { length: 3 },
    (r, s) => er(t.keys[s], n[s])
  );
  return ae([
    ["accountFingerprint", t.accountFingerprint],
    ["accountIndex", 0],
    ["accountLabel", null],
    ["accountPeerId", t.accountPeerId],
    ["accountXpub", t.accountXpub],
    ["identityScheme", we],
    ["keys", it(i)],
    ["schemaVersion", 1],
    ["seedProfile", pt]
  ]);
}
function qn(e, t) {
  const n = Z(e, $i, `${t} result`);
  S(n.schemaVersion, 1, "connection result schemaVersion"), Gt(n.event, ["connected", "disconnected"], "connection event"), t === "connect" && n.event !== "connected" && y("connect result must be connected");
  let i = null, r = null;
  return n.event === "connected" ? (i = tr(n.identity), r = be(n.connectionExpiresAt, "connectionExpiresAt")) : (n.identity !== null || n.connectionExpiresAt !== null) && y("disconnected result must clear identity and expiry"), ae([
    ["connectionExpiresAt", r],
    ["event", n.event],
    ["identity", i],
    ["schemaVersion", 1]
  ]);
}
function nr(e) {
  const t = Z(e, Fi, "raw signature");
  return S(t.schemaVersion, 1, "signature schemaVersion"), K(t.keyId, Me, "signature keyId"), Gt(t.identityScheme, [
    "sdn-fast-password-auth-v1-legacy",
    "sdn-bip39-auth-v1-legacy"
  ], "raw signature identityScheme"), S(t.algorithm, "ed25519", "signature algorithm"), S(t.encoding, "raw", "signature encoding"), S(t.signatureProfile, hn, "signature profile"), K(t.signatureHex, Mn, "signatureHex"), ae([
    ["algorithm", "ed25519"],
    ["encoding", "raw"],
    ["identityScheme", t.identityScheme],
    ["keyId", t.keyId],
    ["schemaVersion", 1],
    ["signatureHex", t.signatureHex],
    ["signatureProfile", hn]
  ]);
}
function Jt(e, t) {
  const n = Z(e, Wi, "canonical signature");
  return S(n.schemaVersion, 1, "signature schemaVersion"), K(n.keyId, Me, "signature keyId"), S(n.identityScheme, we, "signature identityScheme"), S(n.algorithm, "ed25519", "signature algorithm"), S(n.encoding, "raw", "signature encoding"), S(n.signatureProfile, xe, "signature profile"), Zi(n.canonicalEnvelope, t, n), K(n.signedDigestSha256, se, "signedDigestSha256"), K(n.signatureHex, Mn, "signatureHex"), ae([
    ["algorithm", "ed25519"],
    ["canonicalEnvelope", n.canonicalEnvelope],
    ["encoding", "raw"],
    ["identityScheme", we],
    ["keyId", n.keyId],
    ["schemaVersion", 1],
    ["signatureHex", n.signatureHex],
    ["signatureProfile", xe],
    ["signedDigestSha256", n.signedDigestSha256]
  ]);
}
function ir(e) {
  const t = Z(e, ["challengeBase64url", "protocolVersion"], "SDN login v1 request");
  return S(t.protocolVersion, 1, "SDN login v1 protocolVersion"), zn(t.challengeBase64url, "challengeBase64url"), ae([
    ["challengeBase64url", t.challengeBase64url],
    ["protocolVersion", 1]
  ]);
}
function rr(e) {
  const t = Z(e, [
    "audience",
    "challengeBase64url",
    "expiresAt",
    "issuedAt",
    "nonce",
    "protocolVersion"
  ], "SDN login v2 request");
  return S(t.protocolVersion, 2, "SDN login v2 protocolVersion"), S(t.audience, Ut, "SDN login v2 audience"), zn(t.challengeBase64url, "challengeBase64url"), K(t.nonce, se, "SDN login nonce"), be(t.issuedAt, "SDN login issuedAt"), be(t.expiresAt, "SDN login expiresAt"), Rt(t.issuedAt, t.expiresAt), ae([
    ["audience", Ut],
    ["challengeBase64url", t.challengeBase64url],
    ["expiresAt", t.expiresAt],
    ["issuedAt", t.issuedAt],
    ["nonce", t.nonce],
    ["protocolVersion", 2]
  ]);
}
function Hn(e) {
  const t = Z(e, ht, "authority activation request");
  return S(t.protocolVersion, 1, "activation protocolVersion"), S(t.audience, Pi, "activation audience"), S(t.requestOrigin, Bn, "activation requestOrigin"), S(t.clientId, Vn, "activation clientId"), S(t.serviceInstance, "assets.ipfs.01/asset-review-attestation", "activation serviceInstance"), S(t.purpose, "asset-review-authority-activation", "activation purpose"), K(t.nonce, se, "activation nonce"), be(t.issuedAt, "activation issuedAt"), be(t.expiresAt, "activation expiresAt"), Rt(t.issuedAt, t.expiresAt), K(t.publicKeyHex, se, "activation publicKeyHex"), K(t.keyId, Me, "activation keyId"), S(t.identityScheme, we, "activation identityScheme"), S(t.signatureProfile, xe, "activation signatureProfile"), ae(ht.map((n) => [n, t[n]]));
}
function Ct(e, t, n) {
  Ht(e, t, `${n} array`);
  const i = Array.from({ length: t }, (r, s) => {
    const a = e[s];
    return (typeof a != "number" || !Number.isFinite(a)) && y(`${n} values must be finite numbers`), a;
  });
  return it(i);
}
function sr(e) {
  const t = Z(e, zi, "reviewedTransform"), n = Ct(t.translation, 3, "translation"), i = Ct(t.rotation, 4, "rotation"), r = Ct(t.scale, 3, "scale");
  n.some((a) => Math.abs(a) > Ce.translationComponentAbsMax) && y("translation values exceed the reviewed transform policy"), r.some((a) => a <= Ce.scaleComponentExclusiveMin || a > Ce.scaleComponentInclusiveMax) && y("scale values exceed the reviewed transform policy");
  const s = Math.hypot(...i);
  return Math.abs(s - 1) > Ce.quaternionNormTolerance && y("rotation must be a unit quaternion within the reviewed tolerance"), Gt(t.upAxis, Ce.upAxes, "upAxis"), oe(t.sourceUnits, "sourceUnits"), Object.hasOwn(Ce.metersPerSourceUnit, t.sourceUnits) || y("sourceUnits is not allowed by the reviewed transform policy"), (typeof t.metersPerSourceUnit != "number" || !Number.isFinite(t.metersPerSourceUnit) || t.metersPerSourceUnit !== Ce.metersPerSourceUnit[t.sourceUnits]) && y("metersPerSourceUnit must exactly match sourceUnits"), ae([
    ["metersPerSourceUnit", t.metersPerSourceUnit],
    ["rotation", i],
    ["scale", r],
    ["sourceUnits", t.sourceUnits],
    ["translation", n],
    ["upAxis", t.upAxis]
  ]);
}
function yn(e) {
  return e >= 9 && e <= 13 || e === 32 || e === 160 || e === 5760 || e >= 8192 && e <= 8202 || e === 8232 || e === 8233 || e === 8239 || e === 8287 || e === 12288 || e === 65279;
}
function mn(e, t, n) {
  if (n && e === null) return null;
  oe(e, t), (e.length === 0 || Le.encode(e).byteLength > 2e3) && y(`${t} has an invalid length`);
  const i = Array.from(e, (r) => r.codePointAt(0));
  return (yn(i[0]) || yn(i[i.length - 1])) && y(`${t} must already be trimmed`), e;
}
function Gn(e) {
  const t = Wn(e);
  St(t) || y("asset review decision request must be a JSON object");
  const n = Object.getOwnPropertyDescriptor(t, "decision");
  (!n || !n.enumerable || !("value" in n) || n.value === void 0) && y("asset review decision request has an invalid decision field");
  const i = n.value, r = i === "approve" ? ["note", "reviewedTransform"] : i === "disapprove" ? ["reason"] : [], s = Z(t, [...Bt, ...r], "asset review decision request");
  S(s.protocolVersion, 1, "decision protocolVersion"), S(s.audience, ki, "decision audience"), S(s.requestOrigin, Bn, "decision requestOrigin"), S(s.clientId, Vn, "decision clientId"), K(s.challengeId, se, "challengeId"), K(s.nonce, se, "decision nonce"), be(s.issuedAt, "decision issuedAt"), be(s.expiresAt, "decision expiresAt"), Rt(s.issuedAt, s.expiresAt), K(s.modelSha256, se, "modelSha256"), K(s.metadataSha256, se, "metadataSha256"), s.previousDecisionHead !== null && K(s.previousDecisionHead, se, "previousDecisionHead"), Ji(s.modelCid, s.modelSha256), (!Number.isSafeInteger(s.modelBytes) || s.modelBytes <= 0) && y("modelBytes must be a positive safe integer"), oe(s.candidateKey, "candidateKey");
  const a = "asset-review:", o = `:${s.modelSha256}`;
  (!s.candidateKey.startsWith(a) || !s.candidateKey.endsWith(o) || Le.encode(s.candidateKey).byteLength > 206) && y("candidateKey does not bind the model digest");
  const c = s.candidateKey.slice(a.length, -o.length);
  (!/^[a-z0-9-]+\/[a-z0-9][a-z0-9-]*$/u.test(c) || Le.encode(c).byteLength > 128) && y("candidateKey entityId is invalid");
  const l = Bt.map((u) => [u, s[u]]);
  return i === "approve" ? (l.push(["note", mn(s.note, "note", !0)]), l.push(["reviewedTransform", sr(s.reviewedTransform)])) : l.push(["reason", mn(s.reason, "reason", !1)]), Object.values(s).filter((u) => typeof u == "string").reduce((u, f) => u + Le.encode(f).byteLength, 0) > 16384 && y("decision request strings exceed 16 KiB"), ae(l);
}
function Kn(e, t) {
  return Z(e, [], t), it({});
}
function or(e) {
  return Kn(e, "wallet connect request");
}
function ar(e) {
  return qn(e, "connect");
}
function cr(e) {
  return Kn(e, "wallet account request");
}
function Jn(e) {
  return qn(e, "account");
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
  return Jt(e, "sdn-login");
}
function pr(e) {
  return Hn(e);
}
function hr(e) {
  return Jt(e, "asset-review-authority-activation");
}
function yr(e) {
  return Gn(e);
}
function mr(e) {
  return Jt(e, "asset-review-attestation");
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
]), _t = /^[0-9a-f]{64}$/u, Qn = /^[A-Za-z0-9_-]{43}$/u, Ar = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, Er = new TextEncoder(), Yn = Object.freeze({
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
    buildResult: Jn,
    connect: !0
  }),
  "sdn.wallet.connect.v1": Object.freeze({
    parseRequest: or,
    buildResult: ar,
    connect: !0
  })
});
class Ne extends Error {
  constructor(t) {
    super(t), this.name = "WalletOperationError", this.code = t;
  }
}
function $(e) {
  throw new Ne(e);
}
function Qt(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function wr(e, t, n = "INVALID_TRANSACTION") {
  Qt(e) || $(n);
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
function Xe(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(Xe));
  if (!Qt(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = Xe(e[n]);
  return Object.freeze(t);
}
function yt(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" ? JSON.stringify(e) : typeof e == "number" ? (Number.isFinite(e) || $("INVALID_TRANSACTION"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(yt).join(",")}]` : (Qt(e) || $("INVALID_TRANSACTION"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${yt(e[t])}`).join(",")}}`);
}
async function Ir(e, t) {
  typeof t != "function" && $("CRYPTO_UNAVAILABLE");
  let n;
  try {
    n = await t(Er.encode(yt(e)));
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
    if (n instanceof Ne) throw n;
    $("UNREGISTERED_TRANSACTION");
  }
  $("UNREGISTERED_TRANSACTION");
}
function ne({ document: e, window: t }) {
  let n = !1;
  try {
    n = t?.top === t;
  } catch {
    n = !1;
  }
  (!n || e?.visibilityState !== "visible" || typeof e?.hasFocus != "function" || e.hasFocus() !== !0) && $("WALLET_CONTEXT_UNTRUSTED");
}
async function xt(e, {
  registry: t,
  relay: n,
  sha256: i,
  window: r,
  now: s = () => Date.now(),
  expectedTransactionId: a = null
}) {
  const o = wr(e, gr);
  (o.schemaVersion !== 1 || typeof o.clientDisplayName != "string" || o.clientDisplayName.length < 1 || o.clientDisplayName.length > 80 || !_t.test(o.transactionId) || !_t.test(o.state) || !_t.test(o.requestSha256) || !Qn.test(o.resultToken)) && $("INVALID_TRANSACTION"), a !== null && o.transactionId !== a && $("INVALID_TRANSACTION");
  const c = Yn[o.operation];
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
  } catch (E) {
    if (E instanceof Ne) throw E;
    $("CRYPTO_UNAVAILABLE");
  }
  vr(p, o.requestSha256) || $("REQUEST_HASH_MISMATCH");
  const g = { ...o, request: f };
  return Object.freeze({
    binding: Xe(l),
    operation: c,
    request: Xe(f),
    transaction: Xe(g)
  });
}
function Rr(e) {
  Qn.test(e) || $("INVALID_TRANSACTION");
  let t;
  try {
    t = globalThis.atob(`${e.replace(/-/gu, "+").replace(/_/gu, "/")}=`);
  } catch {
    $("INVALID_TRANSACTION");
  }
  const n = Uint8Array.from(t, (i) => i.charCodeAt(0));
  return n.byteLength !== 32 && $("INVALID_TRANSACTION"), n;
}
function de(e, t, n, i) {
  const r = e.createElement("div");
  r.className = "wallet-confirmation-row";
  const s = e.createElement("strong");
  s.textContent = `${n}: `;
  const a = e.createElement("span");
  a.textContent = i === null ? "null" : typeof i == "string" ? i : yt(i), r.append(s, a), t.append(r);
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
  a.id = "wallet-confirmation-heading", a.textContent = "Confirm wallet action", e.append(a), de(n, e, "Client", t.clientDisplayName), de(n, e, "Requesting origin", t.requestOrigin), de(n, e, "Operation", t.operation), t.audience !== void 0 && de(n, e, "Audience", t.audience), t.callbackUri !== void 0 && de(n, e, "Callback URI", t.callbackUri), s && (de(n, e, "Transaction ID", s.transactionId), de(n, e, "Request hash", s.requestSha256), de(n, e, "Registry release", s.registryVersion), de(n, e, "Transaction expiry", s.expiresAt));
  const o = t.operation.startsWith("sdn.asset-review.") ? "asset-review-approval" : t.operation.startsWith("sdn.auth.") ? "sdn-authentication" : null, c = o && Array.isArray(i?.keys) ? i.keys.find((l) => l?.purpose === o) : null;
  c?.keyId && de(n, e, "Signing key ID", c.keyId);
  for (const l of Object.keys(r).sort()) de(n, e, l, r[l]);
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
  const p = new Promise((N, v) => {
    u = N, f = v;
  }), g = (N, v) => {
    d || v?.isTrusted !== !0 || (d = !0, c.disabled = !0, l.disabled = !0, N ? u() : f(new Ne("USER_CANCELLED")));
  }, E = (N) => g(!0, N), h = (N) => g(!1, N), A = (N) => {
    if (N?.isTrusted !== !0) return;
    if (N.key === "Escape") {
      N.preventDefault?.(), g(!1, N);
      return;
    }
    if (N.key !== "Tab") return;
    N.preventDefault?.();
    const v = t.activeElement;
    N.shiftKey === !0 ? (v === c ? l : c).focus?.() : (v === l ? c : l).focus?.();
  }, m = (N) => {
    let v = !1;
    try {
      v = a.contains?.(N?.target) === !0;
    } catch {
      v = !1;
    }
    v || c.focus?.();
  };
  c.addEventListener("click", E), l.addEventListener("click", h), a.addEventListener("keydown", A), t.addEventListener?.("focusin", m);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({
    promise: p,
    cancel(N = "STALE_CONTROLLER") {
      d || (d = !0, f(new Ne(N)));
    },
    destroy() {
      c.removeEventListener?.("click", E), l.removeEventListener?.("click", h), a.removeEventListener?.("keydown", A), t.removeEventListener?.("focusin", m), a.remove();
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
  const a = s?.sdn ?? s, o = Yn[r.operation];
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
  return Jn({
    connectionExpiresAt: null,
    event: "disconnected",
    identity: null,
    schemaVersion: 1
  });
}
const gn = "webauthn-prf-hkdf-sha256-aes256gcm-v2", Cr = "sdn-bip32-slip10-purpose-v1", _r = "password-scrypt-v2", xr = Object.freeze([
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
]), kr = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, Pr = /^[0-9a-f]{64}$/u, Ur = /^[A-Za-z0-9_-]+$/u, jr = new TextEncoder(), ie = "sdn.wallet.remembered.v2", Oe = `${ie}.pending`, ut = 16 * 1024, Xn = Object.freeze([
  "wallet_storage_metadata",
  "wallet_storage_encrypted",
  "wallet_storage_passkey_credential",
  "encrypted_wallet",
  "passkey_credential",
  "passkey_wallet"
]), Zn = /* @__PURE__ */ new Set([
  ie,
  Oe,
  ...Xn
]), Mt = /* @__PURE__ */ new WeakSet();
class rt extends Error {
  constructor(t) {
    super(t), this.name = "WalletStorageError", this.code = t;
  }
}
function T(e) {
  throw new rt(e);
}
function ei(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function An(e, t) {
  ei(e) || T("INVALID_REMEMBERED_WALLET");
  let n;
  try {
    n = Reflect.ownKeys(e);
  } catch {
    T("INVALID_REMEMBERED_WALLET");
  }
  n.some((a) => typeof a != "string") && T("INVALID_REMEMBERED_WALLET");
  const i = [...t].sort(), r = [...n].sort();
  (r.length !== i.length || r.some((a, o) => a !== i[o])) && T("INVALID_REMEMBERED_WALLET");
  const s = {};
  for (const a of i) {
    let o;
    try {
      o = Object.getOwnPropertyDescriptor(e, a);
    } catch {
      T("INVALID_REMEMBERED_WALLET");
    }
    (!o?.enumerable || !("value" in o) || o.value === void 0) && T("INVALID_REMEMBERED_WALLET"), s[a] = o.value;
  }
  return s;
}
function En(e) {
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
function me(e, { minimum: t = 0, maximum: n = 65536, exact: i = null } = {}) {
  (typeof e != "string" || e.length === 0 || !Ur.test(e)) && T("INVALID_REMEMBERED_WALLET");
  const r = e.length % 4;
  r === 1 && T("INVALID_REMEMBERED_WALLET");
  const s = e.replace(/-/gu, "+").replace(/_/gu, "/") + "=".repeat((4 - r) % 4);
  let a;
  try {
    a = atob(s);
  } catch {
    T("INVALID_REMEMBERED_WALLET");
  }
  const o = new Uint8Array(a.length);
  for (let c = 0; c < a.length; c += 1) o[c] = a.charCodeAt(c);
  return (Br(o) !== e || o.length < t || o.length > n || i !== null && o.length !== i) && T("INVALID_REMEMBERED_WALLET"), o;
}
function Yt(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" || typeof e == "number" && Number.isFinite(e) ? JSON.stringify(e) : (ei(e) || T("INVALID_REMEMBERED_WALLET"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${Yt(e[t])}`).join(",")}}`);
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
function ti(e) {
  const t = An(e, xr), n = An(t.aad, Dr), i = En(t.canonicalUsername) ? jr.encode(t.canonicalUsername) : null;
  return (t.schemaVersion !== 2 || t.storageProfile !== gn || n.schemaVersion !== 2 || n.storageProfile !== gn || n.identityScheme !== Cr || n.seedProfile !== _r || !i || i.length < 3 || i.length > 64 || !/^[a-z0-9][a-z0-9._-]*$/u.test(t.canonicalUsername) || !Pr.test(n.usernameSha256) || t.credentialIdBase64url !== n.credentialIdBase64url || !En(t.createdAt) || !kr.test(t.createdAt) || new Date(t.createdAt).toISOString() !== t.createdAt) && T("INVALID_REMEMBERED_WALLET"), me(t.credentialIdBase64url, { minimum: 1, maximum: 1024 }), me(t.ciphertextBase64url, { minimum: 17, maximum: 1024 }), me(t.hkdfSaltBase64url, { exact: 32 }), me(t.nonceBase64url, { exact: 12 }), me(t.prfInputBase64url, { exact: 32 }), Vr({ ...t, aad: n });
}
function Mr(e) {
  return Yt(ti(e));
}
function Xt(e) {
  (typeof e != "string" || e.length === 0 || e.length > 131072) && T("INVALID_REMEMBERED_WALLET");
  let t;
  try {
    t = JSON.parse(e);
  } catch {
    T("INVALID_REMEMBERED_WALLET");
  }
  const n = ti(t);
  return Yt(n) !== e && T("INVALID_REMEMBERED_WALLET"), n;
}
function $t(e, t, { pending: n = !1 } = {}) {
  (!e || typeof e.getItem != "function") && T("STORAGE_UNAVAILABLE");
  let i;
  try {
    i = e.getItem(t);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  if (i === null) return Object.freeze({ raw: null, record: null, status: "empty" });
  typeof i != "string" && T("STORAGE_UNAVAILABLE");
  const r = i.length > ut;
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
    const s = Xt(i);
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
function ni(e) {
  const t = $t(e, Oe, { pending: !0 }), n = $t(e, ie), i = t.status === "empty", r = n.status === "empty" || n.status === "valid";
  return Object.freeze({
    active: n,
    canRestore: i && n.status === "valid",
    canSetup: i && r,
    pending: t
  });
}
function $r(e, t) {
  const n = ni(e);
  n.canSetup || T("STORAGE_QUARANTINED");
  const i = Mr(t);
  try {
    e.setItem(Oe, i);
  } catch (a) {
    throw a;
  }
  let r;
  try {
    r = e.getItem(Oe);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  r !== i && T("STORAGE_WRITE_FAILED"), Xt(r);
  const s = Object.freeze({
    previousActiveRaw: n.active.raw,
    serialized: i,
    storage: e
  });
  return Mt.add(s), s;
}
function Fr(e, t) {
  (!Mt.has(t) || t.storage !== e) && T("INVALID_STORAGE_TRANSACTION"), Mt.delete(t);
  let n, i;
  try {
    n = e.getItem(Oe), i = e.getItem(ie);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  (n !== t.serialized || i !== t.previousActiveRaw) && T("STORAGE_COLLISION"), e.removeItem(Oe), e.getItem(Oe) !== null && T("STORAGE_WRITE_FAILED"), e.setItem(ie, t.serialized);
}
function Zt(e, t) {
  Zn.has(t) || T("INVALID_STORAGE_KEY"), (!e || typeof e.getItem != "function") && T("STORAGE_UNAVAILABLE");
  let n;
  try {
    n = e.getItem(t);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  if (n === null) return null;
  if (typeof n != "string" && T("STORAGE_UNAVAILABLE"), t === ie && n.length <= ut)
    try {
      Xt(n), T("NOT_QUARANTINED");
    } catch (i) {
      if (i instanceof rt && i.code === "NOT_QUARANTINED") throw i;
    }
  return Object.freeze({
    exportable: n.length <= ut,
    key: t,
    oversized: n.length > ut,
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
    ie,
    Oe,
    ...Xn
  ]) {
    let i;
    try {
      i = Zt(e, n);
    } catch (r) {
      if (r instanceof rt && r.code === "NOT_QUARANTINED") continue;
      throw r;
    }
    i && t.push(Wr(i));
  }
  return Object.freeze(t);
}
function qr(e, t) {
  const n = Zt(e, t);
  return n || T("QUARANTINE_NOT_FOUND"), n.exportable || T("QUARANTINE_EXPORT_TOO_LARGE"), n.raw;
}
function Hr(e, t, n) {
  Zn.has(t) || T("INVALID_STORAGE_KEY"), n !== t && T("CONFIRMATION_REQUIRED"), Zt(e, t) || T("QUARANTINE_NOT_FOUND");
  try {
    e.removeItem(t), e.getItem(t) !== null && T("STORAGE_WRITE_FAILED");
  } catch (r) {
    if (r instanceof rt) throw r;
    T("STORAGE_UNAVAILABLE");
  }
}
function Gr(e, t) {
  t !== ie && T("CONFIRMATION_REQUIRED");
  const n = $t(e, ie);
  n.status === "empty" && T("REMEMBER_UNAVAILABLE"), n.status !== "valid" && T("STORAGE_QUARANTINED");
  try {
    e.removeItem(ie), e.getItem(ie) !== null && T("STORAGE_WRITE_FAILED");
  } catch (i) {
    if (i instanceof rt) throw i;
    T("STORAGE_UNAVAILABLE");
  }
}
const mt = "sdn-bip32-slip10-purpose-v1", gt = "password-scrypt-v2", Ft = "m/44'/0'/0'/2'/0'", en = "Approval unavailable — migrate to the new wallet profile", wn = Object.freeze({
  "bip39-mnemonic-v1-legacy": "sdn-bip39-auth-v1-legacy",
  "password-fast-v1-legacy": "sdn-fast-password-auth-v1-legacy"
}), bn = new TextEncoder(), Kr = Uint8Array.prototype.fill, tn = Object.freeze([
  "accountFingerprint",
  "accountIndex",
  "accountLabel",
  "accountPeerId",
  "accountXpub",
  "identityScheme",
  "keys",
  "schemaVersion",
  "seedProfile"
]), nn = Object.freeze([
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
]), At = Object.freeze([
  Object.freeze({
    curve: "ed25519",
    path: Ft,
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
class ii extends Error {
  constructor(t) {
    super(t === "APPROVAL_UNAVAILABLE" ? en : t), this.name = "WalletAccountError", this.code = t;
  }
}
function B(e) {
  throw new ii(e);
}
function In(e) {
  if (e instanceof Uint8Array)
    try {
      Kr.call(e, 0);
    } catch {
    }
}
function De(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(De));
  if (!e || typeof e != "object") return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = De(e[n]);
  return Object.freeze(t);
}
function Jr(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function Et(e, t) {
  Jr(e) || B("INVALID_PUBLIC_IDENTITY");
  let n;
  try {
    n = Reflect.ownKeys(e);
  } catch {
    B("INVALID_PUBLIC_IDENTITY");
  }
  n.some((a) => typeof a != "string") && B("INVALID_PUBLIC_IDENTITY");
  const i = [...n].sort(), r = [...t].sort();
  (i.length !== r.length || i.some((a, o) => a !== r[o])) && B("INVALID_PUBLIC_IDENTITY");
  const s = {};
  for (const a of i) {
    let o;
    try {
      o = Object.getOwnPropertyDescriptor(e, a);
    } catch {
      B("INVALID_PUBLIC_IDENTITY");
    }
    (!o?.enumerable || !("value" in o) || o.value === void 0) && B("INVALID_PUBLIC_IDENTITY"), s[a] = o.value;
  }
  return s;
}
function je(e) {
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
  if (!Array.isArray(e) || Object.getPrototypeOf(e) !== Array.prototype || e.length !== At.length) return !1;
  let t;
  try {
    t = Reflect.ownKeys(e);
  } catch {
    return !1;
  }
  const n = ["0", "1", "2", "length"];
  if (t.length !== n.length || t.some((i, r) => i !== n[r])) return !1;
  for (let i = 0; i < At.length; i += 1) {
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
function he(e) {
  const t = Et(e, tn);
  (t.schemaVersion !== 1 || t.identityScheme !== mt || t.seedProfile !== gt || t.accountIndex !== 0 || t.accountLabel !== null || !/^[0-9a-f]{8}$/u.test(t.accountFingerprint) || !je(t.accountXpub) || !/^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(t.accountXpub) || !je(t.accountPeerId) || !/^16Uiu2H[1-9A-HJ-NP-Za-km-z]{33,57}$/u.test(t.accountPeerId) || !Qr(t.keys)) && B("INVALID_PUBLIC_IDENTITY");
  const n = At.map((i, r) => {
    const s = Et(t.keys[r], nn);
    return (s.bip32Fingerprint !== null || s.curve !== i.curve || s.derivation !== "slip10" || s.encoding !== "raw" || s.identityScheme !== mt || s.path !== i.path || s.purpose !== i.purpose || s.seedProfile !== gt || s.signatureProfile !== i.signatureProfile || !/^[0-9a-f]{64}$/u.test(s.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(s.keyId)) && B("INVALID_PUBLIC_IDENTITY"), De(s);
  });
  return De({ ...t, keys: n });
}
function Xr(e, { accountIndex: t = 0, profile: n } = {}) {
  const i = Object.hasOwn(wn, n) ? wn[n] : null;
  (!i || t !== 0) && B("INVALID_LEGACY_PROFILE");
  const r = Et(e, tn);
  (r.schemaVersion !== 1 || r.identityScheme !== i || r.seedProfile !== n || r.accountIndex !== t || r.accountLabel !== null || !/^[0-9a-f]{8}$/u.test(r.accountFingerprint) || !je(r.accountXpub) || !/^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(r.accountXpub) || !je(r.accountPeerId) || !/^16Uiu2H[1-9A-HJ-NP-Za-km-z]{33,57}$/u.test(r.accountPeerId) || !Yr(r.keys)) && B("INVALID_PUBLIC_IDENTITY");
  const s = Et(r.keys[0], nn);
  return (s.bip32Fingerprint !== null || s.curve !== "ed25519" || s.derivation !== "bip32-scalar-as-ed25519-seed" || s.encoding !== "raw" || s.identityScheme !== i || s.path !== `m/44'/0'/${t}'/0/0` || s.purpose !== "sdn-authentication" || s.seedProfile !== n || s.signatureProfile !== "ed25519-raw-32-v1" || !/^[0-9a-f]{64}$/u.test(s.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(s.keyId)) && B("INVALID_PUBLIC_IDENTITY"), De({ ...r, keys: [s] });
}
function vn(e) {
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
function Sn(e, t) {
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
function Dt(e, t) {
  try {
    e = he(e), t = he(t);
  } catch {
    return !1;
  }
  let n = !0;
  for (const i of tn.filter((r) => r !== "keys"))
    n = e[i] === t[i] && n;
  for (let i = 0; i < At.length; i += 1) {
    const r = e.keys[i], s = t.keys[i];
    for (const a of nn.filter((o) => o !== "publicKeyHex" && o !== "keyId"))
      n = r[a] === s[a] && n;
    n = Sn(r.publicKeyHex, s.publicKeyHex) && n, n = Sn(r.keyId.slice(7), s.keyId.slice(7)) && n;
  }
  return n;
}
function Pe(e, t, n, i) {
  const r = e.createElement("div");
  r.className = "wallet-account-row";
  const s = e.createElement("strong");
  s.textContent = `${n}: `;
  const a = e.createElement("span");
  a.textContent = i == null ? "" : String(i), r.append(s, a), t.append(r);
}
function ri(e) {
  (e?.identityScheme !== mt || e?.seedProfile !== gt || e?.accountIndex !== 0) && B("APPROVAL_UNAVAILABLE");
  const t = Zr(e);
  return (!t || t.identityScheme !== e.identityScheme || t.seedProfile !== e.seedProfile || t.signatureProfile !== "ed25519-over-sha256-jcs-v1" || t.curve !== "ed25519" || t.derivation !== "slip10" || t.path !== Ft || t.encoding !== "raw" || !/^[0-9a-f]{64}$/u.test(t.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(t.keyId)) && B("APPROVAL_UNAVAILABLE"), De({
    algorithm: "Ed25519",
    derivationPath: Ft,
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
  if (i.textContent = "Account", e.append(i), Pe(n, e, "Username / account", t?.accountLabel ?? "account 0"), Pe(n, e, "Account xpub", t?.accountXpub), Pe(n, e, "Peer ID", t?.accountPeerId), Pe(n, e, "Fingerprint", t?.accountFingerprint), t?.identityScheme !== mt || t?.seedProfile !== gt) {
    const s = n.createElement("p");
    return s.textContent = en, e.append(s), Object.freeze({ approvalAvailable: !1 });
  }
  const r = ri(t);
  return Pe(n, e, "Asset approval public key", r.publicKeyHex), Pe(n, e, "Asset approval key ID", r.keyId), Object.freeze({ approvalAvailable: !0, configuration: r });
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
  a.textContent = `Type ${ie} to confirm.`;
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
    confirmationKey: ie,
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
    const E = n.createElement("p");
    E.textContent = `Type ${c.key} to confirm deletion.`;
    const h = n.createElement("input");
    h.type = "text", h.autocomplete = "off", h.spellcheck = !1, h.dataset.walletQuarantineConfirmation = c.key;
    const A = n.createElement("button");
    A.type = "button", A.dataset.walletAction = "confirm-delete-quarantined-wallet", A.dataset.walletQuarantineKey = c.key, A.textContent = "Confirm delete";
    const m = n.createElement("button");
    m.type = "button", m.dataset.walletAction = "cancel-delete-quarantined-wallet", m.dataset.walletQuarantineKey = c.key, m.textContent = "Cancel", g.append(E, h, A, m), l.append(d, u, f, p, g), s.append(l), o.push(Object.freeze({
      cancel: m,
      confirm: A,
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
async function Rn(e, t, n, {
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
    (typeof p != "string" || typeof g != "string") && B("CREDENTIAL_CONFIRMATION_MISMATCH"), (!je(p) || !je(g)) && B("CREDENTIAL_CONFIRMATION_MISMATCH"), d = bn.encode(p), r(d), u = bn.encode(g), r(u);
  } finally {
    vn(c), vn(l);
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
    }), f?.handle || B("CREDENTIAL_CONFIRMATION_MISMATCH"), s(f.handle), i();
    let p;
    try {
      p = he(f.identity);
    } catch {
      B("CREDENTIAL_CONFIRMATION_MISMATCH");
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
      this.#f = i === null ? null : he(i);
    } catch {
      B("CREDENTIAL_CONFIRMATION_MISMATCH");
    }
    (typeof this.#t?.derivePasswordIdentity != "function" || typeof this.#t?.destroySdnIdentity != "function" || typeof n != "function") && B("CREDENTIAL_CONFIRMATION_MISMATCH");
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
    if (this.#h && B("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#d) return this.#d;
    this.#e += 1;
    const t = this.#v;
    let n, i;
    try {
      this.#R(), this.#n.size !== 0 && B("CREDENTIAL_CONFIRMATION_MISMATCH");
      const s = {
        assertCurrent: () => this.#p(t),
        ownBuffer: (a) => this.#r.add(a),
        ownHandle: (a) => this.#n.add(a),
        releaseBuffer: (a) => {
          In(a), this.#r.delete(a);
        }
      };
      return n = await Rn(this.#t, this.#m, 1, s), this.#S(n.handle) || B("CREDENTIAL_CONFIRMATION_MISMATCH"), n.handle = null, this.#p(t), i = await Rn(this.#t, this.#m, 2, s), this.#S(i.handle) || B("CREDENTIAL_CONFIRMATION_MISMATCH"), i.handle = null, this.#p(t), (!Dt(n.identity, i.identity) || this.#f !== null && (!Dt(n.identity, this.#f) || !Dt(i.identity, this.#f))) && B("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#n.size !== 0 && B("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#d = ri(i.identity), this.#d;
    } catch (r) {
      if (this.#d = null, r instanceof ii && r.code === "CREDENTIAL_CONFIRMATION_MISMATCH") throw r;
      B("CREDENTIAL_CONFIRMATION_MISMATCH");
    } finally {
      this.#o(), this.#R(), this.#e -= 1;
    }
  }
  #p(t) {
    (this.#h || this.#v !== t) && B("CREDENTIAL_CONFIRMATION_MISMATCH");
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
    for (const n of t) In(n);
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
  return t === "password-fast-v1-legacy" ? (typeof o?.deriveLegacyPasswordIdentity != "function" && B("INVALID_LEGACY_PROFILE"), c = await o.deriveLegacyPasswordIdentity({ accountIndex: r, ...i })) : t === "bip39-mnemonic-v1-legacy" ? (typeof o?.importLegacyMnemonicIdentity != "function" && B("INVALID_LEGACY_PROFILE"), c = await o.importLegacyMnemonicIdentity({ accountIndex: r, ...i })) : B("INVALID_LEGACY_PROFILE"), c?.handle !== null && c?.handle !== void 0 && a(c.handle), s(), Object.freeze({
    approval: null,
    handle: c.handle,
    identity: De(c.identity),
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
const wt = Uint8Array, si = ArrayBuffer, rn = Object.getPrototypeOf(wt.prototype), cs = Object.getOwnPropertyDescriptor(rn, "buffer").get, ls = Object.getOwnPropertyDescriptor(rn, "byteLength").get, us = Object.getOwnPropertyDescriptor(rn, "byteOffset").get, ds = Object.getOwnPropertyDescriptor(si.prototype, "byteLength").get;
class fs extends Error {
  constructor(t = "RNG_FAILURE") {
    super(t), this.name = "WalletRandomError", this.code = t;
  }
}
function Re() {
  throw new fs();
}
function ps(e) {
  if (!(e instanceof wt) || Object.getPrototypeOf(e) !== wt.prototype) return null;
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
  return Object.getPrototypeOf(t) !== si.prototype || i !== 0 || n !== r || n !== 12 && n !== 32 ? null : t;
}
function hs({ getRandomValues: e, observedWrite: t } = {}) {
  if (typeof e != "function")
    return () => Re();
  const n = /* @__PURE__ */ new WeakSet(), i = /* @__PURE__ */ new WeakSet();
  return function(s) {
    const a = ps(s);
    (!a || n.has(s) || i.has(a)) && Re(), n.add(s), i.add(a);
    let o;
    try {
      o = e(s);
    } catch {
      Re();
    }
    if (o !== s && Re(), t !== void 0) {
      typeof t != "function" && Re();
      let c = !1;
      try {
        c = t(s) === !0;
      } catch {
        Re();
      }
      c || Re();
    }
    return s;
  };
}
function Ue(e, t) {
  (typeof e != "function" || t !== 12 && t !== 32) && Re();
  const n = new wt(t);
  return e(n);
}
const Ze = new TextEncoder(), ys = new TextDecoder("utf-8", { fatal: !0 }), ms = Uint8Array.prototype.fill, gs = Uint8Array.prototype.slice, As = Uint8Array.prototype.subarray, sn = Object.getPrototypeOf(Uint8Array.prototype), Es = Object.getOwnPropertyDescriptor(sn, "buffer").get, oi = Object.getOwnPropertyDescriptor(sn, "byteLength").get, ws = Object.getOwnPropertyDescriptor(sn, "byteOffset").get, ai = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength").get, bs = ArrayBuffer.prototype.slice, Ln = "webauthn-prf-hkdf-sha256-aes256gcm-v2", Is = "sdn-bip32-slip10-purpose-v1", vs = "password-scrypt-v2", ci = "wallet.spacedatanetwork.org", Ss = "Space Data Network Wallet", li = 12e4;
class Rs extends Error {
  constructor(t) {
    super(t), this.name = "RememberedWalletError", this.code = t;
  }
}
function _(e) {
  throw new Rs(e);
}
function k(e) {
  if (e instanceof Uint8Array)
    try {
      ms.call(e, 0);
    } catch {
    }
}
function bt(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function ui(e, t) {
  if (!bt(e)) return !1;
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
function et(e, t) {
  if (!ui(e, t)) return null;
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
function dt(e, t = null) {
  if (!(e instanceof Uint8Array) || Object.getPrototypeOf(e) !== Uint8Array.prototype)
    return !1;
  let n, i, r, s;
  try {
    n = Reflect.apply(Es, e, []), i = Reflect.apply(oi, e, []), r = Reflect.apply(ws, e, []), s = Reflect.apply(ai, n, []);
  } catch {
    return !1;
  }
  return Object.getPrototypeOf(n) === ArrayBuffer.prototype && r === 0 && i === s && (t === null || i === t);
}
function di(e, t, n) {
  if (!(e instanceof ArrayBuffer) || Object.getPrototypeOf(e) !== ArrayBuffer.prototype)
    return !1;
  let i;
  try {
    i = Reflect.apply(ai, e, []);
  } catch {
    return !1;
  }
  return i >= t && i <= n;
}
function _e(e) {
  try {
    return Reflect.apply(oi, e, []);
  } catch {
    return -1;
  }
}
function Ge(e) {
  let t = "";
  const n = _e(e);
  n < 0 && _("INVALID_REMEMBERED_WALLET");
  for (let i = 0; i < n; i += 32768)
    t += String.fromCharCode(...Reflect.apply(
      As,
      e,
      [i, i + 32768]
    ));
  return btoa(t).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
}
function Ls(e) {
  let t = "";
  for (const n of e) t += n.toString(16).padStart(2, "0");
  return t;
}
function Be(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" || typeof e == "number" && Number.isFinite(e) ? JSON.stringify(e) : Array.isArray(e) ? `[${e.map(Be).join(",")}]` : (bt(e) || _("INVALID_REMEMBERED_WALLET"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${Be(e[t])}`).join(",")}}`);
}
function Os(e, t) {
  if (!(e instanceof Uint8Array) || !(t instanceof Uint8Array)) return !1;
  const n = _e(e), i = _e(t);
  if (n < 0 || i < 0) return !1;
  const r = Math.max(n, i);
  let s = n ^ i;
  for (let a = 0; a < r; a += 1)
    s |= (e[a] ?? 0) ^ (t[a] ?? 0);
  return s === 0;
}
function Ns(e, t) {
  return Be(e) === Be(t);
}
function fi(e) {
  (!e || typeof e != "object" && typeof e != "function") && _("WEBAUTHN_INVALID_RESPONSE");
  let t, n, i;
  try {
    t = e.type, n = e.rawId, i = e.getClientExtensionResults;
  } catch {
    _("WEBAUTHN_INVALID_RESPONSE");
  }
  return (t !== "public-key" || !di(n, 1, 1024) || typeof i != "function") && _("WEBAUTHN_INVALID_RESPONSE"), Object.freeze({
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
function Ts(e) {
  const t = et(e.readExtensionResults(), ["prf"]), n = t ? et(t.prf, ["enabled"]) : null;
  (!n || n.enabled !== !0) && _("WEBAUTHN_PRF_REQUIRED");
}
function Cs(e, t) {
  const n = fi(e), i = n.rawId;
  Os(i, t) || (k(i), _("WEBAUTHN_CREDENTIAL_MISMATCH")), k(i);
  const r = et(n.readExtensionResults(), ["prf"]), s = r ? et(r.prf, ["results"]) : null, o = (s ? et(s.results, ["first"]) : null)?.first;
  di(o, 32, 32) || _("WEBAUTHN_PRF_REQUIRED");
  const c = new Uint8Array(o), l = Reflect.apply(gs, c, []);
  return k(c), l;
}
function _s({ challenge: e, credentialId: t, prfInput: n, signal: i }) {
  return {
    publicKey: {
      allowCredentials: [{
        id: t,
        type: "public-key"
      }],
      challenge: e,
      extensions: { prf: { eval: { first: n } } },
      rpId: ci,
      timeout: li,
      userVerification: "required"
    },
    signal: i
  };
}
async function On({ assertCurrent: e, credentials: t, credentialId: n, fillRandom: i, prfInput: r, signal: s }) {
  const a = Ue(i, 32);
  let o;
  try {
    o = await t.get(_s({
      challenge: a,
      credentialId: n,
      prfInput: r,
      signal: s
    }));
  } catch (c) {
    throw e(), c;
  } finally {
    k(a);
  }
  return e(), Cs(o, n);
}
function xs(e) {
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
function Ds(e) {
  if (typeof e == "string")
    return xs(e) || _("INVALID_USERNAME"), Ze.encode(e).length > 256 && _("INVALID_USERNAME"), e;
  (!dt(e) || _e(e) > 256) && _("INVALID_USERNAME");
  try {
    return ys.decode(e);
  } catch {
    _("INVALID_USERNAME");
  }
}
function Wt(e) {
  const n = Ds(e).replace(/^ +/u, "").replace(/ +$/u, "").replace(/[A-Z]/gu, (r) => r.toLowerCase()), i = Ze.encode(n);
  return (i.length < 3 || i.length > 64 || !/^[a-z0-9][a-z0-9._-]*$/u.test(n)) && _("INVALID_USERNAME"), n;
}
function ks(e) {
  let t;
  try {
    t = Un(e?.module);
  } catch {
    _("WASM_UNAVAILABLE");
  }
  return (!ui(t, ["sdn", "sha256"]) || typeof t.sha256 != "function" || !t.sdn || typeof t.sdn != "object") && _("WASM_UNAVAILABLE"), t;
}
function Ps(e) {
  const t = ks(e), n = t.sdn, i = e?.storage, r = e?.credentials, s = hs(e?.rng), a = e?.createRequestController, o = e?.releaseRequestController, c = e?.ownHandle, l = e?.destroyHandle, d = e?.ownedHandlesClean, u = e?.now ?? (() => /* @__PURE__ */ new Date()), f = () => typeof r?.create == "function" && typeof r?.get == "function", p = () => ni(i), g = () => zr(i), E = (I) => qr(i, I), h = (I, b) => (Hr(i, I, b), g().some((x) => x.key === I) && _("STORAGE_WRITE_FAILED"), !0), A = () => p().active.status === "valid", m = ({ confirmation: I } = {}) => (A() || _("REMEMBER_UNAVAILABLE"), Gr(i, I), p().active.status !== "empty" && _("STORAGE_WRITE_FAILED"), !0), N = async (I) => {
    let b;
    try {
      b = await t.sha256(Ze.encode(I));
    } catch {
      _("WASM_FAILURE");
    }
    return dt(b, 32) || _("WASM_FAILURE"), Ls(b);
  };
  return Object.freeze({
    canForget: A,
    deleteQuarantine: h,
    exportQuarantine: E,
    forget: m,
    inspect: p,
    listQuarantine: g,
    restore: async ({ assertCurrent: I } = {}) => {
      (typeof I != "function" || !f() || typeof n.importRememberedIdentity != "function" || typeof c != "function" || typeof l != "function" || typeof a != "function" || typeof o != "function") && _("REMEMBER_UNAVAILABLE");
      const b = p();
      (!b.canRestore || !b.active.record) && _("STORAGE_QUARANTINED");
      const x = b.active.record;
      Wt(x.canonicalUsername) !== x.canonicalUsername && _("INVALID_REMEMBERED_WALLET"), await N(x.canonicalUsername) !== x.aad.usernameSha256 && _("INVALID_REMEMBERED_WALLET"), I();
      const z = me(x.credentialIdBase64url, {
        minimum: 1,
        maximum: 1024
      }), O = me(x.prfInputBase64url, { exact: 32 }), F = me(x.ciphertextBase64url, {
        minimum: 17,
        maximum: 1024
      }), D = me(x.hkdfSaltBase64url, { exact: 32 }), ee = me(x.nonceBase64url, { exact: 12 }), G = Ze.encode(x.canonicalUsername), M = Be(x.aad), R = typeof a == "function" ? a() : null;
      (!R?.signal || typeof R.abort != "function") && (k(z), k(O), k(F), k(D), k(ee), k(G), _("WEBAUTHN_UNAVAILABLE"));
      let W, q = null, J = !1;
      try {
        W = await On({
          assertCurrent: I,
          credentialId: z,
          credentials: r,
          fillRandom: s,
          prfInput: O,
          signal: R.signal
        }), I();
        let Q;
        const ce = W;
        try {
          Q = n.importRememberedIdentity({
            canonicalAad: M,
            canonicalUsernameUtf8: G,
            ciphertextAndTag: F,
            hkdfSalt: D,
            nonce: ee,
            prfOutput: ce
          });
        } finally {
          k(ce), W = null;
        }
        bt(Q) || _("WASM_FAILURE"), q = Q.handle, (q == null || typeof c != "function") && _("WASM_FAILURE"), c(q);
        const te = he(Q.identity);
        return I(), J = !0, Object.freeze({ handle: q, identity: te });
      } finally {
        !J && q !== null && typeof l == "function" && (l(q) || _("DESTRUCTION_FAILED")), k(W), k(z), k(O), k(F), k(D), k(ee), k(G), typeof o == "function" && o(R);
      }
    },
    setup: async ({ assertCurrent: I, canonicalUsername: b, handle: x, identity: H, passwordUtf8: z }) => {
      try {
        (typeof I != "function" || x === null || x === void 0 || !dt(z) || _e(z) === 0 || _e(z) > 256 || Wt(b) !== b || !f() || typeof n.sealRememberedIdentity != "function" || typeof n.importRememberedIdentity != "function" || typeof c != "function" || typeof l != "function" || typeof d != "function" || typeof a != "function" || typeof o != "function") && (k(z), _("REMEMBER_UNAVAILABLE"));
        const O = he(H);
        p().canSetup || (k(z), _("STORAGE_QUARANTINED"));
        const D = typeof a == "function" ? a() : null;
        (!D?.signal || typeof D.abort != "function") && (k(z), _("WEBAUTHN_UNAVAILABLE"));
        let ee, G, M, R, W, q, J, Q, ce, te = null, Ie = null;
        try {
          ee = Ue(s, 32), G = Ue(s, 32), M = Ue(s, 32), R = Ue(s, 32), W = Ue(s, 12), I();
          let ve;
          try {
            ve = await r.create({
              publicKey: {
                attestation: "none",
                authenticatorSelection: {
                  residentKey: "preferred",
                  userVerification: "required"
                },
                challenge: ee,
                extensions: { prf: {} },
                pubKeyCredParams: [
                  { alg: -7, type: "public-key" },
                  { alg: -257, type: "public-key" }
                ],
                rp: { id: ci, name: Ss },
                timeout: li,
                user: {
                  displayName: b,
                  id: G,
                  name: b
                }
              },
              signal: D.signal
            });
          } catch (Se) {
            throw I(), Se;
          }
          I();
          const le = fi(ve);
          q = le.rawId, Ts(le), J = await On({
            assertCurrent: I,
            credentialId: q,
            credentials: r,
            fillRandom: s,
            prfInput: M,
            signal: D.signal
          }), Q = J.slice(), ce = J.slice(), k(J), J = null;
          const $e = await N(b);
          I();
          const ke = Ge(q), Fe = Object.freeze({
            credentialIdBase64url: ke,
            identityScheme: Is,
            schemaVersion: 2,
            seedProfile: vs,
            storageProfile: Ln,
            usernameSha256: $e
          }), ge = Be(Fe);
          let Ae;
          try {
            Ae = n.sealRememberedIdentity(x, {
              canonicalAad: ge,
              hkdfSalt: R,
              nonce: W,
              passwordUtf8: z,
              prfOutput: Q
            });
          } finally {
            k(z), k(Q);
          }
          z = null, Q = null;
          const We = _e(Ae);
          (!dt(Ae) || We < 17 || We > 1024) && _("WASM_FAILURE");
          let ze;
          try {
            const Se = u();
            ze = Se instanceof Date ? Se.toISOString() : new Date(Se).toISOString();
          } catch {
            _("CLOCK_FAILURE");
          }
          const st = Object.freeze({
            aad: Fe,
            canonicalUsername: b,
            ciphertextBase64url: Ge(Ae),
            createdAt: ze,
            credentialIdBase64url: ke,
            hkdfSaltBase64url: Ge(R),
            nonceBase64url: Ge(W),
            prfInputBase64url: Ge(M),
            schemaVersion: 2,
            storageProfile: Ln
          });
          I(), Ie = $r(i, st);
          let Ee;
          const qe = ce;
          try {
            Ee = n.importRememberedIdentity({
              canonicalAad: ge,
              canonicalUsernameUtf8: Ze.encode(b),
              ciphertextAndTag: Ae.slice(),
              hkdfSalt: R.slice(),
              nonce: W.slice(),
              prfOutput: qe
            });
          } finally {
            k(qe), ce = null;
          }
          bt(Ee) || _("WASM_FAILURE"), te = Ee.handle, (te == null || typeof c != "function") && _("WASM_FAILURE"), c(te);
          const ot = he(Ee.identity);
          return Ns(O, ot) || _("IDENTITY_MISMATCH"), I(), (typeof l != "function" || !l(te)) && _("DESTRUCTION_FAILED"), te = null, (typeof d != "function" || !d(x)) && _("DESTRUCTION_FAILED"), I(), Fr(i, Ie), Ie = null, Object.freeze({ remembered: !0 });
        } finally {
          te !== null && typeof l == "function" && l(te), k(z), k(ee), k(G), k(J), k(Q), k(ce), k(R), k(W), k(q), typeof o == "function" && o(D);
        }
      } finally {
        k(z);
      }
    },
    supported: f
  });
}
const tt = /^[0-9a-f]{64}$/u, Us = /^[A-Za-z0-9_-]{43}$/u, js = Object.freeze([
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
]), Bs = Object.freeze(["redirectUri", "schemaVersion", "transactionId"]), Vs = 64 * 1024, Ms = 4 * 1024, $s = new TextEncoder(), Fs = new TextDecoder("utf-8", { fatal: !0 });
class It extends Error {
  constructor() {
    super("RELAY_FAILURE"), this.name = "WalletRelayError", this.code = "RELAY_FAILURE";
  }
}
function C() {
  throw new It();
}
function pi(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function hi(e, t) {
  pi(e) || C();
  let n;
  try {
    n = Reflect.ownKeys(e);
  } catch {
    C();
  }
  n.some((s) => typeof s != "string") && C();
  const i = [...n].sort(), r = [...t].sort();
  (i.length !== r.length || i.some((s, a) => s !== r[a])) && C();
  for (const s of i) {
    let a;
    try {
      a = Object.getOwnPropertyDescriptor(e, s);
    } catch {
      C();
    }
    (!a?.enumerable || !("value" in a) || a.value === void 0) && C();
  }
  return e;
}
function zt(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(zt));
  if (!pi(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = zt(e[n]);
  return Object.freeze(t);
}
function Ws(e, t) {
  (typeof e != "string" || $s.encode(e).byteLength > t || e.charCodeAt(0) === 65279) && C();
  let n = 0, i = 0;
  const r = () => {
    for (; n < e.length && /[\u0009\u000a\u000d\u0020]/u.test(e[n]); ) n += 1;
  }, s = () => {
    e[n] !== '"' && C();
    const c = n;
    for (n += 1; n < e.length; ) {
      const l = e.charCodeAt(n);
      if (l === 34) {
        n += 1;
        let d;
        try {
          d = JSON.parse(e.slice(c, n));
        } catch {
          C();
        }
        for (let u = 0; u < d.length; u += 1) {
          const f = d.charCodeAt(u);
          if (f >= 55296 && f <= 56319) {
            const p = d.charCodeAt(u + 1);
            p >= 56320 && p <= 57343 || C(), u += 1;
          } else f >= 56320 && f <= 57343 && C();
        }
        return d;
      }
      l < 32 && C(), l === 92 && (n += 1, n >= e.length && C()), n += 1;
    }
    C();
  }, a = (c = 0) => {
    (c > 32 || ++i > 4096) && C(), r();
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
        if (p.has(g) && C(), p.add(g), r(), e[n] !== ":" && C(), n += 1, f[g] = a(c + 1), r(), e[n] === "}")
          return n += 1, f;
        e[n] !== "," && C(), n += 1;
      }
      C();
    }
    if (l === "[") {
      n += 1;
      const f = [];
      if (r(), e[n] === "]")
        return n += 1, f;
      for (; n < e.length; ) {
        if (f.push(a(c + 1)), r(), e[n] === "]")
          return n += 1, f;
        e[n] !== "," && C(), n += 1;
      }
      C();
    }
    for (const [f, p] of [["true", !0], ["false", !1], ["null", null]])
      if (e.startsWith(f, n))
        return n += f.length, p;
    const d = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(e.slice(n));
    d || C(), n += d[0].length;
    const u = Number(d[0]);
    return Number.isFinite(u) || C(), u;
  };
  r();
  const o = a();
  return r(), n !== e.length && C(), o;
}
function Nn(e) {
  try {
    Promise.resolve(e).catch(() => {
    });
  } catch {
  }
}
function yi(e, t = null) {
  try {
    const n = t?.cancel;
    typeof n == "function" && Nn(Reflect.apply(n, t, []));
  } catch {
  }
  if (!t)
    try {
      const n = e?.body, i = n?.cancel;
      typeof i == "function" && Nn(Reflect.apply(i, n, []));
    } catch {
    }
}
function mi(e, t, n = null) {
  let i;
  try {
    i = Promise.resolve(e);
  } catch {
    return Promise.reject(new It());
  }
  return t ? new Promise((r, s) => {
    let a = !1;
    const o = () => {
      try {
        t.removeEventListener?.("abort", l);
      } catch {
      }
    }, c = (d, u) => a ? !1 : (a = !0, o(), d(u), !0), l = () => c(s, new It());
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
async function zs(e, t, n, i) {
  let r = null;
  try {
    (!e || e.status !== t || e.redirected === !0 || e.headers?.get?.("cache-control")?.trim().toLowerCase() !== "no-store" || e.headers?.get?.("content-type")?.trim().toLowerCase() !== "application/json; charset=utf-8" || typeof e.body?.getReader != "function") && C(), r = e.body.getReader(), (!r || typeof r.read != "function" || i?.aborted === !0) && C();
    const s = [];
    let a = 0;
    for (; ; ) {
      i?.aborted === !0 && C();
      const { done: l, value: d } = await mi(r.read(), i);
      if (i?.aborted === !0 && C(), l) break;
      d instanceof Uint8Array || C(), a += d.byteLength, a > n && C(), s.push(d);
    }
    const o = new Uint8Array(a);
    let c = 0;
    for (const l of s)
      o.set(l, c), c += l.byteLength;
    return Ws(Fs.decode(o), n);
  } catch (s) {
    if (yi(e, r), s instanceof It) throw s;
    C();
  } finally {
    try {
      r?.releaseLock?.();
    } catch {
    }
  }
}
function Tn(e, t) {
  return hi(e, js), (e.schemaVersion !== 1 || e.transactionId !== t || !tt.test(e.transactionId) || !tt.test(e.state) || !tt.test(e.requestSha256) || !Us.test(e.resultToken) || typeof e.callbackUri != "string" || typeof e.clientDisplayName != "string" || typeof e.clientId != "string" || typeof e.expiresAt != "string" || typeof e.operation != "string" || typeof e.registryVersion != "string" || typeof e.requestOrigin != "string") && C(), zt(e);
}
function qt(e, t) {
  hi(t, Bs), (t.schemaVersion !== 1 || t.transactionId !== e.transactionId || typeof t.redirectUri != "string") && C();
  const n = `${e.callbackUri}#code=`, i = `&state=${e.state}`;
  (!t.redirectUri.startsWith(n) || !t.redirectUri.endsWith(i)) && C();
  const r = t.redirectUri.slice(n.length, t.redirectUri.length - i.length);
  return (!tt.test(r) || t.redirectUri !== `${n}${r}${i}`) && C(), Object.freeze({
    redirectUri: t.redirectUri,
    schemaVersion: 1,
    transactionId: t.transactionId
  });
}
function Cn(e, t, n = void 0) {
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
function qs({ fetch: e, location: t }) {
  (typeof e != "function" || typeof t?.replace != "function") && C();
  const n = /* @__PURE__ */ new Set(), i = async (r, s, a, o) => {
    let c;
    try {
      c = await mi(e(r, s), s.signal, (l) => yi(l));
    } catch {
      C();
    }
    return zs(c, a, o, s.signal);
  };
  return Object.freeze({
    async fetchTransaction(r, { signal: s } = {}) {
      (typeof r != "string" || !tt.test(r)) && C();
      const a = await i(
        `/relay/v1/transactions/${r}`,
        Cn("GET", s),
        200,
        Vs
      );
      return Tn(a, r);
    },
    async publishResult(r, s, { signal: a } = {}) {
      const o = Tn(r, r?.transactionId), c = await i(
        `/relay/v1/transactions/${o.transactionId}/result`,
        Cn("POST", a, {
          result: s,
          resultToken: o.resultToken,
          schemaVersion: 1,
          transactionId: o.transactionId
        }),
        201,
        Ms
      ), l = qt(o, c);
      return n.clear(), n.add(l.redirectUri), l;
    },
    navigate(r) {
      (typeof r != "string" || !n.delete(r)) && C();
      try {
        Reflect.apply(t.replace, t, [r]);
      } catch {
        C();
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
const gi = Uint8Array, Hs = gi.prototype.fill, Ke = new TextEncoder();
class j extends Error {
  constructor(t) {
    super(t), this.name = "WalletOriginError", this.code = t;
  }
}
function w(e) {
  throw new j(e);
}
function Je(e) {
  throw e instanceof Ne ? new j(e.code) : e;
}
function fe(e) {
  if (e instanceof gi)
    try {
      Hs.call(e, 0);
    } catch {
    }
}
function Qe(e) {
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
function _n(e) {
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
function Ye(e, t) {
  const n = /* @__PURE__ */ new Set();
  for (const i of t) {
    let r = null;
    try {
      r = i?.form ?? i?.closest?.("form") ?? null;
    } catch {
      r = null;
    }
    r && n.add(r), _n(i);
  }
  for (const i of n) {
    let r = [];
    try {
      r = i.querySelectorAll?.("input, textarea") ?? [];
    } catch {
      r = [];
    }
    for (const s of r) _n(s);
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
function Gs(e) {
  let t;
  try {
    t = Un(e);
  } catch {
    w("WASM_UNAVAILABLE");
  }
  const n = t?.sdn, i = t?.sha256;
  return (!n || typeof n.derivePasswordIdentity != "function" || typeof n.destroySdnIdentity != "function" || typeof i != "function") && w("WASM_UNAVAILABLE"), Object.freeze({ capabilities: n, sha256: i });
}
function ct(e) {
  const t = e?.AbortController ?? globalThis.AbortController;
  return typeof t == "function" ? new t() : null;
}
class Ks {
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
  #k = /* @__PURE__ */ new WeakMap();
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
    storage: o,
    wasm: c,
    window: l
  }) {
    const d = Gs(c);
    this.#m = d.capabilities, this.#x = d.sha256, this.#_ = r, this.#c = s, this.#n = n, this.#s = l, this.#y = Ps({
      createRequestController: () => {
        const u = ct(this.#s);
        return u && (this.#a.add(u), this.#O.add(u)), u;
      },
      credentials: t ?? l?.navigator?.credentials ?? globalThis.navigator?.credentials,
      destroyHandle: (u) => this.#E(u),
      module: c,
      now: i,
      ownHandle: (u) => this.#o.add(u),
      ownedHandlesClean: (u) => this.#o.size === 1 && this.#o.has(u) && this.#t === u,
      releaseRequestController: (u) => {
        this.#O.delete(u), this.#a.delete(u);
      },
      rng: a ?? {
        getRandomValues: l?.crypto?.getRandomValues?.bind(l.crypto) ?? globalThis.crypto?.getRandomValues?.bind(globalThis.crypto)
      },
      storage: o ?? l?.localStorage ?? globalThis.localStorage
    }), this.#F();
  }
  get generation() {
    return this.#r;
  }
  #P() {
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
    for (const n of t) fe(n);
  }
  #N(t = []) {
    if (this.#f.size === 0) return;
    const n = new Set(t), i = [];
    for (const r of this.#f)
      n.has(r) || i.push(r);
    this.#f = new Set(
      [...this.#f].filter((r) => n.has(r))
    ), Ye(this.#n, i);
  }
  #j(t = []) {
    (this.#e || this.#i) && w("STALE_CONTROLLER");
    const n = this.#P();
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
    this.#P(), this.#i = !0, this.#d = !1, this.#u = c, this.#o.add(t), this.#t = null, this.#p = null, r || (this.#A = null);
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
      return Promise.reject(new j("STALE_CONTROLLER"));
    this.#g = null, this.#A = null;
    let i, r;
    const s = new Promise((a, o) => {
      i = a, r = o;
    });
    return this.#C = s, (async () => {
      const a = ct(this.#s);
      a && this.#a.add(a);
      try {
        this.#I(n.epoch, n.permit), ne({ document: this.#n, window: this.#s }), typeof this.#c?.publishResult != "function" && w("RELAY_UNAVAILABLE");
        const o = t === "connected" ? n.connectedResult : Tr();
        let c;
        try {
          c = await this.#c.publishResult(n.transaction, o, {
            signal: a?.signal
          });
        } catch (l) {
          throw this.#I(n.epoch, n.permit), l;
        }
        if (this.#I(n.epoch, n.permit), ne({ document: this.#n, window: this.#s }), typeof this.#c?.navigate == "function") {
          const l = qt(n.transaction, c);
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
      return he(t);
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
      throw typeof t?.code == "string" ? new j(t.code) : t;
    }
  }
  exportQuarantinedWalletRecord(t) {
    (this.#e || this.#i && !this.#g) && w("STALE_CONTROLLER");
    try {
      return this.#y.exportQuarantine(t);
    } catch (n) {
      throw typeof n?.code == "string" ? new j(n.code) : n;
    }
  }
  deleteQuarantinedWalletRecord(t, n) {
    (this.#e || this.#i && !this.#g) && w("STALE_CONTROLLER");
    try {
      return this.#y.deleteQuarantine(t, n);
    } catch (i) {
      throw typeof i?.code == "string" ? new j(i.code) : i;
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
      throw typeof n?.code == "string" ? new j(n.code) : n;
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
      ne({ document: this.#n, window: this.#s });
    } catch (h) {
      Ye(
        this.#n,
        [s, n, i].filter(Boolean)
      ), Je(h);
    }
    let a;
    try {
      a = this.#j([s, n]);
    } catch (h) {
      throw Ye(
        this.#n,
        [s, n, i].filter(Boolean)
      ), h;
    }
    this.registerCredentialControls({ passwordControl: n, usernameControl: s });
    let o, c, l, d, u, f, p = null, g = !1;
    try {
      try {
        ne({ document: this.#n, window: this.#s });
      } catch (A) {
        Je(A);
      }
      this.#T(a);
      const h = s?.value;
      p = n?.value, Qe(h) || w("INVALID_USERNAME"), Qe(p) || w("INVALID_PASSWORD");
      try {
        o = Wt(h);
      } catch {
        w("INVALID_USERNAME");
      }
      c = Ke.encode(h), f = Ke.encode(p), g = i?.checked === !0 && i?.disabled !== !0 && this.#y.supported(), g ? ((f.length === 0 || f.length > 256) && w("INVALID_PASSWORD"), l = f.slice(), d = f.slice(), u = d, fe(f), f = null) : (l = f, f = null), this.#l.add(c), this.#l.add(l), d && this.#l.add(d);
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
      fe(f), this.#N();
    }
    let E;
    try {
      E = await this.#m.derivePasswordIdentity({
        accountIndex: t,
        passwordUtf8: l,
        usernameUtf8: c
      }), (!E || typeof E != "object") && w("WASM_FAILURE");
      const h = E.handle;
      h == null && w("WASM_FAILURE"), this.#o.add(h);
      let A;
      try {
        A = he(E.identity);
      } catch {
        const m = this.#E(h);
        E = null, m || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("WASM_FAILURE");
      }
      if ((this.#e || this.#i || this.#r !== a) && (this.#E(h) || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("STALE_CONTROLLER")), this.#t = h, this.#p = A, g)
        try {
          await this.#y.setup({
            assertCurrent: () => this.#b(h, a),
            canonicalUsername: o,
            handle: h,
            identity: A,
            passwordUtf8: d
          }), this.#b(h, a), this.#l.delete(u), d = null;
          try {
            r && (r.textContent = "Wallet remembered on this device.");
          } catch {
          }
        } catch {
          fe(d), this.#l.delete(u), d = null, (this.#e || this.#i || this.#r !== a || this.#t !== h) && w("STALE_CONTROLLER"), this.#o.size === 1 && this.#o.has(h) && this.#t === h || (this.revokeNow("remembered-wallet-cleanup-failed"), w("DESTRUCTION_FAILED"));
          try {
            r && (r.textContent = "Wallet was not remembered.");
          } catch {
          }
        }
      return this.#b(h, a), A;
    } catch (h) {
      throw h instanceof j && h.code === "DESTRUCTION_FAILED" || (this.#e || this.#i || this.#r !== a) && w("STALE_CONTROLLER"), h;
    } finally {
      fe(c), fe(l), fe(d), fe(u), this.#l.delete(c), this.#l.delete(l), this.#l.delete(u);
    }
  }
  async unlockRemembered({ accountIndex: t = 0 } = {}) {
    (this.#e || this.#i) && w("STALE_CONTROLLER"), t !== 0 && w("INVALID_ACCOUNT"), this.canRestoreRememberedWallet() || w("REMEMBER_UNAVAILABLE");
    try {
      ne({ document: this.#n, window: this.#s });
    } catch (i) {
      Je(i);
    }
    const n = this.#j();
    try {
      const i = await this.#y.restore({
        assertCurrent: () => this.#T(n)
      }), r = i?.handle;
      (r == null || !this.#o.has(r)) && w("WASM_FAILURE");
      let s;
      try {
        s = he(i.identity);
      } catch {
        w("WASM_FAILURE");
      }
      return (this.#e || this.#i || this.#r !== n) && (this.#E(r) || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("STALE_CONTROLLER")), this.#t = r, this.#p = s, s;
    } catch (i) {
      throw i instanceof j && i.code === "DESTRUCTION_FAILED" || ((this.#e || this.#i || this.#r !== n) && w("STALE_CONTROLLER"), this.#o.size > 0 && (this.#w(), this.#o.size > 0 && (this.revokeNow("remembered-wallet-cleanup-failed"), w("DESTRUCTION_FAILED"))), i instanceof j) ? i : typeof i?.code == "string" ? new j(i.code) : i;
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
      ne({ document: this.#n, window: this.#s });
    } catch (E) {
      Ye(this.#n, l), Je(E);
    }
    let d;
    try {
      d = this.#j(l);
    } catch (E) {
      throw Ye(this.#n, l), E;
    }
    o ? this.registerCredentialControls({ passwordControl: r, usernameControl: a }) : this.#f.add(n);
    let u, f, p;
    try {
      try {
        ne({ document: this.#n, window: this.#s });
      } catch (E) {
        Je(E);
      }
      if (this.#T(d), o) {
        const E = a?.value, h = r?.value;
        Qe(E) || w("INVALID_USERNAME"), Qe(h) || w("INVALID_PASSWORD"), u = Ke.encode(E), f = Ke.encode(h), this.#l.add(u), this.#l.add(f);
      } else {
        const E = n?.value;
        Qe(E) || w("INVALID_MNEMONIC"), p = Ke.encode(E), this.#l.add(p);
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
      const E = g.handle;
      E == null && w("WASM_FAILURE"), this.#o.add(E);
      let h;
      try {
        h = Xr(g.identity, { accountIndex: t, profile: s });
      } catch {
        const A = this.#E(E);
        g = null, A || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("WASM_FAILURE");
      }
      return (this.#e || this.#i || this.#r !== d) && (this.#E(E) || (this.revokeNow("native-destruction-failed"), w("DESTRUCTION_FAILED")), w("STALE_CONTROLLER")), this.#t = E, this.#p = h, h;
    } catch (E) {
      throw E instanceof j && E.code === "DESTRUCTION_FAILED" || (this.#e || this.#i || this.#r !== d) && w("STALE_CONTROLLER"), E;
    } finally {
      fe(u), fe(f), fe(p), this.#l.delete(u), this.#l.delete(f), this.#l.delete(p);
    }
  }
  async prepare(t) {
    (this.#e || this.#i) && w("STALE_CONTROLLER"), this.#d && w("TRANSACTION_IN_PROGRESS"), this.#d = !0;
    const n = this.#r, i = typeof t == "string" ? t : t?.transactionId ?? null, r = ct(this.#s);
    r && this.#a.add(r);
    try {
      const s = typeof this.#c?.fetchTransaction == "function" ? await this.#c.fetchTransaction(t, { signal: r?.signal }) : t;
      this.#T(n);
      const a = await xt(s, {
        expectedTransactionId: i,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      });
      return this.#T(n), ne({ document: this.#n, window: this.#s }), this.#D.add(a), r && this.#k.set(a, r), a;
    } catch (s) {
      const a = this.#e || this.#i || this.#r !== n;
      throw this.#d = !1, r && this.#a.delete(r), a && w("STALE_CONTROLLER"), s instanceof j ? s : s instanceof Ne ? new j(s.code) : s;
    }
  }
  async executePrepared(t) {
    (!t || typeof t != "object" || !this.#D.has(t)) && w("INVALID_TRANSACTION"), this.#D.delete(t);
    const n = this.#k.get(t) ?? null;
    this.#k.delete(t), (this.#e || this.#i || this.#t === null) && (n && this.#a.delete(n), this.#d = !1, w("STALE_CONTROLLER"));
    const i = this.#t, r = this.#p, s = this.#r;
    let a = !1;
    try {
      this.#b(i, s);
      let o = await xt(t.transaction, {
        expectedTransactionId: t.transaction.transactionId,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      });
      this.#b(i, s), ne({ document: this.#n, window: this.#s }), this.#h = Or({
        binding: o.binding,
        document: this.#n,
        identity: r,
        request: o.request,
        transaction: o.transaction
      }), await this.#h.promise, this.#b(i, s), ne({ document: this.#n, window: this.#s }), o = await xt(t.transaction, {
        expectedTransactionId: t.transaction.transactionId,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      }), this.#b(i, s), ne({ document: this.#n, window: this.#s });
      const c = await Nr({
        assertCurrent: () => this.#b(i, s),
        binding: o.binding,
        handle: i,
        identity: r,
        transaction: o.transaction,
        wasm: this.#m
      });
      this.#b(i, s), ne({ document: this.#n, window: this.#s });
      const l = o.transaction.operation === "sdn.wallet.account.v1";
      l && (this.#A = he(r));
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
        const p = ct(this.#s);
        p && this.#a.add(p);
        let g;
        try {
          try {
            g = await this.#c.publishResult(o.transaction, c, {
              signal: p?.signal
            });
          } catch (E) {
            throw this.#I(u, f), E;
          }
        } finally {
          p && this.#a.delete(p);
        }
        if (this.#I(u, f), ne({ document: this.#n, window: this.#s }), typeof this.#c?.navigate == "function") {
          const E = qt(o.transaction, g);
          this.#I(u, f), this.#u = null, this.#c.navigate(E.redirectUri);
        } else
          this.#u = null;
        return g;
      } finally {
        this.#u === f && (this.#u = null);
      }
    } catch (o) {
      throw !a && (this.#r !== s || this.#t !== i) && w("STALE_CONTROLLER"), o instanceof j ? o : o instanceof Ne ? new j(o.code) : o;
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
    !this.#i && (this.#i = !0, this.#P()), this.#d = !1, this.#A = null, this.#t !== null && this.#t !== void 0 && this.#o.add(this.#t), this.#t = null, this.#p = null;
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
const Js = [
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
], Qs = "e1ce6fe903c9700484a8a87d96581c8cad97063dabf63030b4518a31a3bdaa93", Ys = 1, Ai = {
  clients: Js,
  registryReleaseSha256: Qs,
  schemaVersion: Ys
}, Xs = new TextEncoder(), Zs = /^[0-9a-f]{64}$/u, eo = /^[a-z0-9]+(?:-[a-z0-9]+)*$/u, to = Object.freeze({
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
}), ye = Object.freeze([
  "sdn.wallet.account.v1",
  "sdn.wallet.connect.v1"
]), no = Object.freeze([
  "sdn.auth.jcs-envelope.v2",
  "sdn.auth.raw-challenge.v1",
  ...ye
]), io = Object.freeze([
  "sdn.asset-review.authority-activation.v1",
  "sdn.asset-review.decision.v1",
  ...ye
]), xn = Object.freeze([
  Object.freeze(["sdn-landing-web-v1", "Space Data Network", "https://spacedatanetwork.org", "https://spacedatanetwork.org/wallet-callback.html", ye]),
  Object.freeze(["sdn-standards-web-v1", "Space Data Standards", "https://spacedatastandards.org", "https://spacedatastandards.org/wallet-callback.html", ye]),
  Object.freeze(["sdn-flatbuffers-pages-v1", "FlatBuffers Documentation", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/flatbuffers/wallet-callback.html", ye]),
  Object.freeze(["sdn-flatsql-pages-v1", "FlatSQL Documentation", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/flatsql/wallet-callback.html", ye]),
  Object.freeze(["sdn-module-sdk-pages-v1", "Space Data Module SDK", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/space-data-module-sdk/wallet-callback.html", ye]),
  Object.freeze(["spaceaware-web-v1", "SpaceAware", "https://spaceaware.io", "https://spaceaware.io/wallet/callback", ye]),
  Object.freeze(["sdn-node-console-v1", "SDN Node Console", "https://sdn.spaceaware.io", "https://sdn.spaceaware.io/wallet/callback", no]),
  Object.freeze(["orbpro-pages-v1", "OrbPro", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/OrbPro/wallet-callback.html", ye]),
  Object.freeze(["sdn-asset-models-pages-v1", "SDN Asset Models", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/asset-models/wallet-callback.html", ye]),
  Object.freeze(["sdn-asset-review-v1", "SDN Asset Review", "https://review.spacedatanetwork.org", "https://review.spacedatanetwork.org/wallet/callback", io])
]), ro = Object.freeze([
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
function U(e) {
  throw new TypeError(e);
}
function on(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function ft(e, t, n) {
  on(e) || U(`${n} must be an object`);
  const i = Reflect.ownKeys(e);
  i.some((a) => typeof a != "string") && U(`${n} has missing or unknown fields`);
  const r = i.sort(), s = [...t].sort();
  (r.length !== s.length || r.some((a, o) => a !== s[o])) && U(`${n} has missing or unknown fields`);
  for (const a of r) {
    const o = Object.getOwnPropertyDescriptor(e, a);
    (!o || !o.enumerable || !("value" in o) || o.value === void 0) && U(`${n} has an invalid field`);
  }
  return e;
}
function pe(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" ? JSON.stringify(e) : typeof e == "number" ? (Number.isFinite(e) || U("registry contains a non-finite number"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(pe).join(",")}]` : (on(e) || U("registry contains an unsupported value"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${pe(e[t])}`).join(",")}}`);
}
async function so(e) {
  const t = globalThis.crypto?.subtle;
  t || U("Web Crypto SHA-256 is unavailable");
  const n = new Uint8Array(await t.digest("SHA-256", Xs.encode(e)));
  return Array.from(n, (i) => i.toString(16).padStart(2, "0")).join("");
}
function vt(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(vt));
  if (!on(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = vt(e[n]);
  return Object.freeze(t);
}
function oo(e, t) {
  (typeof e != "string" || !e.startsWith("https://") || e.includes("*")) && U(`${t} must be exact HTTPS`);
  let n;
  try {
    n = new URL(e);
  } catch {
    U(`${t} must be a valid URL`);
  }
  (n.protocol !== "https:" || n.username || n.password || n.port || n.origin !== e || n.pathname !== "/" || n.search || n.hash) && U(`${t} must be an exact HTTPS origin`);
}
function ao(e, t) {
  (typeof e != "string" || !e.startsWith("https://") || e.includes("*")) && U("callbackUri must be exact HTTPS");
  let n;
  try {
    n = new URL(e);
  } catch {
    U("callbackUri must be a valid URL");
  }
  (n.protocol !== "https:" || n.username || n.password || n.port || n.origin !== t || n.search || n.hash || n.href !== e) && U("callbackUri must be exact and same-origin");
}
function co(e) {
  ft(e, ["clients", "registryReleaseSha256", "schemaVersion"], "registry"), e.schemaVersion !== 1 && U("registry schemaVersion must be 1"), (typeof e.registryReleaseSha256 != "string" || !Zs.test(e.registryReleaseSha256)) && U("registryReleaseSha256 must be lowercase SHA-256"), (!Array.isArray(e.clients) || e.clients.length !== xn.length) && U("registry must contain exactly the reviewed clients");
  const t = /* @__PURE__ */ new Set(), n = /* @__PURE__ */ new Set();
  e.clients.forEach((i, r) => {
    ft(i, [
      "allowedOperations",
      "audiences",
      "callbackUri",
      "clientDisplayName",
      "clientId",
      "operationBindings",
      "requestOrigin"
    ], `client ${r}`);
    const [s, a, o, c, l] = xn[r];
    (i.clientId !== s || i.clientDisplayName !== a || i.requestOrigin !== o || i.callbackUri !== c) && U(`client ${r} differs from the reviewed row`), (!eo.test(i.clientId) || t.has(i.clientId)) && U("clientId must be unique and canonical"), t.add(i.clientId), (typeof i.clientDisplayName != "string" || i.clientDisplayName.length < 1 || i.clientDisplayName.length > 80) && U("clientDisplayName is invalid"), oo(i.requestOrigin, "requestOrigin"), ao(i.callbackUri, i.requestOrigin), (!Array.isArray(i.allowedOperations) || pe(i.allowedOperations) !== pe(l)) && U(`${i.clientId} allowedOperations differ from the reviewed allowlist`), (!Array.isArray(i.operationBindings) || i.operationBindings.length !== l.length) && U(`${i.clientId} operationBindings differ from the reviewed allowlist`), i.operationBindings.forEach((u, f) => {
      ft(u, [
        "audience",
        "maxLifetimeSeconds",
        "operation",
        "registryRow",
        "serviceActivationState",
        "serviceInstance"
      ], `${i.clientId} operation ${f}`);
      const p = l[f];
      u.operation !== p && U(`${i.clientId} operation allowlist order or value changed`);
      const g = to[u.operation];
      g || U("unknown registry operation"), (u.maxLifetimeSeconds !== 300 || u.audience !== g.audience || u.registryRow !== g.registryRow || u.serviceActivationState !== g.serviceActivationState || u.serviceInstance !== g.serviceInstance) && U(`${i.clientId} operation policy differs from the reviewed binding`);
      const E = `${i.clientId}\0${i.requestOrigin}\0${u.operation}`;
      n.has(E) && U("duplicate registry binding"), n.add(E);
    });
    const d = [...new Set(i.operationBindings.map(({ audience: u }) => u).filter((u) => u !== null))].sort();
    (!Array.isArray(i.audiences) || pe(i.audiences) !== pe(d)) && U(`${i.clientId} audiences differ from operationBindings`), pe(i.allowedOperations) !== pe(i.operationBindings.map(({ operation: u }) => u)) && U(`${i.clientId} allowedOperations differ from operationBindings`);
  });
  for (const i of ro) {
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
    (!a || pe(a) !== pe(i)) && U(`registry projection drifted from compiled row ${i.registryRow}`);
  }
  return vt(e);
}
const { registryReleaseSha256: lo, ...uo } = Ai, fo = await so(pe(uo));
fo !== lo && U("registry release SHA-256 mismatch");
const Dn = co(Ai);
function po(e) {
  const t = ft(e, ["clientId", "operation", "requestOrigin"], "registry lookup");
  (typeof t.clientId != "string" || typeof t.requestOrigin != "string" || typeof t.operation != "string") && U("registry lookup fields must be strings");
  const n = Dn.clients.find((r) => r.clientId === t.clientId && r.requestOrigin === t.requestOrigin), i = n?.operationBindings.find((r) => r.operation === t.operation);
  return (!n || !i) && U("no exact registry binding exists"), vt({
    audience: i.audience,
    callbackUri: n.callbackUri,
    clientDisplayName: n.clientDisplayName,
    clientId: n.clientId,
    maxLifetimeSeconds: i.maxLifetimeSeconds,
    operation: i.operation,
    requestOrigin: n.requestOrigin,
    registryReleaseSha256: Dn.registryReleaseSha256,
    registryRow: i.registryRow,
    serviceActivationState: i.serviceActivationState,
    serviceInstance: i.serviceInstance
  });
}
const ho = /^\/transaction\/([0-9a-f]{64})$/u, kt = new TextEncoder(), yo = Uint8Array.prototype.fill, mo = Object.freeze({ resolveRegistryBinding: po });
function kn(e) {
  if (e instanceof Uint8Array)
    try {
      yo.call(e, 0);
    } catch {
    }
}
function Pt(e) {
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
function Pn() {
  return new j("INVALID_TRANSACTION");
}
function nt(e, t, n, i) {
  const r = e.createElement("label"), s = e.createElement("span");
  s.textContent = n, r.append(s, i), t.append(r);
}
function Ve(e, t) {
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
function Ei({
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
  const d = (h = o) => !a && h === o && t.isUiGenerationCurrent?.(s) === !0, u = (h, A = o) => {
    d(A) && l?.status && (l.status.textContent = h);
  }, f = () => {
    o += 1;
    for (const [h, A, m] of c)
      try {
        h.removeEventListener?.(A, m);
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
  }, p = (h, A, m) => {
    h.addEventListener?.(A, m), c.push([h, A, m]);
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
    const A = o;
    for (const m of l.rows) {
      const { entry: N } = m, v = async (x) => {
        if (x?.isTrusted !== !0 || !d(A)) return;
        if (N.exportable !== !0) {
          u("Quarantined record is too large to export.", A);
          return;
        }
        let H;
        try {
          if (H = t.exportQuarantinedWalletRecord(N.key), typeof e?.writeText != "function") throw new Error("clipboard unavailable");
          await e.writeText(H), u("Quarantined record exported to the clipboard.", A);
        } catch {
          u("Quarantined record export failed.", A);
        } finally {
          H = null;
        }
      }, V = (x) => {
        if (!(x?.isTrusted !== !0 || !d(A))) {
          try {
            m.confirmation.value = "";
          } catch {
          }
          try {
            m.confirmation.defaultValue = "";
          } catch {
          }
          m.confirmationGroup.hidden = !1, u(`Type ${N.key} to confirm deletion.`, A);
          try {
            m.confirmation.focus?.();
          } catch {
          }
        }
      }, I = (x) => {
        if (!(x?.isTrusted !== !0 || !d(A))) {
          try {
            m.confirmation.value = "";
          } catch {
          }
          try {
            m.confirmation.defaultValue = "";
          } catch {
          }
          m.confirmationGroup.hidden = !0, u("", A);
        }
      }, b = (x) => {
        if (x?.isTrusted !== !0 || !d(A)) return;
        const H = m.confirmation.value;
        if (H !== N.key) {
          u("Type the exact storage key to confirm deletion.", A);
          return;
        }
        try {
          t.deleteQuarantinedWalletRecord(N.key, H);
        } catch {
          u("Quarantined record deletion failed.", A);
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
      p(m.exportButton, "click", v), p(m.deleteButton, "click", V), p(m.cancel, "click", I), p(m.confirm, "click", b);
    }
    return !0;
  }, E = g();
  return Object.freeze({
    container: r,
    destroy() {
      a || (a = !0, f(), r.replaceChildren(), r.remove?.());
    },
    hasEntries: E,
    refresh: g
  });
}
function go({
  clipboard: e = globalThis.navigator?.clipboard,
  controller: t = null,
  document: n,
  mount: i = null,
  offerRememberedUnlock: r = !0,
  title: s = "Sign in"
}) {
  const a = i ?? n?.body;
  if (!n?.createElement || !a?.append) throw new j("DOM_UNAVAILABLE");
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
  f.type = "password", f.name = "password", f.autocomplete = "current-password", f.required = !0, nt(n, d, "Username", u), nt(n, d, "Password", f);
  let p = null;
  if (t && typeof t == "object") {
    p = n.createElement("input"), p.type = "checkbox", p.dataset.walletRemember = "prf-only", p.checked = !1, p.defaultChecked = !1;
    let R = !1;
    try {
      R = t.supportsRememberedWallet?.() === !0;
    } catch {
      R = !1;
    }
    p.disabled = !R, nt(n, d, "Remember on this device", p);
  }
  const g = n.createElement("p");
  g.dataset.walletRememberStatus = "true";
  let E = () => {
  };
  const h = Ei({
    clipboard: e,
    controller: t,
    document: n,
    onChanged: () => E()
  }), A = n.createElement("div");
  A.className = "wallet-login-actions";
  let m = null, N = !1;
  try {
    N = t?.canRestoreRememberedWallet?.() === !0;
  } catch {
    N = !1;
  }
  N && r && (m = n.createElement("button"), m.type = "button", m.dataset.walletAction = "unlock-remembered", m.textContent = "Unlock remembered wallet");
  const v = n.createElement("button");
  v.type = "submit", v.dataset.walletAction = "login", v.textContent = "Login";
  const V = n.createElement("button");
  V.type = "button", V.dataset.walletAction = "cancel-login", V.textContent = "Cancel", m && A.append(m), A.append(v, V), d.append(A), o.append(c, l), h?.hasEntries && o.append(h.container), o.append(d, g), a.append(o), t?.registerCredentialControls?.({ passwordControl: f, usernameControl: u });
  let I = !1, b, x;
  const H = new Promise((R, W) => {
    b = R, x = W;
  }), z = () => {
    d.removeEventListener?.("submit", ee), V.removeEventListener?.("click", G), m?.removeEventListener?.("click", M);
  }, O = () => {
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
  }, F = () => {
    z(), Ve(d, [u, f]), O(), o.remove?.();
  }, D = (R) => {
    if (!I) {
      I = !0;
      try {
        t?.revokeNow?.(R);
      } catch {
      }
      F(), x(new j(R));
    }
  }, ee = (R) => {
    R?.preventDefault?.(), !(I || R?.isTrusted !== !0) && (I = !0, z(), h?.destroy?.(), b({ passwordControl: f, rememberControl: p, rememberStatus: g, usernameControl: u }));
  }, G = (R) => {
    R?.isTrusted === !0 && D("USER_CANCELLED");
  }, M = (R) => {
    I || R?.isTrusted !== !0 || (I = !0, z(), h?.destroy?.(), Ve(d, [u, f]), b({ remembered: !0, rememberStatus: g }));
  };
  E = () => {
    if (I || !r) return;
    let R = !1;
    try {
      R = t?.canRestoreRememberedWallet?.() === !0;
    } catch {
      R = !1;
    }
    R && !m ? (m = n.createElement("button"), m.type = "button", m.dataset.walletAction = "unlock-remembered", m.textContent = "Unlock remembered wallet", A.replaceChildren(m, v, V), m.addEventListener?.("click", M)) : !R && m && (m.removeEventListener?.("click", M), m.remove?.(), m = null);
  }, d.addEventListener("submit", ee), V.addEventListener("click", G), m?.addEventListener?.("click", M);
  try {
    u.focus?.();
  } catch {
  }
  return Object.freeze({
    cancel() {
      I ? F() : D("STALE_CONTROLLER");
    },
    controls: Object.freeze({ passwordControl: f, rememberControl: p, rememberStatus: g, usernameControl: u }),
    form: d,
    promise: H,
    remove: F
  });
}
function wi({
  document: e,
  mount: t = null,
  submitLabel: n = "Compare legacy account",
  title: i = "Enter the legacy BIP-39 mnemonic"
}) {
  const r = t ?? e?.body;
  if (!e?.createElement || !r?.append) throw new j("DOM_UNAVAILABLE");
  const s = e.createElement("section");
  s.className = "wallet-login wallet-legacy-credentials";
  const a = e.createElement("h1");
  a.textContent = i;
  const o = e.createElement("form");
  o.noValidate = !0;
  const c = e.createElement("textarea");
  c.name = "mnemonic", c.autocomplete = "off", c.required = !0, nt(e, o, "Mnemonic", c);
  const l = e.createElement("button");
  l.type = "submit", l.dataset.walletAction = "confirm-legacy-mnemonic", l.textContent = n;
  const d = e.createElement("button");
  d.type = "button", d.dataset.walletAction = "cancel-legacy-migration", d.textContent = "Cancel", o.append(l, d), s.append(a, o), r.append(s);
  let u = !1, f, p;
  const g = new Promise((v, V) => {
    f = v, p = V;
  }), E = () => {
    o.removeEventListener?.("submit", m), d.removeEventListener?.("click", N);
  }, h = () => {
    E(), Ve(o, [c]), s.remove?.();
  }, A = () => {
    u || (u = !0, h(), p(new j("USER_CANCELLED")));
  }, m = (v) => {
    v?.preventDefault?.(), !(u || v?.isTrusted !== !0) && (u = !0, E(), f({ form: o, mnemonicControl: c, section: s }));
  }, N = (v) => {
    v?.isTrusted === !0 && A();
  };
  o.addEventListener("submit", m), d.addEventListener("click", N);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({
    cancel() {
      u ? h() : A();
    },
    promise: g,
    remove: h
  });
}
function Ao({ document: e, mount: t = null, title: n = "Choose legacy wallet profile" }) {
  const i = t ?? e?.body;
  if (!e?.createElement || !i?.append) throw new j("DOM_UNAVAILABLE");
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
  for (const [v, V] of [
    ["", "Select a legacy profile"],
    ["password-fast-v1-legacy", "Legacy fast-password profile"],
    ["bip39-mnemonic-v1-legacy", "Legacy BIP-39 mnemonic import"]
  ]) {
    const I = e.createElement("option");
    I.value = v, I.textContent = V, v === "" && (I.disabled = !0, I.selected = !0), c.append(I);
  }
  nt(e, o, "Legacy profile", c);
  const l = e.createElement("button");
  l.type = "submit", l.dataset.walletAction = "continue-legacy-login", l.textContent = "Continue";
  const d = e.createElement("button");
  d.type = "button", d.dataset.walletAction = "cancel-legacy-login", d.textContent = "Cancel", o.append(l, d), r.append(s, a, o), i.append(r);
  let u = !1, f, p;
  const g = new Promise((v, V) => {
    f = v, p = V;
  }), E = () => {
    o.removeEventListener?.("submit", m), d.removeEventListener?.("click", N);
  }, h = () => {
    try {
      c.value = "";
    } catch {
    }
    Ve(o, [c]), r.remove?.();
  }, A = () => {
    u || (u = !0, E(), h(), p(new j("USER_CANCELLED")));
  }, m = (v) => {
    if (v?.preventDefault?.(), u || v?.isTrusted !== !0) return;
    const V = c.value;
    V !== "password-fast-v1-legacy" && V !== "bip39-mnemonic-v1-legacy" || (u = !0, E(), h(), f(V));
  }, N = (v) => {
    v?.isTrusted === !0 && A();
  };
  o.addEventListener("submit", m), d.addEventListener("click", N);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({ cancel: A, promise: g });
}
function lt(e, t, n, i) {
  const r = e.createElement("div"), s = e.createElement("strong");
  s.textContent = `${n}: `;
  const a = e.createElement("span");
  a.textContent = String(i ?? ""), r.append(s, a), t.append(r);
}
function bi(e, t, n, i = null) {
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
function Ii(e, t = "The wallet request could not be completed. Close this window.", n = null) {
  const i = n ?? e?.body;
  if (!e?.createElement || !i?.replaceChildren) return;
  const r = e.createElement("section");
  r.className = "wallet-terminal-error";
  const s = e.createElement("h1");
  s.textContent = "Wallet request stopped";
  const a = e.createElement("p");
  a.textContent = t, r.append(s, a), i.replaceChildren(r);
}
function Eo({
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
  const g = n.createElement("p"), E = n.createElement("div"), h = n.createElement("button");
  h.type = "button", h.dataset.walletAction = "copy-approval", h.textContent = "Copy approval configuration", h.disabled = !u.approvalAvailable, u.approvalAvailable || (g.textContent = en), f.append(p, g, h, E);
  const A = n.createElement("button");
  A.type = "button", A.dataset.walletAction = "logout", A.textContent = "Logout";
  const m = n.createElement("button");
  m.type = "button", m.dataset.walletAction = "return-to-site", m.textContent = "Return to site";
  const N = n.createElement("section");
  N.className = "wallet-legacy-migration";
  const v = as(N, { document: n });
  let V = !1;
  try {
    V = t.canForgetRememberedWallet?.() === !0;
  } catch {
    V = !1;
  }
  const I = V ? n.createElement("section") : null, b = I ? ns(I, { document: n }) : null;
  b && (b.launch.dataset.walletAction = "forget-stored-wallet");
  const x = Ei({
    clipboard: e,
    controller: t,
    document: n
  }), H = n.createElement("p");
  H.className = "wallet-account-exit-status", l.append(d, f), I && l.append(I), x?.hasEntries && l.append(x.container), l.append(m, A, N), a.replaceChildren(l);
  const z = new ss({
    credentialRound: async (L) => {
      const P = s({
        controller: null,
        document: n,
        mount: a,
        title: `Confirm account (${L} of 2)`
      });
      P?.promise && O.add(P);
      try {
        return P?.promise ? await P.promise : await P;
      } finally {
        P?.promise && O.delete(P);
      }
    },
    expectedIdentity: i,
    wasm: c
  }), O = /* @__PURE__ */ new Set(), F = /* @__PURE__ */ new Set(), D = /* @__PURE__ */ new Set(), ee = /* @__PURE__ */ new Set();
  let G = !1, M = !1, R = !1, W = null, q = !1, J = !1, Q = !1, ce = 0, te = !1, Ie = 0;
  const ve = (L) => !M && Ie === L, le = (L) => {
    if (!ve(L)) throw new Error("account surface closed");
  }, $e = (L) => (L instanceof Uint8Array && ee.add(L), L), ke = (L) => {
    kn(L), ee.delete(L);
  }, Fe = () => {
    const L = [...ee];
    ee.clear();
    for (const P of L) kn(P);
  }, ge = () => {
    const L = G || Q || q;
    m.disabled = R, A.disabled = R, M || (h.disabled = L || !u.approvalAvailable, v.launch.disabled = L, b && (b.launch.disabled = L || J, b.confirm.disabled = L, b.cancel.disabled = L));
  }, Ae = (L) => {
    if (!F.has(L)) return !0;
    if (D.has(L)) return !1;
    D.add(L);
    try {
      return (c?.sdn ?? c).destroySdnIdentity(L), F.delete(L), !0;
    } catch {
      return !1;
    } finally {
      D.delete(L);
    }
  }, We = () => {
    for (const L of [...F]) Ae(L);
    return F.size === 0;
  }, ze = async (L) => {
    if (L?.isTrusted !== !0 || G || Q || q) return;
    const P = Ie;
    G = !0, ge(), g.textContent = "Confirm the same account twice to enable Copy.";
    try {
      const ue = await z.confirm();
      le(P);
      const Y = await rs(ue, {
        assertCurrent: () => le(P),
        clipboard: e,
        container: E,
        document: n
      });
      le(P), g.textContent = Y ? "Approval configuration copied." : "Clipboard unavailable. Copy the exact configuration shown below.";
    } catch {
      ve(P) && (g.textContent = "The two entries did not produce the same account.");
    } finally {
      G = !1, ve(P) && ge();
    }
  }, st = async (L) => {
    if (L?.isTrusted !== !0 || Q || G || q) return;
    const P = v.select.value || "password-fast-v1-legacy";
    if (P !== "password-fast-v1-legacy" && P !== "bip39-mnemonic-v1-legacy") {
      v.result.textContent = "Legacy profile unavailable.";
      return;
    }
    const ue = Ie;
    Q = !0, ce += 1, ge(), v.result.textContent = "Enter the selected legacy credentials to compare accounts.";
    let Y = null, Te, Ot, Nt, re = null;
    try {
      let Tt;
      if (P === "password-fast-v1-legacy") {
        Y = s({
          controller: null,
          document: n,
          mount: a,
          title: "Enter the legacy fast-password account"
        }), Y?.promise && O.add(Y);
        const X = Y?.promise ? await Y.promise : await Y;
        le(ue);
        const He = X?.usernameControl?.value, dn = X?.passwordControl?.value;
        if (!Pt(He) || !Pt(dn)) throw new Error("invalid legacy credentials");
        Te = $e(kt.encode(He)), Ot = $e(kt.encode(dn));
        const fn = X?.usernameControl?.form ?? X?.passwordControl?.form ?? Y?.form, Oi = fn?.parentNode;
        Ve(fn, [X?.usernameControl, X?.passwordControl]), Oi?.remove?.(), Tt = { passwordUtf8: Ot, usernameUtf8: Te };
      } else {
        Y = wi({ document: n, mount: a }), O.add(Y);
        const X = await Y.promise;
        le(ue);
        const He = X?.mnemonicControl?.value;
        if (!Pt(He)) throw new Error("invalid legacy credentials");
        Nt = $e(kt.encode(He)), Ve(X.form, [X.mnemonicControl]), X.section?.remove?.(), Tt = { mnemonicUtf8: Nt };
      }
      if (Y?.promise && O.delete(Y), re = await os({
        accountIndex: 0,
        credentials: Tt,
        operation: "sdn.auth.raw-challenge.v1",
        profile: P,
        wasm: c,
        assertCurrent: () => le(ue),
        ownHandle: (X) => F.add(X)
      }), le(ue), !re?.handle) throw new Error("legacy derivation failed");
      const Si = P === "password-fast-v1-legacy" ? "sdn-fast-password-auth-v1-legacy" : "sdn-bip39-auth-v1-legacy", at = Array.isArray(re.identity?.keys) ? re.identity.keys.find((X) => X?.purpose === "sdn-authentication") : null;
      if (re.identity?.identityScheme !== Si || re.identity?.seedProfile !== P || typeof re.identity?.accountXpub != "string" || !at || typeof at.publicKeyHex != "string" || !/^[0-9a-f]{64}$/u.test(at.publicKeyHex))
        throw new Error("legacy identity invalid");
      const Ri = re.identity;
      if (!Ae(re.handle)) throw new Error("legacy destruction failed");
      re = null, le(ue);
      const Li = i.keys.find((X) => X.purpose === "sdn-authentication");
      v.result.replaceChildren(), lt(n, v.result, "Current account xpub", i.accountXpub), lt(n, v.result, "Legacy account xpub", Ri.accountXpub), lt(n, v.result, "Current authentication key", Li?.publicKeyHex), lt(n, v.result, "Legacy authentication key", at.publicKeyHex);
    } catch {
      ve(ue) && (v.result.textContent = "Legacy account comparison could not be completed.");
    } finally {
      re?.handle && Ae(re.handle), Y?.promise && O.delete(Y), ke(Te), ke(Ot), ke(Nt), We(), ce -= 1, Q = !1, ve(ue) && ge();
    }
  }, Ee = () => {
    if (b) {
      try {
        b.confirmation.value = "";
      } catch {
      }
      try {
        b.confirmation.defaultValue = "";
      } catch {
      }
    }
  }, qe = (L) => {
    if (!(!b || L?.isTrusted !== !0 || G || Q || q || J || M)) {
      Ee(), b.confirmationGroup.hidden = !1, b.status.textContent = `Type ${b.confirmationKey} to confirm.`;
      try {
        b.confirmation.focus?.();
      } catch {
      }
    }
  }, ot = (L) => {
    !b || L?.isTrusted !== !0 || q || M || (Ee(), b.confirmationGroup.hidden = !0, b.status.textContent = "Forget cancelled.");
  }, Se = (L) => {
    if (!b || L?.isTrusted !== !0 || G || Q || q || J || M) return;
    const P = b.confirmation.value;
    if (P !== b.confirmationKey) {
      b.status.textContent = "Type the exact storage key to confirm.";
      return;
    }
    q = !0, ge(), Ee();
    try {
      t.forgetRememberedWallet(P), J = !0, b.confirmationGroup.hidden = !0, b.status.textContent = "Stored wallet forgotten. This account remains signed in.";
    } catch {
      b.status.textContent = "Stored wallet could not be forgotten.";
    } finally {
      q = !1, M || ge();
    }
  }, an = (L) => {
    if (M)
      Fe();
    else {
      M = !0, Ie += 1, h.disabled = !0, v.launch.disabled = !0, b && (b.launch.disabled = !0, b.confirm.disabled = !0, b.cancel.disabled = !0, Ee(), b.launch.removeEventListener?.("click", qe), b.confirm.removeEventListener?.("click", Se), b.cancel.removeEventListener?.("click", ot)), x?.destroy?.(), Fe(), h.removeEventListener?.("click", ze), v.launch.removeEventListener?.("click", st);
      for (const P of O) P.cancel?.();
      O.clear();
    }
    L && !te && (H.textContent = "Secure cleanup is still pending. Retry Return or Logout.", l.replaceChildren(H, m, A), m.disabled = !1, A.disabled = !1);
  }, cn = () => {
    const L = z.destroy(), P = We();
    return L && P && ce === 0 && !Q && !G;
  }, vi = () => (an(!1), te || (te = !0, m.disabled = !0, A.disabled = !0, m.removeEventListener?.("click", ln), A.removeEventListener?.("click", un), l.remove?.()), cn()), Lt = (L, P, ue, { requireTrustedEvent: Y = !0 } = {}) => Y && L?.isTrusted !== !0 ? Promise.resolve() : W || (R || te ? Promise.reject(new j("STALE_CONTROLLER")) : (an(!0), cn() ? (R = !0, m.disabled = !0, A.disabled = !0, W = (async () => {
    o(), bi(n, null, ue, a);
    try {
      const Te = await P();
      if (!r()) throw new j("STALE_CONTROLLER");
      return Te;
    } catch (Te) {
      throw r() && Ii(n, void 0, a), Te;
    }
  })(), W) : (H.textContent = "Secure cleanup is still pending. Retry Return or Logout.", m.disabled = !1, A.disabled = !1, Promise.reject(new j("DESTRUCTION_FAILED"))))), ln = (L) => {
    Lt(
      L,
      () => t.returnToSite(),
      "Returning to the requesting site."
    ).catch(() => {
    });
  }, un = (L) => {
    Lt(
      L,
      () => t.logout(),
      "Logged out. Returning to the requesting site."
    ).catch(() => {
    });
  };
  return h.addEventListener("click", ze), v.launch.addEventListener("click", st), b?.launch.addEventListener?.("click", qe), b?.confirm.addEventListener?.("click", Se), b?.cancel.addEventListener?.("click", ot), m.addEventListener("click", ln), A.addEventListener("click", un), Object.freeze({
    destroy: vi,
    logout: () => Lt(
      null,
      () => t.logout(),
      "Logged out. Returning to the requesting site.",
      { requireTrustedEvent: !1 }
    )
  });
}
function wo(e) {
  const t = e?.pathname, n = e?.search ?? "", i = e?.hash ?? "";
  if (typeof t != "string" || n !== "" || i !== "") throw Pn();
  const r = ho.exec(t);
  if (!r) throw Pn();
  return r[1];
}
function bo(e) {
  const t = e?.window ?? globalThis.window, n = e?.document ?? t?.document ?? globalThis.document, i = e?.mount ?? n?.body, r = typeof t?.fetch == "function" ? t.fetch.bind(t) : typeof globalThis.fetch == "function" ? globalThis.fetch.bind(globalThis) : null, s = e?.relay ?? (e?.controller ? null : qs({
    fetch: e?.fetch ?? r,
    location: e?.location ?? t?.location
  })), a = e?.registry ?? mo, o = e?.controller ?? new Ks({
    ...e,
    document: n,
    registry: a,
    relay: s,
    window: t
  }), c = e?.credentialPrompt ?? ((O) => go(O));
  let l = null, d = null, u = null, f = null, p = null, g = [], E = 0, h = !1;
  const A = () => {
    (l?.destroy?.() ?? !0) && (l = null), d = null;
  }, m = () => {
    try {
      i?.replaceChildren?.();
    } catch {
    }
  }, N = () => (E = E >= Number.MAX_SAFE_INTEGER ? 1 : E + 1, E), v = () => {
    h = !0, N();
  }, V = (O) => !h && E === O, I = (O) => {
    if (!V(O)) throw new j("STALE_CONTROLLER");
  }, b = (O) => {
    try {
      O?.remove?.();
    } catch {
    }
    u === O && (u = null);
  }, x = () => {
    v(), u?.cancel?.(), u = null, A(), m();
  }, H = (O, F, D) => {
    O?.addEventListener?.(F, D), g.push([O, F, D]);
  }, z = () => {
    for (const [O, F, D] of g)
      try {
        O?.removeEventListener?.(F, D);
      } catch {
      }
    g = [];
  };
  return H(t, "pagehide", x), H(n, "freeze", x), H(t, "beforeunload", x), H(t, "pageshow", (O) => {
    O?.persisted === !0 && x();
  }), Object.freeze({
    controller: o,
    logout() {
      return l ? l.logout() : (v(), u?.cancel?.(), u = null, d = null, o.logout());
    },
    start() {
      if (f) return f;
      const O = E;
      return h ? (f = Promise.reject(new j("STALE_CONTROLLER")), f) : (f = (async () => {
        try {
          const F = wo(t?.location);
          if (typeof o.prepare != "function" || typeof o.executePrepared != "function" || typeof o.unlockPassword != "function") {
            const M = await o.execute(F);
            return I(O), M;
          }
          const D = await o.prepare(F);
          if (I(O), D.transaction.operation === "sdn.auth.raw-challenge.v1") {
            const M = Ao({
              document: n,
              mount: i,
              title: `Choose the legacy profile for ${D.binding.clientDisplayName}`
            });
            u = M;
            const R = await M.promise;
            I(O), u === M && (u = null);
            const W = R === "password-fast-v1-legacy" ? c({
              controller: null,
              document: n,
              mount: i,
              title: `Sign in to ${D.binding.clientDisplayName}`,
              transaction: D
            }) : wi({
              document: n,
              mount: i,
              submitLabel: "Continue",
              title: `Sign in to ${D.binding.clientDisplayName}`
            });
            u = W?.promise ? W : null;
            const q = W?.promise ? await W.promise : await W;
            I(O);
            const J = await o.unlockLegacy({
              ...q,
              operation: D.transaction.operation,
              profile: R
            });
            I(O), d = J;
          } else {
            let M = !1;
            for (; ; ) {
              const R = c({
                controller: o,
                document: n,
                mount: i,
                offerRememberedUnlock: !M,
                title: `Sign in to ${D.binding.clientDisplayName}`,
                transaction: D
              });
              M && R?.controls?.rememberStatus && (R.controls.rememberStatus.textContent = "Remembered wallet unavailable. Enter username and password."), u = R?.promise ? R : null;
              const W = R?.promise ? await R.promise : await R;
              if (I(O), W?.remembered === !0)
                try {
                  const J = await o.unlockRemembered();
                  I(O), d = J;
                  break;
                } catch {
                  if (!V(O))
                    throw b(R), new j("STALE_CONTROLLER");
                  M = !0, b(R);
                  continue;
                }
              const q = await o.unlockPassword(W);
              I(O), d = q;
              break;
            }
          }
          const G = await o.executePrepared(D);
          return I(O), b(u), D.transaction.operation === "sdn.wallet.account.v1" ? (d = o.copyPublicIdentity(), l = Eo({
            clipboard: e?.clipboard ?? globalThis.navigator?.clipboard,
            controller: o,
            document: n,
            identity: d,
            isAppCurrent: () => V(O),
            makeCredentialPrompt: c,
            mount: i,
            onClear: A,
            wasm: e?.wasm
          })) : (d = null, n?.createElement && i?.replaceChildren && bi(
            n,
            t,
            "The wallet request completed successfully.",
            i
          )), G;
        } catch (F) {
          const D = !V(O);
          throw u?.cancel?.(), u = null, A(), z(), D || Ii(n, F?.code === "USER_CANCELLED" ? "Cancelled. You may close this window." : void 0, i), await (p ?? o.destroy(D ? "stale-startup" : "startup-failure")), D && F?.code !== "DESTRUCTION_FAILED" ? new j("STALE_CONTROLLER") : F;
        }
      })(), f);
    },
    stop(O = "close") {
      v(), u?.cancel?.(), u = null, A(), m();
      const F = o.destroy(O);
      return p || (z(), p = F), p;
    }
  });
}
async function vo(e) {
  const t = bo(e);
  return await t.start(), t;
}
export {
  go as createPasswordCredentialPrompt,
  bo as createWalletOriginApp,
  vo as mountWalletOriginApp,
  wo as transactionIdFromLocation
};
