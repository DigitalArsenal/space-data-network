import { getWalletOriginCapabilities as qn } from "hd-wallet-wasm";
const ji = Object.freeze({
  cm: 0.01,
  km: 1e3,
  m: 1,
  mm: 1e-3
}), Bi = Object.freeze({
  metersPerSourceUnit: ji,
  quaternionNormTolerance: 1e-6,
  scaleComponentExclusiveMin: 0,
  scaleComponentInclusiveMax: 1e6,
  translationComponentAbsMax: 1e6,
  upAxes: Object.freeze(["X_UP", "Y_UP", "Z_UP"])
}), Vi = Object.freeze({ reviewedTransform: Bi, schemaVersion: 1 }), be = new TextEncoder(), zn = Object.getPrototypeOf(Uint8Array.prototype), Mi = Object.getOwnPropertyDescriptor(zn, "buffer").get, $i = Object.getOwnPropertyDescriptor(zn, "length").get, Fi = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength").get, gn = typeof SharedArrayBuffer > "u" ? null : Object.getOwnPropertyDescriptor(SharedArrayBuffer.prototype, "byteLength").get, Ae = "sdn-bip32-slip10-purpose-v1", At = "password-scrypt-v2", De = "ed25519-over-sha256-jcs-v1", An = "ed25519-raw-32-v1", Wi = "ed25519-sdn-signed-request-v2", bn = ["protocolVersion", "providerOrigin", "method", "requestUri", "bodySha256", "bodyBytes", "schema", "entityId", "entityName", "documentCount"], En = ["schemaVersion", "keyId", "identityScheme", "algorithm", "encoding", "signatureProfile", "publicKeyHex", "signatureHex", "requestDigestSha256", "challengeId", "challengeBase64url"], Hn = "https://review.spacedatanetwork.org", Kn = "sdn-asset-review-v1", qi = "asset-review:assets.ipfs.01", zi = "asset-review-authority:assets.ipfs.01", Vt = "sdn-login:sdn.spaceaware.io", Hi = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, ie = /^[0-9a-f]{64}$/u, Yt = /^[0-9a-f]{128}$/u, ke = /^sha256:[0-9a-f]{64}$/u, Ki = /^[1-9A-HJ-NP-Za-km-z]+$/u, Gi = /^b[a-z2-7]{58}$/u, Mt = "abcdefghijklmnopqrstuvwxyz234567", Gn = 131072, Ji = [
  "accountFingerprint",
  "accountIndex",
  "accountLabel",
  "accountPeerId",
  "accountXpub",
  "identityScheme",
  "keys",
  "schemaVersion",
  "seedProfile"
], Qi = [
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
], Yi = [
  "connectionExpiresAt",
  "event",
  "identity",
  "schemaVersion"
], Xi = [
  "algorithm",
  "encoding",
  "identityScheme",
  "keyId",
  "schemaVersion",
  "signatureHex",
  "signatureProfile"
], Zi = [
  "algorithm",
  "canonicalEnvelope",
  "encoding",
  "identityScheme",
  "keyId",
  "schemaVersion",
  "signatureHex",
  "signatureProfile",
  "signedDigestSha256"
], bt = [
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
], er = [
  "metersPerSourceUnit",
  "rotation",
  "scale",
  "sourceUnits",
  "translation",
  "upAxis"
];
function g(e) {
  throw new TypeError(e);
}
function Nt(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function oe(e, t) {
  typeof e != "string" && g(`${t} must be a string`);
  for (let n = 0; n < e.length; n += 1) {
    const i = e.charCodeAt(n);
    let r = i;
    if (i >= 55296 && i <= 56319) {
      const s = e.charCodeAt(n + 1);
      s >= 56320 && s <= 57343 || g(`${t} contains an unpaired surrogate`), r = 65536 + (i - 55296 << 10) + (s - 56320), n += 1;
    } else i >= 56320 && i <= 57343 && g(`${t} contains an unpaired surrogate`);
    ((r & 65535) === 65534 || (r & 65535) === 65535 || r >= 64976 && r <= 65007) && g(`${t} contains a Unicode noncharacter`);
  }
  return e;
}
function Jn(e) {
  typeof e != "string" && g("wire JSON must be a string"), be.encode(e).byteLength > Gn && g("wire JSON is too large"), e.charCodeAt(0) === 65279 && g("wire JSON must not contain a BOM");
  let t = 0, n = 0;
  const i = () => {
    for (; t < e.length && /[\u0009\u000a\u000d\u0020]/u.test(e[t]); ) t += 1;
  }, r = () => {
    e[t] !== '"' && g("invalid JSON string");
    const o = t;
    for (t += 1; t < e.length; ) {
      const c = e.charCodeAt(t);
      if (c === 34) {
        t += 1;
        let l;
        try {
          l = JSON.parse(e.slice(o, t));
        } catch {
          g("invalid JSON string");
        }
        return oe(l, "JSON string");
      }
      c < 32 && g("invalid JSON control character"), c === 92 && (t += 1, t >= e.length && g("unterminated JSON escape")), t += 1;
    }
    g("unterminated JSON string");
  }, s = (o = 0) => {
    o > 32 && g("wire JSON nesting is too deep"), n += 1, n > 4096 && g("wire JSON contains too many values"), i();
    const c = e[t];
    if (c === '"') return r();
    if (c === "{") {
      t += 1;
      const u = /* @__PURE__ */ Object.create(null), p = /* @__PURE__ */ new Set();
      if (i(), e[t] === "}")
        return t += 1, u;
      for (; t < e.length; ) {
        i();
        const f = r();
        if (p.has(f) && g(`duplicate JSON field: ${f}`), p.add(f), i(), e[t] !== ":" && g("missing JSON object colon"), t += 1, u[f] = s(o + 1), i(), e[t] === "}")
          return t += 1, u;
        e[t] !== "," && g("invalid JSON object separator"), t += 1;
      }
      g("unterminated JSON object");
    }
    if (c === "[") {
      t += 1;
      const u = [];
      if (i(), e[t] === "]")
        return t += 1, u;
      for (; t < e.length; ) {
        if (u.push(s(o + 1)), i(), e[t] === "]")
          return t += 1, u;
        e[t] !== "," && g("invalid JSON array separator"), t += 1;
      }
      g("unterminated JSON array");
    }
    for (const [u, p] of [["true", !0], ["false", !1], ["null", null]])
      if (e.startsWith(u, t))
        return t += u.length, p;
    const l = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(e.slice(t));
    l || g("invalid JSON value"), t += l[0].length;
    const d = Number(l[0]);
    return Number.isFinite(d) || g("wire JSON number must be finite"), d;
  };
  i();
  const a = s();
  return i(), t !== e.length && g("wire JSON contains trailing bytes"), a;
}
function Qn(e) {
  return typeof e == "string" ? Jn(e) : e;
}
function tr(e, t) {
  if (e.byteLength !== t.byteLength) return !1;
  let n = 0;
  for (let i = 0; i < e.byteLength; i += 1) n |= e[i] ^ t[i];
  return n === 0;
}
function Q(e, t, n) {
  const i = Qn(e);
  Nt(i) || g(`${n} must be a JSON object`), Object.getOwnPropertySymbols(i).length !== 0 && g(`${n} has an unknown symbol field`);
  const r = Object.getOwnPropertyNames(i).sort(), s = [...t].sort(), a = be.encode(r.join("\0")), o = be.encode(s.join("\0"));
  tr(a, o) || g(`${n} has missing or unknown fields`);
  for (const c of r) {
    const l = Object.getOwnPropertyDescriptor(i, c);
    (!l || !l.enumerable || !("value" in l)) && g(`${n}.${c} must be an enumerable data field`), l.value === void 0 && g(`${n}.${c} must not be undefined`);
  }
  return i;
}
function st(e) {
  if (!e || typeof e != "object" || Object.isFrozen(e)) return e;
  for (const t of Object.values(e)) st(t);
  return Object.freeze(e);
}
function ae(e) {
  return st(Object.fromEntries([...e].sort(([t], [n]) => t < n ? -1 : t > n ? 1 : 0)));
}
function Xt(e, t, n) {
  (!Array.isArray(e) || Object.getPrototypeOf(e) !== Array.prototype || e.length !== t) && g(`${n} must be a plain array with exactly ${t} values`);
  const i = [...Array.from({ length: t }, (s, a) => String(a)), "length"], r = Reflect.ownKeys(e);
  (r.length !== i.length || r.some((s, a) => s !== i[a])) && g(`${n} must be a dense plain array`);
  for (let s = 0; s < t; s += 1) {
    const a = Object.getOwnPropertyDescriptor(e, String(s));
    (!a || !a.enumerable || !("value" in a) || a.value === void 0) && g(`${n} must contain enumerable data values`);
  }
}
function nr(e) {
  const t = Q(
    e,
    ["reviewedTransform", "schemaVersion"],
    "asset review protocol policy"
  );
  I(t.schemaVersion, 1, "asset review protocol schemaVersion");
  const n = Q(t.reviewedTransform, [
    "metersPerSourceUnit",
    "quaternionNormTolerance",
    "scaleComponentExclusiveMin",
    "scaleComponentInclusiveMax",
    "translationComponentAbsMax",
    "upAxes"
  ], "asset review transform policy"), i = Q(
    n.metersPerSourceUnit,
    ["cm", "km", "m", "mm"],
    "asset review unit policy"
  );
  for (const [r, s] of Object.entries(i))
    (typeof s != "number" || !Number.isFinite(s) || s <= 0) && g(`asset review unit policy ${r} must be positive and finite`);
  for (const [r, s] of [
    ["quaternionNormTolerance", n.quaternionNormTolerance],
    ["scaleComponentExclusiveMin", n.scaleComponentExclusiveMin],
    ["scaleComponentInclusiveMax", n.scaleComponentInclusiveMax],
    ["translationComponentAbsMax", n.translationComponentAbsMax]
  ])
    (typeof s != "number" || !Number.isFinite(s)) && g(`asset review transform policy ${r} must be finite`);
  return (n.quaternionNormTolerance <= 0 || n.scaleComponentInclusiveMax <= n.scaleComponentExclusiveMin || n.translationComponentAbsMax <= 0) && g("asset review transform policy bounds are inconsistent"), Xt(n.upAxes, 3, "asset review up-axis policy"), n.upAxes.join("\0") !== "X_UP\0Y_UP\0Z_UP" && g("asset review up-axis policy is invalid"), ae([
    ["metersPerSourceUnit", ae(Object.entries(i))],
    ["quaternionNormTolerance", n.quaternionNormTolerance],
    ["scaleComponentExclusiveMin", n.scaleComponentExclusiveMin],
    ["scaleComponentInclusiveMax", n.scaleComponentInclusiveMax],
    ["translationComponentAbsMax", n.translationComponentAbsMax],
    ["upAxes", Object.freeze(Array.from({ length: 3 }, (r, s) => n.upAxes[s]))]
  ]);
}
const _e = nr(Vi);
function I(e, t, n) {
  return e !== t && g(`${n} must equal ${JSON.stringify(t)}`), e;
}
function rt(e, t, n) {
  return t.includes(e) || g(`${n} is not an allowed value`), e;
}
function W(e, t, n) {
  return oe(e, n), t.test(e) || g(`${n} has an invalid encoding`), e;
}
function Ee(e, t) {
  return oe(e, t), (!Hi.test(e) || new Date(e).toISOString() !== e) && g(`${t} must be exact RFC3339 milliseconds UTC`), e;
}
function Tt(e, t) {
  const n = Date.parse(e), i = Date.parse(t);
  i > n && i - n <= 3e5 || g("request lifetime must be in (0, 300] seconds");
}
function ir(e, t) {
  (!ArrayBuffer.isView(e) || !(e instanceof Uint8Array) || Object.getPrototypeOf(e) !== Uint8Array.prototype) && g(`${t} must be a plain Uint8Array`), $i.call(e) !== 32 && g(`${t} must be exactly 32 bytes`);
  const i = Array.from({ length: 32 }, (d, u) => String(u)), r = Reflect.ownKeys(e);
  (r.length !== i.length || r.some((d, u) => d !== i[u])) && g(`${t} Uint8Array must contain only 32 indexed bytes`);
  const s = Mi.call(e);
  let a = !1;
  if (gn)
    try {
      gn.call(s), a = !0;
    } catch {
      a = !1;
    }
  a && g(`${t} must not use SharedArrayBuffer storage`);
  try {
    Fi.call(s);
  } catch {
    g(`${t} must use ArrayBuffer storage`);
  }
  const o = new Uint8Array(32);
  for (let d = 0; d < 32; d += 1) {
    const u = Object.getOwnPropertyDescriptor(e, String(d));
    (!u || !u.enumerable || !("value" in u) || !Number.isInteger(u.value) || u.value < 0 || u.value > 255) && g(`${t} Uint8Array contains an invalid indexed byte`), o[d] = e[d];
  }
  let c = "";
  for (let d = 0; d < 32; d += 1) c += String.fromCharCode(o[d]);
  const l = globalThis.btoa(c).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/gu, "");
  return l.length !== 43 && g(`${t} did not encode to 43 base64url characters`), l;
}
function Zt(e, t) {
  oe(e, t), /^[A-Za-z0-9_-]{43}$/u.test(e) || g(`${t} must be canonical unpadded base64url`);
  let n;
  try {
    n = globalThis.atob(`${e.replace(/-/gu, "+").replace(/_/gu, "/")}=`);
  } catch {
    g(`${t} must be valid base64url`);
  }
  const i = Uint8Array.from(n, (r) => r.charCodeAt(0));
  return (i.byteLength !== 32 || ir(i, t) !== e) && g(`${t} must canonically encode exactly 32 bytes`), e;
}
function rr(e) {
  let t = 0, n = 0, i = "";
  for (const r of e)
    for (t = t << 8 | r, n += 8; n >= 5; )
      n -= 5, i += Mt[t >>> n & 31], t &= (1 << n) - 1;
  return n !== 0 && (i += Mt[t << 5 - n & 31]), i;
}
function sr(e, t) {
  oe(e, "modelCid"), Gi.test(e) || g("modelCid must be canonical CIDv1 raw sha2-256 base32");
  let n = 0, i = 0;
  const r = [];
  for (const o of e.slice(1)) {
    const c = Mt.indexOf(o);
    c < 0 && g("modelCid contains invalid base32"), n = n << 5 | c, i += 5, i >= 8 && (i -= 8, r.push(n >>> i & 255), n &= (1 << i) - 1);
  }
  (i !== 0 && n !== 0 || r.length !== 36) && g("modelCid has noncanonical base32 tail bits or length");
  const s = Uint8Array.from(r);
  (s[0] !== 1 || s[1] !== 85 || s[2] !== 18 || s[3] !== 32) && g("modelCid must encode CIDv1 raw sha2-256 with a 32-byte digest"), `b${rr(s)}` !== e && g("modelCid base32 is not canonical");
  let a = 0;
  for (let o = 0; o < 32; o += 1) {
    const c = Number.parseInt(t.slice(o * 2, o * 2 + 2), 16);
    a |= s[o + 4] ^ c;
  }
  return a !== 0 && g("modelCid digest does not match modelSha256"), e;
}
function Ft(e) {
  return e === null || typeof e == "boolean" ? JSON.stringify(e) : typeof e == "string" ? (oe(e, "canonical JSON string"), JSON.stringify(e)) : typeof e == "number" ? (Number.isFinite(e) || g("canonical JSON numbers must be finite"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(Ft).join(",")}]` : (Nt(e) || g("canonical JSON contains an unsupported value"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${Ft(e[t])}`).join(",")}}`);
}
function en(e, t) {
  for (const n of ["identityScheme", "keyId", "signatureProfile"])
    e[n] !== t[n] && g(`canonicalEnvelope ${n} must match the signature result`);
}
function or(e, t) {
  const i = Q(e, [
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
  en(i, t), I(i.audience, Vt, "SDN login envelope audience"), W(i.challengeSha256, ie, "SDN login envelope challengeSha256"), I(i.clientId, "sdn-node-console-v1", "SDN login envelope clientId"), Ee(i.issuedAt, "SDN login envelope issuedAt"), Ee(i.expiresAt, "SDN login envelope expiresAt"), Tt(i.issuedAt, i.expiresAt), I(i.identityScheme, Ae, "SDN login envelope identityScheme"), W(i.keyId, ke, "SDN login envelope keyId"), I(i.kind, "sdn-login", "SDN login envelope kind"), W(i.nonce, ie, "SDN login envelope nonce"), I(i.protocolVersion, 2, "SDN login envelope protocolVersion"), I(i.requestOrigin, "https://sdn.spaceaware.io", "SDN login envelope requestOrigin"), I(i.signatureProfile, De, "SDN login envelope signatureProfile");
}
function ar(e, t) {
  const n = Q(
    e,
    [...bt, "kind"],
    "authority activation canonicalEnvelope"
  );
  en(n, t), I(n.kind, "asset-review-authority-activation", "activation envelope kind"), Xn(Object.fromEntries(bt.map((i) => [i, n[i]])));
}
function cr(e, t) {
  const n = e?.decision === "approve" ? ["note", "reviewedTransform"] : e?.decision === "disapprove" ? ["reason"] : [], i = [...$t, ...n], r = Q(e, [
    ...i,
    "identityScheme",
    "keyId",
    "kind",
    "purpose",
    "signatureProfile"
  ], "asset review decision canonicalEnvelope");
  en(r, t), I(r.identityScheme, Ae, "decision envelope identityScheme"), W(r.keyId, ke, "decision envelope keyId"), I(r.kind, "asset-review-attestation", "decision envelope kind"), I(r.purpose, "asset-review-approval", "decision envelope purpose"), I(r.signatureProfile, De, "decision envelope signatureProfile"), Zn(Object.fromEntries(i.map((s) => [s, r[s]])));
}
function lr(e, t, n) {
  oe(e, "canonicalEnvelope"), be.encode(e).byteLength > Gn && g("canonicalEnvelope is too large");
  const i = Jn(e);
  return (!Nt(i) || i.kind !== t) && g("canonicalEnvelope kind does not match the operation"), Ft(i) !== e && g("canonicalEnvelope must be exact JCS"), t === "sdn-login" ? or(i, n) : t === "asset-review-authority-activation" ? ar(i, n) : t === "asset-review-attestation" ? cr(i, n) : g("canonicalEnvelope operation is not registered"), e;
}
function ur(e, t) {
  const n = Q(e, Qi, `identity key ${t.purpose}`);
  return I(n.purpose, t.purpose, "key purpose"), I(n.identityScheme, Ae, "key identityScheme"), I(n.seedProfile, At, "key seedProfile"), I(n.signatureProfile, t.signatureProfile, "key signatureProfile"), I(n.curve, t.curve, "key curve"), I(n.derivation, "slip10", "key derivation"), I(n.path, t.path, "key path"), I(n.encoding, "raw", "key encoding"), W(n.publicKeyHex, ie, "key publicKeyHex"), I(n.bip32Fingerprint, null, "key bip32Fingerprint"), W(n.keyId, ke, "key keyId"), ae([
    ["bip32Fingerprint", null],
    ["curve", n.curve],
    ["derivation", "slip10"],
    ["encoding", "raw"],
    ["identityScheme", Ae],
    ["keyId", n.keyId],
    ["path", n.path],
    ["publicKeyHex", n.publicKeyHex],
    ["purpose", n.purpose],
    ["seedProfile", At],
    ["signatureProfile", n.signatureProfile]
  ]);
}
function dr(e) {
  const t = Q(e, Ji, "WalletPublicIdentity");
  I(t.schemaVersion, 1, "identity schemaVersion"), I(t.identityScheme, Ae, "identity identityScheme"), I(t.seedProfile, At, "identity seedProfile"), t.accountIndex !== 0 && g("public wallet identity must use account 0"), I(t.accountLabel, null, "identity accountLabel"), oe(t.accountXpub, "identity accountXpub"), /^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(t.accountXpub) || g("identity accountXpub is invalid"), oe(t.accountPeerId, "identity accountPeerId"), (!t.accountPeerId.startsWith("16Uiu2H") || t.accountPeerId.length < 40 || t.accountPeerId.length > 64 || !Ki.test(t.accountPeerId)) && g("identity accountPeerId is invalid"), W(t.accountFingerprint, /^[0-9a-f]{8}$/u, "identity accountFingerprint"), Xt(t.keys, 3, "public identity keys array");
  const n = [
    {
      purpose: "asset-review-approval",
      curve: "ed25519",
      signatureProfile: De,
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
      signatureProfile: De,
      path: "m/44'/0'/0'/0'/0'"
    }
  ], i = Array.from(
    { length: 3 },
    (r, s) => ur(t.keys[s], n[s])
  );
  return ae([
    ["accountFingerprint", t.accountFingerprint],
    ["accountIndex", 0],
    ["accountLabel", null],
    ["accountPeerId", t.accountPeerId],
    ["accountXpub", t.accountXpub],
    ["identityScheme", Ae],
    ["keys", st(i)],
    ["schemaVersion", 1],
    ["seedProfile", At]
  ]);
}
function Yn(e, t) {
  const n = Q(e, Yi, `${t} result`);
  I(n.schemaVersion, 1, "connection result schemaVersion"), rt(n.event, ["connected", "disconnected"], "connection event"), t === "connect" && n.event !== "connected" && g("connect result must be connected");
  let i = null, r = null;
  return n.event === "connected" ? (i = dr(n.identity), r = Ee(n.connectionExpiresAt, "connectionExpiresAt")) : (n.identity !== null || n.connectionExpiresAt !== null) && g("disconnected result must clear identity and expiry"), ae([
    ["connectionExpiresAt", r],
    ["event", n.event],
    ["identity", i],
    ["schemaVersion", 1]
  ]);
}
function fr(e) {
  const t = Q(e, Xi, "raw signature");
  return I(t.schemaVersion, 1, "signature schemaVersion"), W(t.keyId, ke, "signature keyId"), rt(t.identityScheme, [
    "sdn-fast-password-auth-v1-legacy",
    "sdn-bip39-auth-v1-legacy"
  ], "raw signature identityScheme"), I(t.algorithm, "ed25519", "signature algorithm"), I(t.encoding, "raw", "signature encoding"), I(t.signatureProfile, An, "signature profile"), W(t.signatureHex, Yt, "signatureHex"), ae([
    ["algorithm", "ed25519"],
    ["encoding", "raw"],
    ["identityScheme", t.identityScheme],
    ["keyId", t.keyId],
    ["schemaVersion", 1],
    ["signatureHex", t.signatureHex],
    ["signatureProfile", An]
  ]);
}
function tn(e, t) {
  const n = Q(e, Zi, "canonical signature");
  return I(n.schemaVersion, 1, "signature schemaVersion"), W(n.keyId, ke, "signature keyId"), I(n.identityScheme, Ae, "signature identityScheme"), I(n.algorithm, "ed25519", "signature algorithm"), I(n.encoding, "raw", "signature encoding"), I(n.signatureProfile, De, "signature profile"), lr(n.canonicalEnvelope, t, n), W(n.signedDigestSha256, ie, "signedDigestSha256"), W(n.signatureHex, Yt, "signatureHex"), ae([
    ["algorithm", "ed25519"],
    ["canonicalEnvelope", n.canonicalEnvelope],
    ["encoding", "raw"],
    ["identityScheme", Ae],
    ["keyId", n.keyId],
    ["schemaVersion", 1],
    ["signatureHex", n.signatureHex],
    ["signatureProfile", De],
    ["signedDigestSha256", n.signedDigestSha256]
  ]);
}
function pr(e) {
  const t = Q(e, ["challengeBase64url", "protocolVersion"], "SDN login v1 request");
  return I(t.protocolVersion, 1, "SDN login v1 protocolVersion"), Zt(t.challengeBase64url, "challengeBase64url"), ae([
    ["challengeBase64url", t.challengeBase64url],
    ["protocolVersion", 1]
  ]);
}
function hr(e) {
  const t = Q(e, bn, "SDN publication request");
  I(t.protocolVersion, 1, "publication protocolVersion"), I(t.method, "POST", "publication method"), rt(t.schema, ["CZM", "ETM"], "publication schema"), oe(t.providerOrigin, "providerOrigin");
  let n;
  try {
    n = new URL(t.providerOrigin);
  } catch {
    g("providerOrigin must be canonical HTTPS");
  }
  (n.protocol !== "https:" || t.providerOrigin !== n.origin || n.username || n.password || n.port === "0" || n.pathname !== "/" || n.search || n.hash || n.hostname.length > 253 || n.hostname.split(".").some((s) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/u.test(s))) && g("providerOrigin must be a canonical HTTPS DNS or IPv4 origin");
  const i = `/api/v1/data/publish/${t.schema}`, r = `/api/v1/data/publish/batch/${t.schema}`;
  rt(t.requestUri, [i, `${i}.fbs`, r, `${r}.fbs`], "publication requestUri"), W(t.bodySha256, ie, "publication bodySha256");
  for (const [s, a] of [["bodyBytes", 1048576], ["documentCount", 1e5]])
    (!Number.isInteger(t[s]) || t[s] < 1 || t[s] > a) && g(`publication ${s} is outside its bounds`);
  for (const s of ["entityId", "entityName"]) {
    oe(t[s], s);
    const a = be.encode(t[s]).byteLength;
    (a < 1 || a > 256 || /[\u0000-\u001f\u007f]/u.test(t[s])) && g(`${s} must contain 1..256 UTF-8 bytes without controls`);
  }
  return ae(bn.map((s) => [s, t[s]]));
}
function yr(e) {
  const t = Q(e, En, "SDN publication signature");
  return I(t.schemaVersion, 1, "publication signature schemaVersion"), I(t.identityScheme, Ae, "publication identityScheme"), I(t.algorithm, "ed25519", "publication algorithm"), I(t.encoding, "raw", "publication encoding"), I(t.signatureProfile, Wi, "publication signatureProfile"), W(t.keyId, ke, "publication keyId"), W(t.publicKeyHex, ie, "publication publicKeyHex"), W(t.signatureHex, Yt, "publication signatureHex"), W(t.requestDigestSha256, ie, "publication requestDigestSha256"), W(t.challengeId, /^[0-9a-f]{32}$/u, "publication challengeId"), Zt(t.challengeBase64url, "publication challengeBase64url"), ae(En.map((n) => [n, t[n]]));
}
function mr(e) {
  const t = Q(e, [
    "audience",
    "challengeBase64url",
    "expiresAt",
    "issuedAt",
    "nonce",
    "protocolVersion"
  ], "SDN login v2 request");
  return I(t.protocolVersion, 2, "SDN login v2 protocolVersion"), I(t.audience, Vt, "SDN login v2 audience"), Zt(t.challengeBase64url, "challengeBase64url"), W(t.nonce, ie, "SDN login nonce"), Ee(t.issuedAt, "SDN login issuedAt"), Ee(t.expiresAt, "SDN login expiresAt"), Tt(t.issuedAt, t.expiresAt), ae([
    ["audience", Vt],
    ["challengeBase64url", t.challengeBase64url],
    ["expiresAt", t.expiresAt],
    ["issuedAt", t.issuedAt],
    ["nonce", t.nonce],
    ["protocolVersion", 2]
  ]);
}
function Xn(e) {
  const t = Q(e, bt, "authority activation request");
  return I(t.protocolVersion, 1, "activation protocolVersion"), I(t.audience, zi, "activation audience"), I(t.requestOrigin, Hn, "activation requestOrigin"), I(t.clientId, Kn, "activation clientId"), I(t.serviceInstance, "assets.ipfs.01/asset-review-attestation", "activation serviceInstance"), I(t.purpose, "asset-review-authority-activation", "activation purpose"), W(t.nonce, ie, "activation nonce"), Ee(t.issuedAt, "activation issuedAt"), Ee(t.expiresAt, "activation expiresAt"), Tt(t.issuedAt, t.expiresAt), W(t.publicKeyHex, ie, "activation publicKeyHex"), W(t.keyId, ke, "activation keyId"), I(t.identityScheme, Ae, "activation identityScheme"), I(t.signatureProfile, De, "activation signatureProfile"), ae(bt.map((n) => [n, t[n]]));
}
function Pt(e, t, n) {
  Xt(e, t, `${n} array`);
  const i = Array.from({ length: t }, (r, s) => {
    const a = e[s];
    return (typeof a != "number" || !Number.isFinite(a)) && g(`${n} values must be finite numbers`), a;
  });
  return st(i);
}
function gr(e) {
  const t = Q(e, er, "reviewedTransform"), n = Pt(t.translation, 3, "translation"), i = Pt(t.rotation, 4, "rotation"), r = Pt(t.scale, 3, "scale");
  n.some((a) => Math.abs(a) > _e.translationComponentAbsMax) && g("translation values exceed the reviewed transform policy"), r.some((a) => a <= _e.scaleComponentExclusiveMin || a > _e.scaleComponentInclusiveMax) && g("scale values exceed the reviewed transform policy");
  const s = Math.hypot(...i);
  return Math.abs(s - 1) > _e.quaternionNormTolerance && g("rotation must be a unit quaternion within the reviewed tolerance"), rt(t.upAxis, _e.upAxes, "upAxis"), oe(t.sourceUnits, "sourceUnits"), Object.hasOwn(_e.metersPerSourceUnit, t.sourceUnits) || g("sourceUnits is not allowed by the reviewed transform policy"), (typeof t.metersPerSourceUnit != "number" || !Number.isFinite(t.metersPerSourceUnit) || t.metersPerSourceUnit !== _e.metersPerSourceUnit[t.sourceUnits]) && g("metersPerSourceUnit must exactly match sourceUnits"), ae([
    ["metersPerSourceUnit", t.metersPerSourceUnit],
    ["rotation", i],
    ["scale", r],
    ["sourceUnits", t.sourceUnits],
    ["translation", n],
    ["upAxis", t.upAxis]
  ]);
}
function wn(e) {
  return e >= 9 && e <= 13 || e === 32 || e === 160 || e === 5760 || e >= 8192 && e <= 8202 || e === 8232 || e === 8233 || e === 8239 || e === 8287 || e === 12288 || e === 65279;
}
function In(e, t, n) {
  if (n && e === null) return null;
  oe(e, t), (e.length === 0 || be.encode(e).byteLength > 2e3) && g(`${t} has an invalid length`);
  const i = Array.from(e, (r) => r.codePointAt(0));
  return (wn(i[0]) || wn(i[i.length - 1])) && g(`${t} must already be trimmed`), e;
}
function Zn(e) {
  const t = Qn(e);
  Nt(t) || g("asset review decision request must be a JSON object");
  const n = Object.getOwnPropertyDescriptor(t, "decision");
  (!n || !n.enumerable || !("value" in n) || n.value === void 0) && g("asset review decision request has an invalid decision field");
  const i = n.value, r = i === "approve" ? ["note", "reviewedTransform"] : i === "disapprove" ? ["reason"] : [], s = Q(t, [...$t, ...r], "asset review decision request");
  I(s.protocolVersion, 1, "decision protocolVersion"), I(s.audience, qi, "decision audience"), I(s.requestOrigin, Hn, "decision requestOrigin"), I(s.clientId, Kn, "decision clientId"), W(s.challengeId, ie, "challengeId"), W(s.nonce, ie, "decision nonce"), Ee(s.issuedAt, "decision issuedAt"), Ee(s.expiresAt, "decision expiresAt"), Tt(s.issuedAt, s.expiresAt), W(s.modelSha256, ie, "modelSha256"), W(s.metadataSha256, ie, "metadataSha256"), s.previousDecisionHead !== null && W(s.previousDecisionHead, ie, "previousDecisionHead"), sr(s.modelCid, s.modelSha256), (!Number.isSafeInteger(s.modelBytes) || s.modelBytes <= 0) && g("modelBytes must be a positive safe integer"), oe(s.candidateKey, "candidateKey");
  const a = "asset-review:", o = `:${s.modelSha256}`;
  (!s.candidateKey.startsWith(a) || !s.candidateKey.endsWith(o) || be.encode(s.candidateKey).byteLength > 206) && g("candidateKey does not bind the model digest");
  const c = s.candidateKey.slice(a.length, -o.length);
  (!/^[a-z0-9-]+\/[a-z0-9][a-z0-9-]*$/u.test(c) || be.encode(c).byteLength > 128) && g("candidateKey entityId is invalid");
  const l = $t.map((u) => [u, s[u]]);
  return i === "approve" ? (l.push(["note", In(s.note, "note", !0)]), l.push(["reviewedTransform", gr(s.reviewedTransform)])) : l.push(["reason", In(s.reason, "reason", !1)]), Object.values(s).filter((u) => typeof u == "string").reduce((u, p) => u + be.encode(p).byteLength, 0) > 16384 && g("decision request strings exceed 16 KiB"), ae(l);
}
function ei(e, t) {
  return Q(e, [], t), st({});
}
function Ar(e) {
  return ei(e, "wallet connect request");
}
function br(e) {
  return Yn(e, "connect");
}
function Er(e) {
  return ei(e, "wallet account request");
}
function ti(e) {
  return Yn(e, "account");
}
function wr(e) {
  return pr(e);
}
function Ir(e) {
  return fr(e);
}
function vr(e) {
  return mr(e);
}
function Sr(e) {
  return tn(e, "sdn-login");
}
function ni(e) {
  return hr(e);
}
function ii(e) {
  return yr(e);
}
function Lr(e) {
  return Xn(e);
}
function Rr(e) {
  return tn(e, "asset-review-authority-activation");
}
function Or(e) {
  return Zn(e);
}
function Nr(e) {
  return tn(e, "asset-review-attestation");
}
const Tr = Object.freeze([
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
]), ft = /^[0-9a-f]{64}$/u, ri = /^[A-Za-z0-9_-]{43}$/u, Cr = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, _r = new TextEncoder(), si = Object.freeze({
  "sdn.auth.publish-request.v1": Object.freeze({
    parseRequest: ni,
    buildResult: ii,
    publish: !0
  }),
  "sdn.auth.jcs-envelope.v2": Object.freeze({
    parseRequest: vr,
    buildResult: Sr,
    sign(e, t, n, i) {
      return e.signSdnLoginV2(t, n, i.registryRow);
    }
  }),
  "sdn.auth.raw-challenge.v1": Object.freeze({
    parseRequest: wr,
    buildResult: Ir,
    sign(e, t, n) {
      return e.signSdnLoginV1(t, kr(n.challengeBase64url));
    }
  }),
  "sdn.asset-review.authority-activation.v1": Object.freeze({
    parseRequest: Lr,
    buildResult: Rr,
    sign(e, t, n, i) {
      return e.signAssetReviewAuthorityActivation(t, n, i.registryRow);
    }
  }),
  "sdn.asset-review.decision.v1": Object.freeze({
    parseRequest: Or,
    buildResult: Nr,
    sign(e, t, n, i) {
      return e.signAssetReviewDecision(t, n, i.registryRow);
    }
  }),
  "sdn.wallet.account.v1": Object.freeze({
    parseRequest: Er,
    buildResult: ti,
    connect: !0
  }),
  "sdn.wallet.connect.v1": Object.freeze({
    parseRequest: Ar,
    buildResult: br,
    connect: !0
  })
});
class we extends Error {
  constructor(t) {
    super(t), this.name = "WalletOperationError", this.code = t;
  }
}
function _(e) {
  throw new we(e);
}
function nn(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function Wt(e, t, n = "INVALID_TRANSACTION") {
  nn(e) || _(n);
  let i;
  try {
    i = Reflect.ownKeys(e);
  } catch {
    _(n);
  }
  i.some((o) => typeof o != "string") && _(n);
  const r = [...i].sort(), s = [...t].sort();
  (r.length !== s.length || r.some((o, c) => o !== s[c])) && _(n);
  const a = /* @__PURE__ */ Object.create(null);
  for (const o of r) {
    let c;
    try {
      c = Object.getOwnPropertyDescriptor(e, o);
    } catch {
      _(n);
    }
    (!c?.enumerable || !("value" in c) || c.value === void 0) && _(n), a[o] = c.value;
  }
  return a;
}
function pt(e, t = "INVALID_TRANSACTION") {
  (typeof e != "string" || !Cr.test(e)) && _(t);
  const n = Date.parse(e);
  return (!Number.isFinite(n) || new Date(n).toISOString() !== e) && _(t), n;
}
function et(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(et));
  if (!nn(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = et(e[n]);
  return Object.freeze(t);
}
function qt(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" ? JSON.stringify(e) : typeof e == "number" ? (Number.isFinite(e) || _("INVALID_TRANSACTION"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(qt).join(",")}]` : (nn(e) || _("INVALID_TRANSACTION"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${qt(e[t])}`).join(",")}}`);
}
async function xr(e, t) {
  typeof t != "function" && _("CRYPTO_UNAVAILABLE");
  let n;
  try {
    n = await t(_r.encode(qt(e)));
  } catch {
    _("CRYPTO_UNAVAILABLE");
  }
  return (!(n instanceof Uint8Array) || n.byteLength !== 32) && _("CRYPTO_UNAVAILABLE"), Array.from(n, (i) => i.toString(16).padStart(2, "0")).join("");
}
function Dr(e, t) {
  if (typeof e != "string" || typeof t != "string") return !1;
  const n = Math.max(e.length, t.length);
  let i = e.length ^ t.length;
  for (let r = 0; r < n; r += 1)
    i |= (e.charCodeAt(r) || 0) ^ (t.charCodeAt(r) || 0);
  return i === 0;
}
function Pr(e, t) {
  try {
    if (typeof e?.resolveRegistryBinding == "function")
      return e.resolveRegistryBinding(t);
    if (typeof e?.resolve == "function") return e.resolve(t);
    if (typeof e == "function") return e(t);
  } catch (n) {
    if (n instanceof we) throw n;
    _("UNREGISTERED_TRANSACTION");
  }
  _("UNREGISTERED_TRANSACTION");
}
function le({ document: e, window: t }) {
  let n = !1;
  try {
    n = t?.top === t;
  } catch {
    n = !1;
  }
  (!n || e?.visibilityState !== "visible" || typeof e?.hasFocus != "function" || e.hasFocus() !== !0) && _("WALLET_CONTEXT_UNTRUSTED");
}
async function kt(e, {
  registry: t,
  relay: n,
  sha256: i,
  window: r,
  now: s = () => Date.now(),
  expectedTransactionId: a = null
}) {
  const o = Wt(e, Tr);
  (o.schemaVersion !== 1 || typeof o.clientDisplayName != "string" || o.clientDisplayName.length < 1 || o.clientDisplayName.length > 80 || !ft.test(o.transactionId) || !ft.test(o.state) || !ft.test(o.requestSha256) || !ri.test(o.resultToken)) && _("INVALID_TRANSACTION"), a !== null && o.transactionId !== a && _("INVALID_TRANSACTION");
  const c = si[o.operation];
  c || _("UNREGISTERED_TRANSACTION");
  const l = Pr(t, {
    clientId: o.clientId,
    operation: o.operation,
    requestOrigin: o.requestOrigin
  });
  (l.registryReleaseSha256 !== o.registryVersion || l.callbackUri !== o.callbackUri || l.clientDisplayName !== o.clientDisplayName || l.clientId !== o.clientId || l.operation !== o.operation || l.requestOrigin !== o.requestOrigin) && _("REGISTRY_BINDING_MISMATCH");
  const d = s(), u = pt(o.expiresAt);
  (!Number.isFinite(d) || u <= d || u - d > l.maxLifetimeSeconds * 1e3) && _("TRANSACTION_EXPIRED");
  let p;
  try {
    p = c.parseRequest(o.request);
  } catch {
    _("INVALID_TRANSACTION");
  }
  let f;
  try {
    f = await xr(p, i);
  } catch (b) {
    if (b instanceof we) throw b;
    _("CRYPTO_UNAVAILABLE");
  }
  Dr(f, o.requestSha256) || _("REQUEST_HASH_MISMATCH");
  const A = { ...o, request: p };
  return Object.freeze({
    binding: et(l),
    operation: c,
    request: et(p),
    transaction: et(A)
  });
}
function kr(e) {
  ri.test(e) || _("INVALID_TRANSACTION");
  let t;
  try {
    t = globalThis.atob(`${e.replace(/-/gu, "+").replace(/_/gu, "/")}=`);
  } catch {
    _("INVALID_TRANSACTION");
  }
  const n = Uint8Array.from(t, (i) => i.charCodeAt(0));
  return n.byteLength !== 32 && _("INVALID_TRANSACTION"), n;
}
function Ur(e, t, n, i) {
  const r = e.createElement("div");
  r.className = "wallet-confirmation-row";
  const s = e.createElement("strong");
  s.textContent = n;
  const a = e.createElement("span");
  a.textContent = i, r.append(s, a), t.append(r);
}
function vn(e) {
  try {
    return new URL(e).host;
  } catch {
    return String(e ?? "");
  }
}
const Sn = "The site sees your public key and address. It cannot spend or sign anything without asking you.";
function oi(e, t = {}) {
  const n = vn(e.requestOrigin), i = String(e.clientDisplayName ?? "").trim(), r = i || n, s = i && n && i !== n ? [["Site", n]] : [], a = (o, c, l, d = []) => ({
    title: o,
    detail: c,
    action: l,
    facts: [...s, ...d].filter(([, u]) => u != null && String(u).trim() !== "")
  });
  switch (e.operation) {
    case "sdn.auth.raw-challenge.v1":
    case "sdn.auth.jcs-envelope.v2":
      return a(`Sign in to ${r}`, "This proves you hold this wallet. Nothing is paid, sent or shared.", "Sign in");
    case "sdn.wallet.connect.v1":
      return a(`Connect your wallet to ${r}`, Sn, "Connect");
    case "sdn.wallet.account.v1":
      return a(`Share your account with ${r}`, Sn, "Share");
    case "sdn.auth.publish-request.v1":
      return a(`Publish to ${vn(t.providerOrigin)}`, "One signed publication of exactly this data. It grants no session and no later writes.", "Publish", [
        ["Provider", t.providerOrigin],
        ["Data", t.entityName],
        ["Schema", t.schema],
        ["Documents", t.documentCount]
      ]);
    case "sdn.asset-review.authority-activation.v1":
      return a("Activate asset review", `Your key becomes the asset reviewer ${r} accepts.`, "Activate");
    case "sdn.asset-review.decision.v1": {
      const o = t.decision !== "disapprove";
      return a(o ? "Approve this model" : "Disapprove this model", `Your decision is signed and published by ${r}.`, o ? "Approve" : "Disapprove", [
        ["Model", String(t.candidateKey ?? "").replace(/^asset-review:/u, "").replace(/:[0-9a-f]{64}$/u, "")],
        ["Note", t.note]
      ]);
    }
    default:
      return a(`Confirm a request from ${r}`, "", "Confirm");
  }
}
function jr(e, { binding: t, document: n, request: i }) {
  const r = oi(t, i ?? {});
  e.replaceChildren();
  const s = n.createElement("h1");
  if (s.id = "wallet-confirmation-heading", s.textContent = r.title, e.append(s), r.detail) {
    const a = n.createElement("p");
    a.className = "wallet-confirmation-detail", a.textContent = r.detail, e.append(a);
  }
  for (const [a, o] of r.facts) Ur(n, e, a, String(o));
  return e;
}
async function Br({ assertCurrent: e, binding: t, capabilities: n, handle: i, identity: r, transaction: s }) {
  const a = ni(s.request);
  (t.clientId !== "spaceaware-web-v1" || t.requestOrigin !== "https://spaceaware.io" || t.registryRow !== "spaceaware-publish-request-v1" || typeof n.signSdnPublishRequest != "function" || r?.identityScheme !== "sdn-bip32-slip10-purpose-v1" || r?.seedProfile !== "password-scrypt-v2") && _("OPERATION_NOT_ALLOWED");
  const o = r.keys?.filter((y) => y?.purpose === "sdn-authentication") ?? [];
  (o.length !== 1 || o[0].identityScheme !== r.identityScheme || o[0].seedProfile !== r.seedProfile || o[0].curve !== "ed25519" || !ft.test(o[0].publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(o[0].keyId)) && _("OPERATION_NOT_ALLOWED");
  const { publicKeyHex: c, keyId: l } = o[0], d = `${a.providerOrigin}/api/auth/challenge`, u = new AbortController(), p = setTimeout(() => u.abort(), 1e4);
  let f;
  try {
    e(), Date.now() >= pt(s.expiresAt) && _("TRANSACTION_EXPIRED");
    const y = await globalThis.fetch(d, {
      method: "POST",
      credentials: "omit",
      redirect: "error",
      cache: "no-store",
      referrerPolicy: "no-referrer",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ client_pubkey_hex: c, ts: Math.floor(Date.now() / 1e3) }),
      signal: u.signal
    });
    e(), (!y.ok || y.redirected || y.url && y.url !== d) && _("PROVIDER_CHALLENGE_FAILED");
    const m = y.body?.getReader();
    m || _("PROVIDER_CHALLENGE_FAILED");
    const $ = [];
    let v = 0;
    try {
      for (; ; ) {
        const { value: q, done: O } = await m.read();
        if (e(), O) break;
        v += q.byteLength, v > 4096 && _("PROVIDER_CHALLENGE_FAILED"), $.push(q);
      }
    } finally {
      await m.cancel().catch(() => {
      });
    }
    const k = new Uint8Array(v);
    let D = 0;
    for (const q of $)
      k.set(q, D), D += q.byteLength;
    f = Wt(JSON.parse(new TextDecoder("utf-8", { fatal: !0 }).decode(k)), ["challenge_id", "challenge", "expires_at"], "PROVIDER_CHALLENGE_FAILED"), (!/^[0-9a-f]{32}$/u.test(f.challenge_id) || !/^[A-Za-z0-9+/]{43}$/u.test(f.challenge) || !Number.isSafeInteger(f.expires_at) || f.expires_at * 1e3 <= Date.now() || f.expires_at * 1e3 - Date.now() > 3e5) && _("PROVIDER_CHALLENGE_FAILED");
    const L = globalThis.atob(`${f.challenge}=`);
    (L.length !== 32 || globalThis.btoa(L).replace(/=+$/u, "") !== f.challenge) && _("PROVIDER_CHALLENGE_FAILED");
  } catch (y) {
    if (e(), y instanceof we) throw y;
    _("PROVIDER_CHALLENGE_FAILED");
  } finally {
    clearTimeout(p);
  }
  const A = f.challenge.replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
  e(), (Date.now() >= pt(s.expiresAt) || f.expires_at * 1e3 <= Date.now()) && _("TRANSACTION_EXPIRED");
  const b = await n.signSdnPublishRequest(i, {
    ...a,
    challengeId: f.challenge_id,
    challengeBase64url: A
  }, t.registryRow);
  e(), (Date.now() >= pt(s.expiresAt) || f.expires_at * 1e3 <= Date.now()) && _("TRANSACTION_EXPIRED");
  const h = Wt(b, ["schemaVersion", "keyId", "identityScheme", "algorithm", "encoding", "signatureProfile", "publicKeyHex", "signatureHex", "requestDigestSha256"], "INVALID_WALLET_RESULT");
  return (h.keyId !== l || h.publicKeyHex !== c) && _("INVALID_WALLET_RESULT"), ii({ ...h, challengeId: f.challenge_id, challengeBase64url: A });
}
function Vr({ binding: e, document: t, request: n }) {
  const i = t.activeElement ?? null, r = t.createElement("section");
  r.className = "wallet-confirmation", r.setAttribute?.("role", "dialog"), r.setAttribute?.("aria-modal", "true"), r.setAttribute?.("aria-labelledby", "wallet-confirmation-heading"), r.tabIndex = -1, jr(r, { binding: e, document: t, request: n });
  const s = t.createElement("div");
  s.className = "wallet-confirmation-actions";
  const a = t.createElement("button");
  a.type = "button", a.dataset.walletAction = "confirm", a.textContent = oi(e, n ?? {}).action;
  const o = t.createElement("button");
  o.type = "button", o.dataset.walletAction = "cancel", o.textContent = "Cancel", s.append(a, o), r.append(s), t.body.append(r);
  let c = !1, l, d;
  const u = new Promise((y, m) => {
    l = y, d = m;
  }), p = (y, m) => {
    c || m?.isTrusted !== !0 || (c = !0, a.disabled = !0, o.disabled = !0, y ? l() : d(new we("USER_CANCELLED")));
  }, f = (y) => p(!0, y), A = (y) => p(!1, y), b = (y) => {
    if (y?.isTrusted !== !0) return;
    if (y.key === "Escape") {
      y.preventDefault?.(), p(!1, y);
      return;
    }
    if (y.key !== "Tab") return;
    y.preventDefault?.();
    const m = t.activeElement;
    y.shiftKey === !0 ? (m === a ? o : a).focus?.() : (m === o ? a : o).focus?.();
  }, h = (y) => {
    let m = !1;
    try {
      m = r.contains?.(y?.target) === !0;
    } catch {
      m = !1;
    }
    m || a.focus?.();
  };
  a.addEventListener("click", f), o.addEventListener("click", A), r.addEventListener("keydown", b), t.addEventListener?.("focusin", h);
  try {
    a.focus?.();
  } catch {
  }
  return Object.freeze({
    promise: u,
    cancel(y = "STALE_CONTROLLER") {
      c || (c = !0, d(new we(y)));
    },
    destroy() {
      a.removeEventListener?.("click", f), o.removeEventListener?.("click", A), r.removeEventListener?.("keydown", b), t.removeEventListener?.("focusin", h), r.remove();
      try {
        i?.focus?.();
      } catch {
      }
    }
  });
}
async function Mr({
  assertCurrent: e = () => {
  },
  binding: t,
  handle: n,
  identity: i,
  transaction: r,
  wasm: s
}) {
  const a = s?.sdn ?? s, o = si[r.operation];
  if ((!o || !a) && _("OPERATION_NOT_ALLOWED"), o.publish) return Br({ assertCurrent: e, binding: t, capabilities: a, handle: n, identity: i, transaction: r });
  if (o.connect)
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
    _("INVALID_WALLET_RESULT");
  }
}
function $r() {
  return ti({
    connectionExpiresAt: null,
    event: "disconnected",
    identity: null,
    schemaVersion: 1
  });
}
const Ln = "webauthn-prf-hkdf-sha256-aes256gcm-v2", Fr = "sdn-bip32-slip10-purpose-v1", Wr = "password-scrypt-v2", qr = Object.freeze([
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
]), zr = Object.freeze([
  "credentialIdBase64url",
  "identityScheme",
  "schemaVersion",
  "seedProfile",
  "storageProfile",
  "usernameSha256"
]), Hr = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u, Kr = /^[0-9a-f]{64}$/u, Gr = /^[A-Za-z0-9_-]+$/u, Jr = new TextEncoder(), ue = "sdn.wallet.remembered.v2", Le = `${ue}.pending`, ht = 16 * 1024, ai = Object.freeze([
  "wallet_storage_metadata",
  "wallet_storage_encrypted",
  "wallet_storage_passkey_credential",
  "encrypted_wallet",
  "passkey_credential",
  "passkey_wallet"
]), ci = /* @__PURE__ */ new Set([
  ue,
  Le,
  ...ai
]), zt = /* @__PURE__ */ new WeakSet();
class ot extends Error {
  constructor(t) {
    super(t), this.name = "WalletStorageError", this.code = t;
  }
}
function T(e) {
  throw new ot(e);
}
function li(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function Rn(e, t) {
  li(e) || T("INVALID_REMEMBERED_WALLET");
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
function ui(e) {
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
function di(e) {
  if (!ui(e)) return !1;
  const t = Jr.encode(e).length;
  return t >= 3 && t <= 64 && !/[\u0000-\u001f\u007f-\u009f]/u.test(e) && !/^ | $/u.test(e) && !/[A-Z]/u.test(e);
}
function Qr(e) {
  let t = "";
  for (let n = 0; n < e.length; n += 32768)
    t += String.fromCharCode(...e.subarray(n, n + 32768));
  return btoa(t).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
}
function ge(e, { minimum: t = 0, maximum: n = 65536, exact: i = null } = {}) {
  (typeof e != "string" || e.length === 0 || !Gr.test(e)) && T("INVALID_REMEMBERED_WALLET");
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
  return (Qr(o) !== e || o.length < t || o.length > n || i !== null && o.length !== i) && T("INVALID_REMEMBERED_WALLET"), o;
}
function rn(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" || typeof e == "number" && Number.isFinite(e) ? JSON.stringify(e) : (li(e) || T("INVALID_REMEMBERED_WALLET"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${rn(e[t])}`).join(",")}}`);
}
function Yr(e) {
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
function fi(e) {
  const t = Rn(e, qr), n = Rn(t.aad, zr);
  return (t.schemaVersion !== 2 || t.storageProfile !== Ln || n.schemaVersion !== 2 || n.storageProfile !== Ln || n.identityScheme !== Fr || n.seedProfile !== Wr || !di(t.canonicalUsername) || !Kr.test(n.usernameSha256) || t.credentialIdBase64url !== n.credentialIdBase64url || !ui(t.createdAt) || !Hr.test(t.createdAt) || new Date(t.createdAt).toISOString() !== t.createdAt) && T("INVALID_REMEMBERED_WALLET"), ge(t.credentialIdBase64url, { minimum: 1, maximum: 1024 }), ge(t.ciphertextBase64url, { minimum: 17, maximum: 1024 }), ge(t.hkdfSaltBase64url, { exact: 32 }), ge(t.nonceBase64url, { exact: 12 }), ge(t.prfInputBase64url, { exact: 32 }), Yr({ ...t, aad: n });
}
function Xr(e) {
  return rn(fi(e));
}
function sn(e) {
  (typeof e != "string" || e.length === 0 || e.length > 131072) && T("INVALID_REMEMBERED_WALLET");
  let t;
  try {
    t = JSON.parse(e);
  } catch {
    T("INVALID_REMEMBERED_WALLET");
  }
  const n = fi(t);
  return rn(n) !== e && T("INVALID_REMEMBERED_WALLET"), n;
}
function Ht(e, t, { pending: n = !1 } = {}) {
  (!e || typeof e.getItem != "function") && T("STORAGE_UNAVAILABLE");
  let i;
  try {
    i = e.getItem(t);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  if (i === null) return Object.freeze({ raw: null, record: null, status: "empty" });
  typeof i != "string" && T("STORAGE_UNAVAILABLE");
  const r = i.length > ht;
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
    const s = sn(i);
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
function pi(e) {
  const t = Ht(e, Le, { pending: !0 }), n = Ht(e, ue), i = t.status === "empty", r = n.status === "empty" || n.status === "valid";
  return Object.freeze({
    active: n,
    canRestore: i && n.status === "valid",
    canSetup: i && r,
    pending: t
  });
}
function Zr(e, t) {
  const n = pi(e);
  n.canSetup || T("STORAGE_QUARANTINED");
  const i = Xr(t);
  try {
    e.setItem(Le, i);
  } catch (a) {
    throw a;
  }
  let r;
  try {
    r = e.getItem(Le);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  r !== i && T("STORAGE_WRITE_FAILED"), sn(r);
  const s = Object.freeze({
    previousActiveRaw: n.active.raw,
    serialized: i,
    storage: e
  });
  return zt.add(s), s;
}
function es(e, t) {
  (!zt.has(t) || t.storage !== e) && T("INVALID_STORAGE_TRANSACTION"), zt.delete(t);
  let n, i;
  try {
    n = e.getItem(Le), i = e.getItem(ue);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  (n !== t.serialized || i !== t.previousActiveRaw) && T("STORAGE_COLLISION"), e.removeItem(Le), e.getItem(Le) !== null && T("STORAGE_WRITE_FAILED"), e.setItem(ue, t.serialized);
}
function on(e, t) {
  ci.has(t) || T("INVALID_STORAGE_KEY"), (!e || typeof e.getItem != "function") && T("STORAGE_UNAVAILABLE");
  let n;
  try {
    n = e.getItem(t);
  } catch {
    T("STORAGE_UNAVAILABLE");
  }
  if (n === null) return null;
  if (typeof n != "string" && T("STORAGE_UNAVAILABLE"), t === ue && n.length <= ht)
    try {
      sn(n), T("NOT_QUARANTINED");
    } catch (i) {
      if (i instanceof ot && i.code === "NOT_QUARANTINED") throw i;
    }
  return Object.freeze({
    exportable: n.length <= ht,
    key: t,
    oversized: n.length > ht,
    raw: n,
    rawLength: n.length
  });
}
function ts(e) {
  return Object.freeze({
    exportable: e.exportable,
    key: e.key,
    oversized: e.oversized,
    rawLength: e.rawLength
  });
}
function ns(e) {
  const t = [];
  for (const n of [
    ue,
    Le,
    ...ai
  ]) {
    let i;
    try {
      i = on(e, n);
    } catch (r) {
      if (r instanceof ot && r.code === "NOT_QUARANTINED") continue;
      throw r;
    }
    i && t.push(ts(i));
  }
  return Object.freeze(t);
}
function is(e, t) {
  const n = on(e, t);
  return n || T("QUARANTINE_NOT_FOUND"), n.exportable || T("QUARANTINE_EXPORT_TOO_LARGE"), n.raw;
}
function rs(e, t, n) {
  ci.has(t) || T("INVALID_STORAGE_KEY"), n !== t && T("CONFIRMATION_REQUIRED"), on(e, t) || T("QUARANTINE_NOT_FOUND");
  try {
    e.removeItem(t), e.getItem(t) !== null && T("STORAGE_WRITE_FAILED");
  } catch (r) {
    if (r instanceof ot) throw r;
    T("STORAGE_UNAVAILABLE");
  }
}
function ss(e, t) {
  t !== ue && T("CONFIRMATION_REQUIRED");
  const n = Ht(e, ue);
  n.status === "empty" && T("REMEMBER_UNAVAILABLE"), n.status !== "valid" && T("STORAGE_QUARANTINED");
  try {
    e.removeItem(ue), e.getItem(ue) !== null && T("STORAGE_WRITE_FAILED");
  } catch (i) {
    if (i instanceof ot) throw i;
    T("STORAGE_UNAVAILABLE");
  }
}
const Et = "sdn-bip32-slip10-purpose-v1", wt = "password-scrypt-v2", Kt = "m/44'/0'/0'/2'/0'", an = "Approval unavailable — migrate to the new wallet profile", On = Object.freeze({
  "bip39-mnemonic-v1-legacy": "sdn-bip39-auth-v1-legacy",
  "password-fast-v1-legacy": "sdn-fast-password-auth-v1-legacy"
}), Nn = new TextEncoder(), os = Uint8Array.prototype.fill, cn = Object.freeze([
  "accountFingerprint",
  "accountIndex",
  "accountLabel",
  "accountPeerId",
  "accountXpub",
  "identityScheme",
  "keys",
  "schemaVersion",
  "seedProfile"
]), ln = Object.freeze([
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
]), It = Object.freeze([
  Object.freeze({
    curve: "ed25519",
    path: Kt,
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
class hi extends Error {
  constructor(t) {
    super(t === "APPROVAL_UNAVAILABLE" ? an : t), this.name = "WalletAccountError", this.code = t;
  }
}
function M(e) {
  throw new hi(e);
}
function Tn(e) {
  if (e instanceof Uint8Array)
    try {
      os.call(e, 0);
    } catch {
    }
}
function Pe(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(Pe));
  if (!e || typeof e != "object") return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = Pe(e[n]);
  return Object.freeze(t);
}
function as(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function vt(e, t) {
  as(e) || M("INVALID_PUBLIC_IDENTITY");
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
function cs(e) {
  if (!Array.isArray(e) || Object.getPrototypeOf(e) !== Array.prototype || e.length !== It.length) return !1;
  let t;
  try {
    t = Reflect.ownKeys(e);
  } catch {
    return !1;
  }
  const n = ["0", "1", "2", "length"];
  if (t.length !== n.length || t.some((i, r) => i !== n[r])) return !1;
  for (let i = 0; i < It.length; i += 1) {
    const r = Object.getOwnPropertyDescriptor(e, String(i));
    if (!r?.enumerable || !("value" in r)) return !1;
  }
  return !0;
}
function ls(e) {
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
function ye(e) {
  const t = vt(e, cn);
  (t.schemaVersion !== 1 || t.identityScheme !== Et || t.seedProfile !== wt || t.accountIndex !== 0 || t.accountLabel !== null || !/^[0-9a-f]{8}$/u.test(t.accountFingerprint) || !Me(t.accountXpub) || !/^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(t.accountXpub) || !Me(t.accountPeerId) || !/^16Uiu2H[1-9A-HJ-NP-Za-km-z]{33,57}$/u.test(t.accountPeerId) || !cs(t.keys)) && M("INVALID_PUBLIC_IDENTITY");
  const n = It.map((i, r) => {
    const s = vt(t.keys[r], ln);
    return (s.bip32Fingerprint !== null || s.curve !== i.curve || s.derivation !== "slip10" || s.encoding !== "raw" || s.identityScheme !== Et || s.path !== i.path || s.purpose !== i.purpose || s.seedProfile !== wt || s.signatureProfile !== i.signatureProfile || !/^[0-9a-f]{64}$/u.test(s.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(s.keyId)) && M("INVALID_PUBLIC_IDENTITY"), Pe(s);
  });
  return Pe({ ...t, keys: n });
}
function us(e, { accountIndex: t = 0, profile: n } = {}) {
  const i = Object.hasOwn(On, n) ? On[n] : null;
  (!i || t !== 0) && M("INVALID_LEGACY_PROFILE");
  const r = vt(e, cn);
  (r.schemaVersion !== 1 || r.identityScheme !== i || r.seedProfile !== n || r.accountIndex !== t || r.accountLabel !== null || !/^[0-9a-f]{8}$/u.test(r.accountFingerprint) || !Me(r.accountXpub) || !/^xpub[1-9A-HJ-NP-Za-km-z]{107}$/u.test(r.accountXpub) || !Me(r.accountPeerId) || !/^16Uiu2H[1-9A-HJ-NP-Za-km-z]{33,57}$/u.test(r.accountPeerId) || !ls(r.keys)) && M("INVALID_PUBLIC_IDENTITY");
  const s = vt(r.keys[0], ln);
  return (s.bip32Fingerprint !== null || s.curve !== "ed25519" || s.derivation !== "bip32-scalar-as-ed25519-seed" || s.encoding !== "raw" || s.identityScheme !== i || s.path !== `m/44'/0'/${t}'/0/0` || s.purpose !== "sdn-authentication" || s.seedProfile !== n || s.signatureProfile !== "ed25519-raw-32-v1" || !/^[0-9a-f]{64}$/u.test(s.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(s.keyId)) && M("INVALID_PUBLIC_IDENTITY"), Pe({ ...r, keys: [s] });
}
function Cn(e) {
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
function _n(e, t) {
  if (typeof e != "string" || typeof t != "string") return !1;
  const n = Math.max(e.length, t.length);
  let i = e.length ^ t.length;
  for (let r = 0; r < n; r += 2) {
    const s = Number.parseInt(e.slice(r, r + 2), 16), a = Number.parseInt(t.slice(r, r + 2), 16);
    i |= (Number.isNaN(s) ? 256 : s) ^ (Number.isNaN(a) ? 256 : a);
  }
  return i === 0;
}
function ds(e) {
  return !e || typeof e != "object" || !Array.isArray(e.keys) ? null : e.keys.find((t) => t?.purpose === "asset-review-approval") ?? null;
}
function Ut(e, t) {
  try {
    e = ye(e), t = ye(t);
  } catch {
    return !1;
  }
  let n = !0;
  for (const i of cn.filter((r) => r !== "keys"))
    n = e[i] === t[i] && n;
  for (let i = 0; i < It.length; i += 1) {
    const r = e.keys[i], s = t.keys[i];
    for (const a of ln.filter((o) => o !== "publicKeyHex" && o !== "keyId"))
      n = r[a] === s[a] && n;
    n = _n(r.publicKeyHex, s.publicKeyHex) && n, n = _n(r.keyId.slice(7), s.keyId.slice(7)) && n;
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
function yi(e) {
  (e?.identityScheme !== Et || e?.seedProfile !== wt || e?.accountIndex !== 0) && M("APPROVAL_UNAVAILABLE");
  const t = ds(e);
  return (!t || t.identityScheme !== e.identityScheme || t.seedProfile !== e.seedProfile || t.signatureProfile !== "ed25519-over-sha256-jcs-v1" || t.curve !== "ed25519" || t.derivation !== "slip10" || t.path !== Kt || t.encoding !== "raw" || !/^[0-9a-f]{64}$/u.test(t.publicKeyHex) || !/^sha256:[0-9a-f]{64}$/u.test(t.keyId)) && M("APPROVAL_UNAVAILABLE"), Pe({
    algorithm: "Ed25519",
    derivationPath: Kt,
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
function fs(e) {
  return JSON.stringify(e, null, 2);
}
function ps(e, t, { document: n = e.ownerDocument } = {}) {
  e.replaceChildren();
  const i = n.createElement("h1");
  if (i.textContent = "Account", e.append(i), Be(n, e, "Username / account", t?.accountLabel ?? "account 0"), Be(n, e, "Account xpub", t?.accountXpub), Be(n, e, "Peer ID", t?.accountPeerId), Be(n, e, "Fingerprint", t?.accountFingerprint), t?.identityScheme !== Et || t?.seedProfile !== wt) {
    const s = n.createElement("p");
    return s.textContent = an, e.append(s), Object.freeze({ approvalAvailable: !1 });
  }
  const r = yi(t);
  return Be(n, e, "Asset approval public key", r.publicKeyHex), Be(n, e, "Asset approval key ID", r.keyId), Object.freeze({ approvalAvailable: !0, configuration: r });
}
function hs(e, { document: t = e.ownerDocument } = {}) {
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
  a.textContent = `Type ${ue} to confirm.`;
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
    confirmationKey: ue,
    launch: r,
    status: d
  });
}
function ys(e, t, { document: n = e.ownerDocument } = {}) {
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
    const p = n.createElement("button");
    p.type = "button", p.dataset.walletAction = "export-quarantined-wallet", p.dataset.walletQuarantineKey = c.key, p.textContent = c.exportable ? "Export" : "Export unavailable";
    const f = n.createElement("button");
    f.type = "button", f.dataset.walletAction = "delete-quarantined-wallet", f.dataset.walletQuarantineKey = c.key, f.textContent = "Delete";
    const A = n.createElement("div");
    A.hidden = !0;
    const b = n.createElement("p");
    b.textContent = `Type ${c.key} to confirm deletion.`;
    const h = n.createElement("input");
    h.type = "text", h.autocomplete = "off", h.spellcheck = !1, h.dataset.walletQuarantineConfirmation = c.key;
    const y = n.createElement("button");
    y.type = "button", y.dataset.walletAction = "confirm-delete-quarantined-wallet", y.dataset.walletQuarantineKey = c.key, y.textContent = "Confirm delete";
    const m = n.createElement("button");
    m.type = "button", m.dataset.walletAction = "cancel-delete-quarantined-wallet", m.dataset.walletQuarantineKey = c.key, m.textContent = "Cancel", A.append(b, h, y, m), l.append(d, u, p, f, A), s.append(l), o.push(Object.freeze({
      cancel: m,
      confirm: y,
      confirmation: h,
      confirmationGroup: A,
      deleteButton: f,
      entry: c,
      exportButton: p
    }));
  }
  return e.append(i, r, s, a), Object.freeze({ rows: Object.freeze(o), status: a });
}
async function ms(e, {
  assertCurrent: t = () => {
  },
  clipboard: n = globalThis.navigator?.clipboard,
  container: i,
  document: r = i?.ownerDocument
}) {
  const s = fs(e);
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
async function xn(e, t, n, {
  assertCurrent: i,
  ownBuffer: r,
  ownHandle: s,
  releaseBuffer: a
}) {
  const o = await t(n), c = o?.usernameControl, l = o?.passwordControl;
  let d, u;
  try {
    i();
    const f = c?.value, A = l?.value;
    (typeof f != "string" || typeof A != "string") && M("CREDENTIAL_CONFIRMATION_MISMATCH"), (!Me(f) || !Me(A)) && M("CREDENTIAL_CONFIRMATION_MISMATCH"), d = Nn.encode(f), r(d), u = Nn.encode(A), r(u);
  } finally {
    Cn(c), Cn(l);
    const f = c?.form, A = l?.form;
    try {
      (f?.parentNode ?? f)?.remove?.();
    } catch {
    }
    if (A !== f)
      try {
        (A?.parentNode ?? A)?.remove?.();
      } catch {
      }
  }
  let p;
  try {
    i(), p = await e.derivePasswordIdentity({
      accountIndex: 0,
      passwordUtf8: u,
      usernameUtf8: d
    }), p?.handle || M("CREDENTIAL_CONFIRMATION_MISMATCH"), s(p.handle), i();
    let f;
    try {
      f = ye(p.identity);
    } catch {
      M("CREDENTIAL_CONFIRMATION_MISMATCH");
    }
    return { handle: p.handle, identity: f };
  } finally {
    a(d), a(u);
  }
}
class gs {
  #d = null;
  #p;
  #y = !1;
  #a = /* @__PURE__ */ new Set();
  #f;
  #v = 0;
  #e = 0;
  #n = /* @__PURE__ */ new Set();
  #r = /* @__PURE__ */ new Set();
  #t;
  constructor({ wasm: t, credentialRound: n, expectedIdentity: i = null }) {
    this.#t = t?.sdn ?? t, this.#p = n;
    try {
      this.#f = i === null ? null : ye(i);
    } catch {
      M("CREDENTIAL_CONFIRMATION_MISMATCH");
    }
    (typeof this.#t?.derivePasswordIdentity != "function" || typeof this.#t?.destroySdnIdentity != "function" || typeof n != "function") && M("CREDENTIAL_CONFIRMATION_MISMATCH");
  }
  get confirmed() {
    return this.#d;
  }
  clear() {
    return this.#d = null, this.#y || (this.#y = !0, this.#v += 1, this.#f = null), this.#o(), this.#L(), this.#e === 0 && this.#n.size === 0;
  }
  destroy() {
    return this.clear();
  }
  async confirm() {
    if (this.#y && M("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#d) return this.#d;
    this.#e += 1;
    const t = this.#v;
    let n, i;
    try {
      this.#L(), this.#n.size !== 0 && M("CREDENTIAL_CONFIRMATION_MISMATCH");
      const s = {
        assertCurrent: () => this.#h(t),
        ownBuffer: (a) => this.#r.add(a),
        ownHandle: (a) => this.#n.add(a),
        releaseBuffer: (a) => {
          Tn(a), this.#r.delete(a);
        }
      };
      return n = await xn(this.#t, this.#p, 1, s), this.#S(n.handle) || M("CREDENTIAL_CONFIRMATION_MISMATCH"), n.handle = null, this.#h(t), i = await xn(this.#t, this.#p, 2, s), this.#S(i.handle) || M("CREDENTIAL_CONFIRMATION_MISMATCH"), i.handle = null, this.#h(t), (!Ut(n.identity, i.identity) || this.#f !== null && (!Ut(n.identity, this.#f) || !Ut(i.identity, this.#f))) && M("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#n.size !== 0 && M("CREDENTIAL_CONFIRMATION_MISMATCH"), this.#d = yi(i.identity), this.#d;
    } catch (r) {
      if (this.#d = null, r instanceof hi && r.code === "CREDENTIAL_CONFIRMATION_MISMATCH") throw r;
      M("CREDENTIAL_CONFIRMATION_MISMATCH");
    } finally {
      this.#o(), this.#L(), this.#e -= 1;
    }
  }
  #h(t) {
    (this.#y || this.#v !== t) && M("CREDENTIAL_CONFIRMATION_MISMATCH");
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
  #L() {
    for (const t of [...this.#n]) this.#S(t);
  }
  #o() {
    const t = [...this.#r];
    this.#r.clear();
    for (const n of t) Tn(n);
  }
}
async function As({
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
    identity: Pe(c.identity),
    legacy: !0
  });
}
function bs(e, { document: t = e.ownerDocument } = {}) {
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
const St = Uint8Array, mi = ArrayBuffer, un = Object.getPrototypeOf(St.prototype), Es = Object.getOwnPropertyDescriptor(un, "buffer").get, ws = Object.getOwnPropertyDescriptor(un, "byteLength").get, Is = Object.getOwnPropertyDescriptor(un, "byteOffset").get, vs = Object.getOwnPropertyDescriptor(mi.prototype, "byteLength").get;
class Ss extends Error {
  constructor(t = "RNG_FAILURE") {
    super(t), this.name = "WalletRandomError", this.code = t;
  }
}
function Se() {
  throw new Ss();
}
function Ls(e) {
  if (!(e instanceof St) || Object.getPrototypeOf(e) !== St.prototype) return null;
  let t, n, i;
  try {
    t = Reflect.apply(Es, e, []), n = Reflect.apply(ws, e, []), i = Reflect.apply(Is, e, []);
  } catch {
    return null;
  }
  let r;
  try {
    r = Reflect.apply(vs, t, []);
  } catch {
    return null;
  }
  return Object.getPrototypeOf(t) !== mi.prototype || i !== 0 || n !== r || n !== 12 && n !== 32 ? null : t;
}
function Rs({ getRandomValues: e, observedWrite: t } = {}) {
  if (typeof e != "function")
    return () => Se();
  const n = /* @__PURE__ */ new WeakSet(), i = /* @__PURE__ */ new WeakSet();
  return function(s) {
    const a = Ls(s);
    (!a || n.has(s) || i.has(a)) && Se(), n.add(s), i.add(a);
    let o;
    try {
      o = e(s);
    } catch {
      Se();
    }
    if (o !== s && Se(), t !== void 0) {
      typeof t != "function" && Se();
      let c = !1;
      try {
        c = t(s) === !0;
      } catch {
        Se();
      }
      c || Se();
    }
    return s;
  };
}
function Ve(e, t) {
  (typeof e != "function" || t !== 12 && t !== 32) && Se();
  const n = new St(t);
  return e(n);
}
const yt = new TextEncoder(), Os = new TextDecoder("utf-8", { fatal: !0 }), Ns = Uint8Array.prototype.fill, Ts = Uint8Array.prototype.slice, Cs = Uint8Array.prototype.subarray, dn = Object.getPrototypeOf(Uint8Array.prototype), _s = Object.getOwnPropertyDescriptor(dn, "buffer").get, gi = Object.getOwnPropertyDescriptor(dn, "byteLength").get, xs = Object.getOwnPropertyDescriptor(dn, "byteOffset").get, Ai = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength").get, Ds = ArrayBuffer.prototype.slice, Dn = "webauthn-prf-hkdf-sha256-aes256gcm-v2", Ps = "sdn-bip32-slip10-purpose-v1", ks = "password-scrypt-v2", Us = "Space Data Network Wallet", bi = 12e4;
class js extends Error {
  constructor(t) {
    super(t), this.name = "RememberedWalletError", this.code = t;
  }
}
function C(e) {
  throw new js(e);
}
function Pn(e) {
  if (typeof e != "string") return "";
  const t = e.trim().toLowerCase();
  return t.endsWith(".") ? t.slice(0, -1) : t;
}
function Bs(e, t) {
  if (e == null || e === "") return;
  const n = Pn(e);
  n === "" && C("INVALID_RP_ID");
  const i = Pn(t);
  if (i === "" || n === i || i.endsWith(`.${n}`)) return n;
  C("INVALID_RP_ID");
}
function P(e) {
  if (e instanceof Uint8Array)
    try {
      Ns.call(e, 0);
    } catch {
    }
}
function Lt(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function Ei(e, t) {
  if (!Lt(e)) return !1;
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
function tt(e, t) {
  if (!Ei(e, t)) return null;
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
function mt(e, t = null) {
  if (!(e instanceof Uint8Array) || Object.getPrototypeOf(e) !== Uint8Array.prototype)
    return !1;
  let n, i, r, s;
  try {
    n = Reflect.apply(_s, e, []), i = Reflect.apply(gi, e, []), r = Reflect.apply(xs, e, []), s = Reflect.apply(Ai, n, []);
  } catch {
    return !1;
  }
  return Object.getPrototypeOf(n) === ArrayBuffer.prototype && r === 0 && i === s && (t === null || i === t);
}
function wi(e, t, n) {
  if (!(e instanceof ArrayBuffer) || Object.getPrototypeOf(e) !== ArrayBuffer.prototype)
    return !1;
  let i;
  try {
    i = Reflect.apply(Ai, e, []);
  } catch {
    return !1;
  }
  return i >= t && i <= n;
}
function xe(e) {
  try {
    return Reflect.apply(gi, e, []);
  } catch {
    return -1;
  }
}
function Je(e) {
  let t = "";
  const n = xe(e);
  n < 0 && C("INVALID_REMEMBERED_WALLET");
  for (let i = 0; i < n; i += 32768)
    t += String.fromCharCode(...Reflect.apply(
      Cs,
      e,
      [i, i + 32768]
    ));
  return btoa(t).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
}
function Vs(e) {
  let t = "";
  for (const n of e) t += n.toString(16).padStart(2, "0");
  return t;
}
function $e(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" || typeof e == "number" && Number.isFinite(e) ? JSON.stringify(e) : Array.isArray(e) ? `[${e.map($e).join(",")}]` : (Lt(e) || C("INVALID_REMEMBERED_WALLET"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${$e(e[t])}`).join(",")}}`);
}
function Ms(e, t) {
  if (!(e instanceof Uint8Array) || !(t instanceof Uint8Array)) return !1;
  const n = xe(e), i = xe(t);
  if (n < 0 || i < 0) return !1;
  const r = Math.max(n, i);
  let s = n ^ i;
  for (let a = 0; a < r; a += 1)
    s |= (e[a] ?? 0) ^ (t[a] ?? 0);
  return s === 0;
}
function $s(e, t) {
  return $e(e) === $e(t);
}
function Ii(e) {
  (!e || typeof e != "object" && typeof e != "function") && C("WEBAUTHN_INVALID_RESPONSE");
  let t, n, i;
  try {
    t = e.type, n = e.rawId, i = e.getClientExtensionResults;
  } catch {
    C("WEBAUTHN_INVALID_RESPONSE");
  }
  return (t !== "public-key" || !wi(n, 1, 1024) || typeof i != "function") && C("WEBAUTHN_INVALID_RESPONSE"), Object.freeze({
    rawId: new Uint8Array(Reflect.apply(Ds, n, [0])),
    readExtensionResults() {
      try {
        return Reflect.apply(i, e, []);
      } catch {
        C("WEBAUTHN_PRF_REQUIRED");
      }
    }
  });
}
function Fs(e) {
  const t = tt(e.readExtensionResults(), ["prf"]), n = t ? tt(t.prf, ["enabled"]) : null;
  (!n || n.enabled !== !0) && C("WEBAUTHN_PRF_REQUIRED");
}
function Ws(e, t) {
  const n = Ii(e), i = n.rawId;
  Ms(i, t) || (P(i), C("WEBAUTHN_CREDENTIAL_MISMATCH")), P(i);
  const r = tt(n.readExtensionResults(), ["prf"]), s = r ? tt(r.prf, ["results"]) : null, o = (s ? tt(s.results, ["first"]) : null)?.first;
  wi(o, 32, 32) || C("WEBAUTHN_PRF_REQUIRED");
  const c = new Uint8Array(o), l = Reflect.apply(Ts, c, []);
  return P(c), l;
}
function qs({ challenge: e, credentialId: t, prfInput: n, rpId: i, signal: r }) {
  return {
    publicKey: {
      allowCredentials: [{
        id: t,
        type: "public-key"
      }],
      challenge: e,
      extensions: { prf: { eval: { first: n } } },
      ...i === void 0 ? {} : { rpId: i },
      timeout: bi,
      userVerification: "required"
    },
    signal: r
  };
}
async function kn({ assertCurrent: e, credentials: t, credentialId: n, fillRandom: i, prfInput: r, rpId: s, signal: a }) {
  const o = Ve(i, 32);
  let c;
  try {
    c = await t.get(qs({
      challenge: o,
      credentialId: n,
      prfInput: r,
      rpId: s,
      signal: a
    }));
  } catch (l) {
    throw e(), l;
  } finally {
    P(o);
  }
  return e(), Ws(c, n);
}
function zs(e) {
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
function Hs(e) {
  if (typeof e == "string")
    return zs(e) || C("INVALID_USERNAME"), yt.encode(e).length > 256 && C("INVALID_USERNAME"), e;
  (!mt(e) || xe(e) > 256) && C("INVALID_USERNAME");
  try {
    return Os.decode(e);
  } catch {
    C("INVALID_USERNAME");
  }
}
function Gt(e) {
  const t = Hs(e).replace(/^ +/u, "").replace(/ +$/u, "").replace(/[A-Z]/gu, (n) => n.toLowerCase());
  return di(t) || C("INVALID_USERNAME"), t;
}
function Ks(e) {
  let t;
  try {
    t = qn(e?.module);
  } catch {
    C("WASM_UNAVAILABLE");
  }
  return (!Ei(t, ["sdn", "sha256"]) || typeof t.sha256 != "function" || !t.sdn || typeof t.sdn != "object") && C("WASM_UNAVAILABLE"), t;
}
function Gs(e) {
  const t = Ks(e), n = t.sdn, i = e?.storage, r = e?.credentials, s = Rs(e?.rng), a = e?.createRequestController, o = e?.releaseRequestController, c = e?.ownHandle, l = e?.destroyHandle, d = e?.ownedHandlesClean, u = e?.now ?? (() => /* @__PURE__ */ new Date()), p = e?.rpHostname ?? e?.window?.location?.hostname ?? globalThis?.location?.hostname, f = Bs(e?.rpId, p), A = typeof e?.rpName == "string" && e.rpName !== "" ? e.rpName : Us, b = () => typeof r?.create == "function" && typeof r?.get == "function", h = () => pi(i), y = () => ns(i), m = (O) => is(i, O), $ = (O, F) => (rs(i, O, F), y().some((w) => w.key === O) && C("STORAGE_WRITE_FAILED"), !0), v = () => h().active.status === "valid", k = ({ confirmation: O } = {}) => (v() || C("REMEMBER_UNAVAILABLE"), ss(i, O), h().active.status !== "empty" && C("STORAGE_WRITE_FAILED"), !0), D = async (O) => {
    let F;
    try {
      F = await t.sha256(yt.encode(O));
    } catch {
      C("WASM_FAILURE");
    }
    return mt(F, 32) || C("WASM_FAILURE"), Vs(F);
  };
  return Object.freeze({
    canForget: v,
    deleteQuarantine: $,
    exportQuarantine: m,
    forget: k,
    inspect: h,
    listQuarantine: y,
    restore: async ({ assertCurrent: O } = {}) => {
      (typeof O != "function" || !b() || typeof n.importRememberedIdentity != "function" || typeof c != "function" || typeof l != "function" || typeof a != "function" || typeof o != "function") && C("REMEMBER_UNAVAILABLE");
      const F = h();
      (!F.canRestore || !F.active.record) && C("STORAGE_QUARANTINED");
      const w = F.active.record;
      Gt(w.canonicalUsername) !== w.canonicalUsername && C("INVALID_REMEMBERED_WALLET"), await D(w.canonicalUsername) !== w.aad.usernameSha256 && C("INVALID_REMEMBERED_WALLET"), O();
      const N = ge(w.credentialIdBase64url, {
        minimum: 1,
        maximum: 1024
      }), re = ge(w.prfInputBase64url, { exact: 32 }), Y = ge(w.ciphertextBase64url, {
        minimum: 17,
        maximum: 1024
      }), U = ge(w.hkdfSaltBase64url, { exact: 32 }), R = ge(w.nonceBase64url, { exact: 12 }), H = yt.encode(w.canonicalUsername), X = $e(w.aad), K = typeof a == "function" ? a() : null;
      (!K?.signal || typeof K.abort != "function") && (P(N), P(re), P(Y), P(U), P(R), P(H), C("WEBAUTHN_UNAVAILABLE"));
      let G, te = null, se = !1;
      try {
        G = await kn({
          assertCurrent: O,
          credentialId: N,
          credentials: r,
          fillRandom: s,
          prfInput: re,
          rpId: f,
          signal: K.signal
        }), O();
        let ne;
        const ce = G;
        try {
          ne = n.importRememberedIdentity({
            canonicalAad: X,
            canonicalUsernameUtf8: H,
            ciphertextAndTag: Y,
            hkdfSalt: U,
            nonce: R,
            prfOutput: ce
          });
        } finally {
          P(ce), G = null;
        }
        Lt(ne) || C("WASM_FAILURE"), te = ne.handle, (te == null || typeof c != "function") && C("WASM_FAILURE"), c(te);
        const J = ye(ne.identity);
        return O(), se = !0, Object.freeze({ handle: te, identity: J });
      } finally {
        !se && te !== null && typeof l == "function" && (l(te) || C("DESTRUCTION_FAILED")), P(G), P(N), P(re), P(Y), P(U), P(R), P(H), typeof o == "function" && o(K);
      }
    },
    setup: async ({ assertCurrent: O, canonicalUsername: F, handle: w, identity: z, passwordUtf8: N }) => {
      try {
        (typeof O != "function" || w === null || w === void 0 || !mt(N) || xe(N) === 0 || xe(N) > 256 || Gt(F) !== F || !b() || typeof n.sealRememberedIdentity != "function" || typeof n.importRememberedIdentity != "function" || typeof c != "function" || typeof l != "function" || typeof d != "function" || typeof a != "function" || typeof o != "function") && (P(N), C("REMEMBER_UNAVAILABLE"));
        const re = ye(z);
        h().canSetup || (P(N), C("STORAGE_QUARANTINED"));
        const U = typeof a == "function" ? a() : null;
        (!U?.signal || typeof U.abort != "function") && (P(N), C("WEBAUTHN_UNAVAILABLE"));
        let R, H, X, K, G, te, se, ne, ce, J = null, Re = null;
        try {
          R = Ve(s, 32), H = Ve(s, 32), X = Ve(s, 32), K = Ve(s, 32), G = Ve(s, 12), O();
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
                rp: f === void 0 ? { name: A } : { id: f, name: A },
                timeout: bi,
                user: {
                  displayName: F,
                  id: H,
                  name: F
                }
              },
              signal: U.signal
            });
          } catch (Te) {
            throw O(), Te;
          }
          O();
          const We = Ii(Ue);
          te = We.rawId, Fs(We), se = await kn({
            assertCurrent: O,
            credentialId: te,
            credentials: r,
            fillRandom: s,
            prfInput: X,
            rpId: f,
            signal: U.signal
          }), ne = se.slice(), ce = se.slice(), P(se), se = null;
          const Ie = await D(F);
          O();
          const je = Je(te), qe = Object.freeze({
            credentialIdBase64url: je,
            identityScheme: Ps,
            schemaVersion: 2,
            seedProfile: ks,
            storageProfile: Dn,
            usernameSha256: Ie
          }), ze = $e(qe);
          let ve;
          try {
            ve = n.sealRememberedIdentity(w, {
              canonicalAad: ze,
              hkdfSalt: K,
              nonce: G,
              passwordUtf8: N,
              prfOutput: ne
            });
          } finally {
            P(N), P(ne);
          }
          N = null, ne = null;
          const Oe = xe(ve);
          (!mt(ve) || Oe < 17 || Oe > 1024) && C("WASM_FAILURE");
          let He;
          try {
            const Te = u();
            He = Te instanceof Date ? Te.toISOString() : new Date(Te).toISOString();
          } catch {
            C("CLOCK_FAILURE");
          }
          const at = Object.freeze({
            aad: qe,
            canonicalUsername: F,
            ciphertextBase64url: Je(ve),
            createdAt: He,
            credentialIdBase64url: je,
            hkdfSaltBase64url: Je(K),
            nonceBase64url: Je(G),
            prfInputBase64url: Je(X),
            schemaVersion: 2,
            storageProfile: Dn
          });
          O(), Re = Zr(i, at);
          let Ne;
          const Ke = ce;
          try {
            Ne = n.importRememberedIdentity({
              canonicalAad: ze,
              canonicalUsernameUtf8: yt.encode(F),
              ciphertextAndTag: ve.slice(),
              hkdfSalt: K.slice(),
              nonce: G.slice(),
              prfOutput: Ke
            });
          } finally {
            P(Ke), ce = null;
          }
          Lt(Ne) || C("WASM_FAILURE"), J = Ne.handle, (J == null || typeof c != "function") && C("WASM_FAILURE"), c(J);
          const ct = ye(Ne.identity);
          return $s(re, ct) || C("IDENTITY_MISMATCH"), O(), (typeof l != "function" || !l(J)) && C("DESTRUCTION_FAILED"), J = null, (typeof d != "function" || !d(w)) && C("DESTRUCTION_FAILED"), O(), es(i, Re), Re = null, Object.freeze({ remembered: !0 });
        } finally {
          J !== null && typeof l == "function" && l(J), P(N), P(R), P(H), P(se), P(ne), P(ce), P(K), P(G), P(te), typeof o == "function" && o(U);
        }
      } finally {
        P(N);
      }
    },
    supported: b
  });
}
const nt = /^[0-9a-f]{64}$/u, Js = /^[A-Za-z0-9_-]{43}$/u, Qs = Object.freeze([
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
]), Ys = Object.freeze(["redirectUri", "schemaVersion", "transactionId"]), Xs = 64 * 1024, Zs = 4 * 1024, eo = new TextEncoder(), to = new TextDecoder("utf-8", { fatal: !0 });
class Rt extends Error {
  constructor() {
    super("RELAY_FAILURE"), this.name = "WalletRelayError", this.code = "RELAY_FAILURE";
  }
}
function x() {
  throw new Rt();
}
function vi(e) {
  if (e === null || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function Si(e, t) {
  vi(e) || x();
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
function Jt(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(Jt));
  if (!vi(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = Jt(e[n]);
  return Object.freeze(t);
}
function no(e, t) {
  (typeof e != "string" || eo.encode(e).byteLength > t || e.charCodeAt(0) === 65279) && x();
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
          const p = d.charCodeAt(u);
          if (p >= 55296 && p <= 56319) {
            const f = d.charCodeAt(u + 1);
            f >= 56320 && f <= 57343 || x(), u += 1;
          } else p >= 56320 && p <= 57343 && x();
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
      const p = /* @__PURE__ */ Object.create(null), f = /* @__PURE__ */ new Set();
      if (r(), e[n] === "}")
        return n += 1, p;
      for (; n < e.length; ) {
        r();
        const A = s();
        if (f.has(A) && x(), f.add(A), r(), e[n] !== ":" && x(), n += 1, p[A] = a(c + 1), r(), e[n] === "}")
          return n += 1, p;
        e[n] !== "," && x(), n += 1;
      }
      x();
    }
    if (l === "[") {
      n += 1;
      const p = [];
      if (r(), e[n] === "]")
        return n += 1, p;
      for (; n < e.length; ) {
        if (p.push(a(c + 1)), r(), e[n] === "]")
          return n += 1, p;
        e[n] !== "," && x(), n += 1;
      }
      x();
    }
    for (const [p, f] of [["true", !0], ["false", !1], ["null", null]])
      if (e.startsWith(p, n))
        return n += p.length, f;
    const d = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(e.slice(n));
    d || x(), n += d[0].length;
    const u = Number(d[0]);
    return Number.isFinite(u) || x(), u;
  };
  r();
  const o = a();
  return r(), n !== e.length && x(), o;
}
function Un(e) {
  try {
    Promise.resolve(e).catch(() => {
    });
  } catch {
  }
}
function Li(e, t = null) {
  try {
    const n = t?.cancel;
    typeof n == "function" && Un(Reflect.apply(n, t, []));
  } catch {
  }
  if (!t)
    try {
      const n = e?.body, i = n?.cancel;
      typeof i == "function" && Un(Reflect.apply(i, n, []));
    } catch {
    }
}
function Ri(e, t, n = null) {
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
async function io(e, t, n, i) {
  let r = null;
  try {
    (!e || e.status !== t || e.redirected === !0 || e.headers?.get?.("cache-control")?.trim().toLowerCase() !== "no-store" || e.headers?.get?.("content-type")?.trim().toLowerCase() !== "application/json; charset=utf-8" || typeof e.body?.getReader != "function") && x(), r = e.body.getReader(), (!r || typeof r.read != "function" || i?.aborted === !0) && x();
    const s = [];
    let a = 0;
    for (; ; ) {
      i?.aborted === !0 && x();
      const { done: l, value: d } = await Ri(r.read(), i);
      if (i?.aborted === !0 && x(), l) break;
      d instanceof Uint8Array || x(), a += d.byteLength, a > n && x(), s.push(d);
    }
    const o = new Uint8Array(a);
    let c = 0;
    for (const l of s)
      o.set(l, c), c += l.byteLength;
    return no(to.decode(o), n);
  } catch (s) {
    if (Li(e, r), s instanceof Rt) throw s;
    x();
  } finally {
    try {
      r?.releaseLock?.();
    } catch {
    }
  }
}
function jn(e, t) {
  return Si(e, Qs), (e.schemaVersion !== 1 || e.transactionId !== t || !nt.test(e.transactionId) || !nt.test(e.state) || !nt.test(e.requestSha256) || !Js.test(e.resultToken) || typeof e.callbackUri != "string" || typeof e.clientDisplayName != "string" || typeof e.clientId != "string" || typeof e.expiresAt != "string" || typeof e.operation != "string" || typeof e.registryVersion != "string" || typeof e.requestOrigin != "string") && x(), Jt(e);
}
function Qt(e, t) {
  Si(t, Ys), (t.schemaVersion !== 1 || t.transactionId !== e.transactionId || typeof t.redirectUri != "string") && x();
  const n = `${e.callbackUri}#code=`, i = `&state=${e.state}`;
  (!t.redirectUri.startsWith(n) || !t.redirectUri.endsWith(i)) && x();
  const r = t.redirectUri.slice(n.length, t.redirectUri.length - i.length);
  return (!nt.test(r) || t.redirectUri !== `${n}${r}${i}`) && x(), Object.freeze({
    redirectUri: t.redirectUri,
    schemaVersion: 1,
    transactionId: t.transactionId
  });
}
function Bn(e, t, n = void 0) {
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
function ro({ fetch: e, location: t }) {
  (typeof e != "function" || typeof t?.replace != "function") && x();
  const n = /* @__PURE__ */ new Set(), i = async (r, s, a, o) => {
    let c;
    try {
      c = await Ri(e(r, s), s.signal, (l) => Li(l));
    } catch {
      x();
    }
    return io(c, a, o, s.signal);
  };
  return Object.freeze({
    async fetchTransaction(r, { signal: s } = {}) {
      (typeof r != "string" || !nt.test(r)) && x();
      const a = await i(
        `/relay/v1/transactions/${r}`,
        Bn("GET", s),
        200,
        Xs
      );
      return jn(a, r);
    },
    async publishResult(r, s, { signal: a } = {}) {
      const o = jn(r, r?.transactionId), c = await i(
        `/relay/v1/transactions/${o.transactionId}/result`,
        Bn("POST", a, {
          result: s,
          resultToken: o.resultToken,
          schemaVersion: 1,
          transactionId: o.transactionId
        }),
        201,
        Zs
      ), l = Qt(o, c);
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
const Oi = Uint8Array, so = Oi.prototype.fill, Qe = new TextEncoder();
class V extends Error {
  constructor(t) {
    super(t), this.name = "WalletOriginError", this.code = t;
  }
}
function E(e) {
  throw new V(e);
}
function Ye(e) {
  throw e instanceof we ? new V(e.code) : e;
}
function pe(e) {
  if (e instanceof Oi)
    try {
      so.call(e, 0);
    } catch {
    }
}
function Xe(e) {
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
function Vn(e) {
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
function Ze(e, t) {
  const n = /* @__PURE__ */ new Set();
  for (const i of t) {
    let r = null;
    try {
      r = i?.form ?? i?.closest?.("form") ?? null;
    } catch {
      r = null;
    }
    r && n.add(r), Vn(i);
  }
  for (const i of n) {
    let r = [];
    try {
      r = i.querySelectorAll?.("input, textarea") ?? [];
    } catch {
      r = [];
    }
    for (const s of r) Vn(s);
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
function oo(e) {
  let t;
  try {
    t = qn(e);
  } catch {
    E("WASM_UNAVAILABLE");
  }
  const n = t?.sdn, i = t?.sha256;
  return (!n || typeof n.derivePasswordIdentity != "function" || typeof n.destroySdnIdentity != "function" || typeof i != "function") && E("WASM_UNAVAILABLE"), Object.freeze({ capabilities: n, sha256: i });
}
function ut(e) {
  const t = e?.AbortController ?? globalThis.AbortController;
  return typeof t == "function" ? new t() : null;
}
class ao {
  #d = !1;
  #p;
  #y = null;
  #a = /* @__PURE__ */ new Set();
  #f = /* @__PURE__ */ new Set();
  #v = null;
  #e = !1;
  #n;
  #r = 0;
  #t = null;
  #h = null;
  #S = [];
  #L = /* @__PURE__ */ new Set();
  #o = /* @__PURE__ */ new Set();
  #g = null;
  #C = null;
  #D = /* @__PURE__ */ new WeakSet();
  #P = /* @__PURE__ */ new WeakMap();
  #u = null;
  #_;
  #m;
  #c;
  #V = !1;
  #A = null;
  #i = !1;
  #R = 0;
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
    const p = oo(d);
    this.#p = p.capabilities, this.#x = p.sha256, this.#_ = r, this.#c = s, this.#n = n, this.#s = u, this.#m = Gs({
      createRequestController: () => {
        const f = ut(this.#s);
        return f && (this.#a.add(f), this.#O.add(f)), f;
      },
      credentials: t ?? u?.navigator?.credentials ?? globalThis.navigator?.credentials,
      rpHostname: u?.location?.hostname ?? globalThis?.location?.hostname,
      rpId: o,
      rpName: c,
      destroyHandle: (f) => this.#b(f),
      module: d,
      now: i,
      ownHandle: (f) => this.#o.add(f),
      ownedHandlesClean: (f) => this.#o.size === 1 && this.#o.has(f) && this.#t === f,
      releaseRequestController: (f) => {
        this.#O.delete(f), this.#a.delete(f);
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
  #b(t = this.#t) {
    if (t == null) return !0;
    if (this.#o.add(t), this.#L.has(t)) return !1;
    this.#L.add(t);
    let n = !1;
    try {
      this.#p.destroySdnIdentity(t), n = !0;
    } catch {
      n = !1;
    } finally {
      this.#L.delete(t);
    }
    return n ? (this.#o.delete(t), this.#t === t && (this.#t = null, this.#h = null), !0) : !1;
  }
  #E() {
    let t = !0;
    for (const n of [...this.#o])
      this.#b(n) || (t = !1);
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
  #q() {
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
    for (const n of t) pe(n);
  }
  #N(t = []) {
    if (this.#f.size === 0) return;
    const n = new Set(t), i = [];
    for (const r of this.#f)
      n.has(r) || i.push(r);
    this.#f = new Set(
      [...this.#f].filter((r) => n.has(r))
    ), Ze(this.#n, i);
  }
  #j(t = []) {
    (this.#e || this.#i) && E("STALE_CONTROLLER");
    const n = this.#k();
    this.#N(t), this.#q(), this.#U();
    let i = this.#b();
    return i && (i = this.#E() && this.#o.size === 0), i || (this.revokeNow("native-destruction-failed"), this.#E(), E("DESTRUCTION_FAILED")), this.#A = null, n;
  }
  #B(t, n, {
    allowPublication: i = !1,
    retainIdentity: r = !1
  } = {}) {
    const s = !this.#e && !this.#i && this.#r === n && this.#t === t, a = this.#t === t || this.#o.has(t);
    if (!s) {
      const p = !a || this.#b(t);
      return p || (this.revokeNow("native-destruction-failed"), this.#E()), Object.freeze({ destroyed: p, permit: null });
    }
    const o = this.#R, c = i ? Object.freeze({}) : null;
    this.#k(), this.#i = !0, this.#d = !1, this.#u = c, this.#o.add(t), this.#t = null, this.#h = null, r || (this.#A = null);
    const l = this.#y;
    this.#y = null;
    let d = this.#b(t);
    d && (d = this.#E() && this.#o.size === 0), d || (this.#u = null, this.#A = null);
    try {
      l?.cancel("STALE_CONTROLLER");
    } catch {
    }
    try {
      l?.destroy();
    } catch {
    }
    this.#N(), this.#U(), this.#M(), d || this.#E();
    const u = d && this.#R === o && this.#u === c ? c : null;
    return u === null && this.#u === c && (this.#u = null), Object.freeze({ destroyed: d, permit: u });
  }
  #w(t, n) {
    (this.#e || this.#i || this.#r !== n || this.#t !== t) && E("STALE_CONTROLLER");
  }
  #T(t) {
    (this.#e || this.#i || this.#r !== t) && E("STALE_CONTROLLER");
  }
  #I(t, n) {
    (this.#e || this.#R !== t || this.#o.size !== 0 || n === null || this.#u !== n) && E("STALE_CONTROLLER");
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
      const a = ut(this.#s);
      a && this.#a.add(a);
      try {
        this.#I(n.epoch, n.permit), le({ document: this.#n, window: this.#s }), typeof this.#c?.publishResult != "function" && E("RELAY_UNAVAILABLE");
        const o = t === "connected" ? n.connectedResult : $r();
        let c;
        try {
          c = await this.#c.publishResult(n.transaction, o, {
            signal: a?.signal
          });
        } catch (l) {
          throw this.#I(n.epoch, n.permit), l;
        }
        if (this.#I(n.epoch, n.permit), le({ document: this.#n, window: this.#s }), typeof this.#c?.navigate == "function") {
          const l = Qt(n.transaction, c);
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
    (this.#e || this.#i) && E("STALE_CONTROLLER"), (!t || !n) && E("INVALID_CREDENTIAL_FORM"), this.#f.add(t), this.#f.add(n);
  }
  copyPublicIdentity() {
    const t = this.#h ?? this.#A;
    t || E("STALE_CONTROLLER");
    try {
      return ye(t);
    } catch {
      E("WASM_FAILURE");
    }
  }
  /**
   * The account key's proof that `sessionPublicKeyHex` (the page's Ed25519
   * module-delivery key) may fetch paid modules for this account, for this
   * page's origin, until `expiresAt` (unix seconds). Shaped as an EPM
   * ChainProof; the key server checks it before an allowlisted grant.
   */
  signModuleDeliveryKey({ sessionPublicKeyHex: t, expiresAt: n }) {
    return (this.#e || this.#i || this.#t === null) && E("STALE_CONTROLLER"), typeof this.#p.signModuleDeliveryKey != "function" && E("OPERATION_NOT_ALLOWED"), this.#p.signModuleDeliveryKey(
      this.#t,
      {
        protocolVersion: 1,
        origin: String(this.#s?.location?.origin ?? ""),
        sessionPublicKeyHex: t,
        expiresAt: n
      },
      "spaceaware-module-delivery-key-v1"
    );
  }
  supportsRememberedWallet() {
    return !this.#e && !this.#i && this.#m.supported();
  }
  canRestoreRememberedWallet() {
    if (!this.supportsRememberedWallet()) return !1;
    try {
      return this.#m.inspect().canRestore === !0;
    } catch {
      return !1;
    }
  }
  isUiGenerationCurrent(t) {
    return Number.isSafeInteger(t) && t === this.#r && !this.#e && (!this.#i || this.#g !== null);
  }
  listQuarantinedWalletRecords() {
    (this.#e || this.#i && !this.#g) && E("STALE_CONTROLLER");
    try {
      return this.#m.listQuarantine();
    } catch (t) {
      throw typeof t?.code == "string" ? new V(t.code) : t;
    }
  }
  exportQuarantinedWalletRecord(t) {
    (this.#e || this.#i && !this.#g) && E("STALE_CONTROLLER");
    try {
      return this.#m.exportQuarantine(t);
    } catch (n) {
      throw typeof n?.code == "string" ? new V(n.code) : n;
    }
  }
  deleteQuarantinedWalletRecord(t, n) {
    (this.#e || this.#i && !this.#g) && E("STALE_CONTROLLER");
    try {
      return this.#m.deleteQuarantine(t, n);
    } catch (i) {
      throw typeof i?.code == "string" ? new V(i.code) : i;
    }
  }
  canForgetRememberedWallet() {
    if (this.#e || !this.#A || !this.#g) return !1;
    try {
      return this.#m.canForget() === !0;
    } catch {
      return !1;
    }
  }
  forgetRememberedWallet(t) {
    this.canForgetRememberedWallet() || E("REMEMBER_UNAVAILABLE");
    try {
      return this.#m.forget({ confirmation: t });
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
    (this.#e || this.#i) && E("STALE_CONTROLLER"), t !== 0 && E("INVALID_ACCOUNT"), (!s || !n) && E("INVALID_CREDENTIAL_FORM");
    try {
      le({ document: this.#n, window: this.#s });
    } catch (h) {
      Ze(
        this.#n,
        [s, n, i].filter(Boolean)
      ), Ye(h);
    }
    let a;
    try {
      a = this.#j([s, n]);
    } catch (h) {
      throw Ze(
        this.#n,
        [s, n, i].filter(Boolean)
      ), h;
    }
    this.registerCredentialControls({ passwordControl: n, usernameControl: s });
    let o, c, l, d, u, p, f = null, A = !1;
    try {
      try {
        le({ document: this.#n, window: this.#s });
      } catch (y) {
        Ye(y);
      }
      this.#T(a);
      const h = s?.value;
      f = n?.value, Xe(h) || E("INVALID_USERNAME"), Xe(f) || E("INVALID_PASSWORD");
      try {
        o = Gt(h);
      } catch {
        E("INVALID_USERNAME");
      }
      c = Qe.encode(h), p = Qe.encode(f), A = i?.checked === !0 && i?.disabled !== !0 && this.#m.supported(), A ? ((p.length === 0 || p.length > 256) && E("INVALID_PASSWORD"), l = p.slice(), d = p.slice(), u = d, pe(p), p = null) : (l = p, p = null), this.#l.add(c), this.#l.add(l), d && this.#l.add(d);
    } finally {
      if (f = null, i && typeof i == "object") {
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
      pe(p), this.#N();
    }
    let b;
    try {
      b = await this.#p.derivePasswordIdentity({
        accountIndex: t,
        passwordUtf8: l,
        usernameUtf8: c
      }), (!b || typeof b != "object") && E("WASM_FAILURE");
      const h = b.handle;
      h == null && E("WASM_FAILURE"), this.#o.add(h);
      let y;
      try {
        y = ye(b.identity);
      } catch {
        const m = this.#b(h);
        b = null, m || (this.revokeNow("native-destruction-failed"), E("DESTRUCTION_FAILED")), E("WASM_FAILURE");
      }
      if ((this.#e || this.#i || this.#r !== a) && (this.#b(h) || (this.revokeNow("native-destruction-failed"), E("DESTRUCTION_FAILED")), E("STALE_CONTROLLER")), this.#t = h, this.#h = y, A)
        try {
          await this.#m.setup({
            assertCurrent: () => this.#w(h, a),
            canonicalUsername: o,
            handle: h,
            identity: y,
            passwordUtf8: d
          }), this.#w(h, a), this.#l.delete(u), d = null;
          try {
            r && (r.textContent = "Wallet remembered on this device.");
          } catch {
          }
        } catch {
          pe(d), this.#l.delete(u), d = null, (this.#e || this.#i || this.#r !== a || this.#t !== h) && E("STALE_CONTROLLER"), this.#o.size === 1 && this.#o.has(h) && this.#t === h || (this.revokeNow("remembered-wallet-cleanup-failed"), E("DESTRUCTION_FAILED"));
          try {
            r && (r.textContent = "Wallet was not remembered.");
          } catch {
          }
        }
      return this.#w(h, a), y;
    } catch (h) {
      throw h instanceof V && h.code === "DESTRUCTION_FAILED" || (this.#e || this.#i || this.#r !== a) && E("STALE_CONTROLLER"), h;
    } finally {
      pe(c), pe(l), pe(d), pe(u), this.#l.delete(c), this.#l.delete(l), this.#l.delete(u);
    }
  }
  async unlockRemembered({ accountIndex: t = 0 } = {}) {
    (this.#e || this.#i) && E("STALE_CONTROLLER"), t !== 0 && E("INVALID_ACCOUNT"), this.canRestoreRememberedWallet() || E("REMEMBER_UNAVAILABLE");
    try {
      le({ document: this.#n, window: this.#s });
    } catch (i) {
      Ye(i);
    }
    const n = this.#j();
    try {
      const i = await this.#m.restore({
        assertCurrent: () => this.#T(n)
      }), r = i?.handle;
      (r == null || !this.#o.has(r)) && E("WASM_FAILURE");
      let s;
      try {
        s = ye(i.identity);
      } catch {
        E("WASM_FAILURE");
      }
      return (this.#e || this.#i || this.#r !== n) && (this.#b(r) || (this.revokeNow("native-destruction-failed"), E("DESTRUCTION_FAILED")), E("STALE_CONTROLLER")), this.#t = r, this.#h = s, s;
    } catch (i) {
      throw i instanceof V && i.code === "DESTRUCTION_FAILED" || ((this.#e || this.#i || this.#r !== n) && E("STALE_CONTROLLER"), this.#o.size > 0 && (this.#E(), this.#o.size > 0 && (this.revokeNow("remembered-wallet-cleanup-failed"), E("DESTRUCTION_FAILED"))), i instanceof V) ? i : typeof i?.code == "string" ? new V(i.code) : i;
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
    (this.#e || this.#i) && E("STALE_CONTROLLER"), i !== "sdn.auth.raw-challenge.v1" && E("OPERATION_NOT_ALLOWED"), t !== 0 && E("INVALID_ACCOUNT");
    const o = s === "password-fast-v1-legacy";
    !o && !(s === "bip39-mnemonic-v1-legacy") && E("INVALID_LEGACY_PROFILE"), o ? (!a || !r) && E("INVALID_CREDENTIAL_FORM") : (!n || typeof n != "object") && E("INVALID_CREDENTIAL_FORM");
    const l = o ? [a, r] : [n];
    try {
      le({ document: this.#n, window: this.#s });
    } catch (b) {
      Ze(this.#n, l), Ye(b);
    }
    let d;
    try {
      d = this.#j(l);
    } catch (b) {
      throw Ze(this.#n, l), b;
    }
    o ? this.registerCredentialControls({ passwordControl: r, usernameControl: a }) : this.#f.add(n);
    let u, p, f;
    try {
      try {
        le({ document: this.#n, window: this.#s });
      } catch (b) {
        Ye(b);
      }
      if (this.#T(d), o) {
        const b = a?.value, h = r?.value;
        Xe(b) || E("INVALID_USERNAME"), Xe(h) || E("INVALID_PASSWORD"), u = Qe.encode(b), p = Qe.encode(h), this.#l.add(u), this.#l.add(p);
      } else {
        const b = n?.value;
        Xe(b) || E("INVALID_MNEMONIC"), f = Qe.encode(b), this.#l.add(f);
      }
    } finally {
      this.#N();
    }
    let A;
    try {
      A = o ? await this.#p.deriveLegacyPasswordIdentity({
        accountIndex: t,
        passwordUtf8: p,
        usernameUtf8: u
      }) : await this.#p.importLegacyMnemonicIdentity({ accountIndex: t, mnemonicUtf8: f }), (!A || typeof A != "object") && E("WASM_FAILURE");
      const b = A.handle;
      b == null && E("WASM_FAILURE"), this.#o.add(b);
      let h;
      try {
        h = us(A.identity, { accountIndex: t, profile: s });
      } catch {
        const y = this.#b(b);
        A = null, y || (this.revokeNow("native-destruction-failed"), E("DESTRUCTION_FAILED")), E("WASM_FAILURE");
      }
      return (this.#e || this.#i || this.#r !== d) && (this.#b(b) || (this.revokeNow("native-destruction-failed"), E("DESTRUCTION_FAILED")), E("STALE_CONTROLLER")), this.#t = b, this.#h = h, h;
    } catch (b) {
      throw b instanceof V && b.code === "DESTRUCTION_FAILED" || (this.#e || this.#i || this.#r !== d) && E("STALE_CONTROLLER"), b;
    } finally {
      pe(u), pe(p), pe(f), this.#l.delete(u), this.#l.delete(p), this.#l.delete(f);
    }
  }
  async prepare(t) {
    (this.#e || this.#i) && E("STALE_CONTROLLER"), this.#d && E("TRANSACTION_IN_PROGRESS"), this.#d = !0;
    const n = this.#r, i = typeof t == "string" ? t : t?.transactionId ?? null, r = ut(this.#s);
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
      return this.#T(n), le({ document: this.#n, window: this.#s }), this.#D.add(a), r && this.#P.set(a, r), a;
    } catch (s) {
      const a = this.#e || this.#i || this.#r !== n;
      throw this.#d = !1, r && this.#a.delete(r), a && E("STALE_CONTROLLER"), s instanceof V ? s : s instanceof we ? new V(s.code) : s;
    }
  }
  async executePrepared(t) {
    (!t || typeof t != "object" || !this.#D.has(t)) && E("INVALID_TRANSACTION"), this.#D.delete(t);
    const n = this.#P.get(t) ?? null;
    this.#P.delete(t), (this.#e || this.#i || this.#t === null) && (n && this.#a.delete(n), this.#d = !1, E("STALE_CONTROLLER"));
    const i = this.#t, r = this.#h, s = this.#r;
    let a = !1;
    try {
      this.#w(i, s);
      let o = await kt(t.transaction, {
        expectedTransactionId: t.transaction.transactionId,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      });
      this.#w(i, s), le({ document: this.#n, window: this.#s }), this.#y = Vr({
        binding: o.binding,
        document: this.#n,
        identity: r,
        request: o.request,
        transaction: o.transaction
      }), await this.#y.promise, this.#w(i, s), le({ document: this.#n, window: this.#s }), o = await kt(t.transaction, {
        expectedTransactionId: t.transaction.transactionId,
        registry: this.#_,
        relay: this.#c,
        sha256: this.#x,
        window: this.#s
      }), this.#w(i, s), le({ document: this.#n, window: this.#s });
      const c = await Mr({
        assertCurrent: () => this.#w(i, s),
        binding: o.binding,
        handle: i,
        identity: r,
        transaction: o.transaction,
        wasm: this.#p
      });
      this.#w(i, s), le({ document: this.#n, window: this.#s });
      const l = o.transaction.operation === "sdn.wallet.account.v1";
      l && (this.#A = ye(r));
      const d = this.#B(i, s, {
        allowPublication: !0,
        retainIdentity: l
      });
      a = !0, d.destroyed || (this.#A = null, E("DESTRUCTION_FAILED"));
      const u = this.#R, p = d.permit;
      if (l)
        return this.#I(u, p), this.#g = Object.freeze({
          connectedResult: c,
          epoch: u,
          permit: p,
          transaction: o.transaction
        }), Object.freeze({ accountReady: !0 });
      try {
        this.#I(u, p), typeof this.#c?.publishResult != "function" && E("RELAY_UNAVAILABLE");
        const f = ut(this.#s);
        f && this.#a.add(f);
        let A;
        try {
          try {
            A = await this.#c.publishResult(o.transaction, c, {
              signal: f?.signal
            });
          } catch (b) {
            throw this.#I(u, p), b;
          }
        } finally {
          f && this.#a.delete(f);
        }
        if (this.#I(u, p), le({ document: this.#n, window: this.#s }), typeof this.#c?.navigate == "function") {
          const b = Qt(o.transaction, A);
          this.#I(u, p), this.#u = null, this.#c.navigate(b.redirectUri);
        } else
          this.#u = null;
        return A;
      } finally {
        this.#u === p && (this.#u = null);
      }
    } catch (o) {
      throw !a && (this.#r !== s || this.#t !== i) && E("STALE_CONTROLLER"), o instanceof V ? o : o instanceof we ? new V(o.code) : o;
    } finally {
      n && this.#a.delete(n), a || this.#B(i, s);
    }
  }
  async execute(t) {
    (this.#e || this.#i || this.#t === null) && E("STALE_CONTROLLER");
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
    if (this.#R = this.#R >= Number.MAX_SAFE_INTEGER ? 1 : this.#R + 1, this.#u = null, this.#g = null, this.#e) {
      this.#E();
      return;
    }
    !this.#i && (this.#i = !0, this.#k()), this.#d = !1, this.#A = null, this.#t !== null && this.#t !== void 0 && this.#o.add(this.#t), this.#t = null, this.#h = null;
    const i = this.#y;
    this.#y = null;
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
    if (this.#N(), this.#U(), this.#M(), this.#E(), r)
      try {
        this.#c?.revokeNow?.(t);
      } catch {
      }
  }
  destroy(t = "destroy") {
    if (this.#v)
      return this.#E(), this.#v;
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
const co = [
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
      "sdn.auth.publish-request.v1",
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
        operation: "sdn.auth.publish-request.v1",
        registryRow: "spaceaware-publish-request-v1",
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
], lo = "7674bfb019753eee8387f064467c0f6f2ff6babd1ecabbd70dc585afc31fbc1a", uo = 1, Ni = {
  clients: co,
  registryReleaseSha256: lo,
  schemaVersion: uo
}, fo = new TextEncoder(), po = /^[0-9a-f]{64}$/u, ho = /^[a-z0-9]+(?:-[a-z0-9]+)*$/u, yo = Object.freeze({
  "sdn.auth.publish-request.v1": Object.freeze({
    audience: null,
    registryRow: "spaceaware-publish-request-v1",
    serviceActivationState: null,
    serviceInstance: null
  }),
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
}), me = Object.freeze([
  "sdn.wallet.account.v1",
  "sdn.wallet.connect.v1"
]), mo = Object.freeze([
  "sdn.auth.jcs-envelope.v2",
  "sdn.auth.raw-challenge.v1",
  ...me
]), go = Object.freeze(["sdn.auth.publish-request.v1", ...me]), Ao = Object.freeze([
  "sdn.asset-review.authority-activation.v1",
  "sdn.asset-review.decision.v1",
  ...me
]), Mn = Object.freeze([
  Object.freeze(["sdn-landing-web-v1", "Space Data Network", "https://spacedatanetwork.org", "https://spacedatanetwork.org/wallet-callback.html", me]),
  Object.freeze(["sdn-standards-web-v1", "Space Data Standards", "https://spacedatastandards.org", "https://spacedatastandards.org/wallet-callback.html", me]),
  Object.freeze(["sdn-flatbuffers-pages-v1", "FlatBuffers Documentation", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/flatbuffers/wallet-callback.html", me]),
  Object.freeze(["sdn-flatsql-pages-v1", "FlatSQL Documentation", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/flatsql/wallet-callback.html", me]),
  Object.freeze(["sdn-module-sdk-pages-v1", "Space Data Module SDK", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/space-data-module-sdk/wallet-callback.html", me]),
  Object.freeze(["spaceaware-web-v1", "SpaceAware", "https://spaceaware.io", "https://spaceaware.io/wallet/callback", go]),
  Object.freeze(["sdn-node-console-v1", "SDN Node Console", "https://sdn.spaceaware.io", "https://sdn.spaceaware.io/wallet/callback", mo]),
  Object.freeze(["orbpro-pages-v1", "OrbPro", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/OrbPro/wallet-callback.html", me]),
  Object.freeze(["sdn-asset-models-pages-v1", "SDN Asset Models", "https://digitalarsenal.github.io", "https://digitalarsenal.github.io/asset-models/wallet-callback.html", me]),
  Object.freeze(["sdn-asset-review-v1", "SDN Asset Review", "https://review.spacedatanetwork.org", "https://review.spacedatanetwork.org/wallet/callback", Ao])
]), bo = Object.freeze([
  Object.freeze({
    audience: null,
    clientId: "spaceaware-web-v1",
    maxLifetimeSeconds: 300,
    operation: "sdn.auth.publish-request.v1",
    requestOrigin: "https://spaceaware.io",
    registryRow: "spaceaware-publish-request-v1",
    serviceActivationState: null,
    serviceInstance: null
  }),
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
function B(e) {
  throw new TypeError(e);
}
function fn(e) {
  if (!e || typeof e != "object" || Array.isArray(e)) return !1;
  const t = Object.getPrototypeOf(e);
  return t === Object.prototype || t === null;
}
function gt(e, t, n) {
  fn(e) || B(`${n} must be an object`);
  const i = Reflect.ownKeys(e);
  i.some((a) => typeof a != "string") && B(`${n} has missing or unknown fields`);
  const r = i.sort(), s = [...t].sort();
  (r.length !== s.length || r.some((a, o) => a !== s[o])) && B(`${n} has missing or unknown fields`);
  for (const a of r) {
    const o = Object.getOwnPropertyDescriptor(e, a);
    (!o || !o.enumerable || !("value" in o) || o.value === void 0) && B(`${n} has an invalid field`);
  }
  return e;
}
function he(e) {
  return e === null || typeof e == "boolean" || typeof e == "string" ? JSON.stringify(e) : typeof e == "number" ? (Number.isFinite(e) || B("registry contains a non-finite number"), JSON.stringify(e)) : Array.isArray(e) ? `[${e.map(he).join(",")}]` : (fn(e) || B("registry contains an unsupported value"), `{${Object.keys(e).sort().map((t) => `${JSON.stringify(t)}:${he(e[t])}`).join(",")}}`);
}
async function Eo(e) {
  const t = globalThis.crypto?.subtle;
  t || B("Web Crypto SHA-256 is unavailable");
  const n = new Uint8Array(await t.digest("SHA-256", fo.encode(e)));
  return Array.from(n, (i) => i.toString(16).padStart(2, "0")).join("");
}
function Ot(e) {
  if (Array.isArray(e)) return Object.freeze(e.map(Ot));
  if (!fn(e)) return e;
  const t = {};
  for (const n of Object.keys(e).sort()) t[n] = Ot(e[n]);
  return Object.freeze(t);
}
function wo(e, t) {
  (typeof e != "string" || !e.startsWith("https://") || e.includes("*")) && B(`${t} must be exact HTTPS`);
  let n;
  try {
    n = new URL(e);
  } catch {
    B(`${t} must be a valid URL`);
  }
  (n.protocol !== "https:" || n.username || n.password || n.port || n.origin !== e || n.pathname !== "/" || n.search || n.hash) && B(`${t} must be an exact HTTPS origin`);
}
function Io(e, t) {
  (typeof e != "string" || !e.startsWith("https://") || e.includes("*")) && B("callbackUri must be exact HTTPS");
  let n;
  try {
    n = new URL(e);
  } catch {
    B("callbackUri must be a valid URL");
  }
  (n.protocol !== "https:" || n.username || n.password || n.port || n.origin !== t || n.search || n.hash || n.href !== e) && B("callbackUri must be exact and same-origin");
}
function vo(e) {
  gt(e, ["clients", "registryReleaseSha256", "schemaVersion"], "registry"), e.schemaVersion !== 1 && B("registry schemaVersion must be 1"), (typeof e.registryReleaseSha256 != "string" || !po.test(e.registryReleaseSha256)) && B("registryReleaseSha256 must be lowercase SHA-256"), (!Array.isArray(e.clients) || e.clients.length !== Mn.length) && B("registry must contain exactly the reviewed clients");
  const t = /* @__PURE__ */ new Set(), n = /* @__PURE__ */ new Set();
  e.clients.forEach((i, r) => {
    gt(i, [
      "allowedOperations",
      "audiences",
      "callbackUri",
      "clientDisplayName",
      "clientId",
      "operationBindings",
      "requestOrigin"
    ], `client ${r}`);
    const [s, a, o, c, l] = Mn[r];
    (i.clientId !== s || i.clientDisplayName !== a || i.requestOrigin !== o || i.callbackUri !== c) && B(`client ${r} differs from the reviewed row`), (!ho.test(i.clientId) || t.has(i.clientId)) && B("clientId must be unique and canonical"), t.add(i.clientId), (typeof i.clientDisplayName != "string" || i.clientDisplayName.length < 1 || i.clientDisplayName.length > 80) && B("clientDisplayName is invalid"), wo(i.requestOrigin, "requestOrigin"), Io(i.callbackUri, i.requestOrigin), (!Array.isArray(i.allowedOperations) || he(i.allowedOperations) !== he(l)) && B(`${i.clientId} allowedOperations differ from the reviewed allowlist`), (!Array.isArray(i.operationBindings) || i.operationBindings.length !== l.length) && B(`${i.clientId} operationBindings differ from the reviewed allowlist`), i.operationBindings.forEach((u, p) => {
      gt(u, [
        "audience",
        "maxLifetimeSeconds",
        "operation",
        "registryRow",
        "serviceActivationState",
        "serviceInstance"
      ], `${i.clientId} operation ${p}`);
      const f = l[p];
      u.operation !== f && B(`${i.clientId} operation allowlist order or value changed`);
      const A = yo[u.operation];
      A || B("unknown registry operation"), (u.maxLifetimeSeconds !== 300 || u.audience !== A.audience || u.registryRow !== A.registryRow || u.serviceActivationState !== A.serviceActivationState || u.serviceInstance !== A.serviceInstance) && B(`${i.clientId} operation policy differs from the reviewed binding`);
      const b = `${i.clientId}\0${i.requestOrigin}\0${u.operation}`;
      n.has(b) && B("duplicate registry binding"), n.add(b);
    });
    const d = [...new Set(i.operationBindings.map(({ audience: u }) => u).filter((u) => u !== null))].sort();
    (!Array.isArray(i.audiences) || he(i.audiences) !== he(d)) && B(`${i.clientId} audiences differ from operationBindings`), he(i.allowedOperations) !== he(i.operationBindings.map(({ operation: u }) => u)) && B(`${i.clientId} allowedOperations differ from operationBindings`);
  });
  for (const i of bo) {
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
    (!a || he(a) !== he(i)) && B(`registry projection drifted from compiled row ${i.registryRow}`);
  }
  return Ot(e);
}
const { registryReleaseSha256: So, ...Lo } = Ni, Ro = await Eo(he(Lo));
Ro !== So && B("registry release SHA-256 mismatch");
const $n = vo(Ni);
function Oo(e) {
  const t = gt(e, ["clientId", "operation", "requestOrigin"], "registry lookup");
  (typeof t.clientId != "string" || typeof t.requestOrigin != "string" || typeof t.operation != "string") && B("registry lookup fields must be strings");
  const n = $n.clients.find((r) => r.clientId === t.clientId && r.requestOrigin === t.requestOrigin), i = n?.operationBindings.find((r) => r.operation === t.operation);
  return (!n || !i) && B("no exact registry binding exists"), Ot({
    audience: i.audience,
    callbackUri: n.callbackUri,
    clientDisplayName: n.clientDisplayName,
    clientId: n.clientId,
    maxLifetimeSeconds: i.maxLifetimeSeconds,
    operation: i.operation,
    requestOrigin: n.requestOrigin,
    registryReleaseSha256: $n.registryReleaseSha256,
    registryRow: i.registryRow,
    serviceActivationState: i.serviceActivationState,
    serviceInstance: i.serviceInstance
  });
}
const No = /^\/transaction\/([0-9a-f]{64})$/u, jt = new TextEncoder(), To = Uint8Array.prototype.fill, Co = Object.freeze({ resolveRegistryBinding: Oo });
function Fn(e) {
  if (e instanceof Uint8Array)
    try {
      To.call(e, 0);
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
function Wn() {
  return new V("INVALID_TRANSACTION");
}
function it(e, t, n, i) {
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
function Ti({
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
  const d = (h = o) => !a && h === o && t.isUiGenerationCurrent?.(s) === !0, u = (h, y = o) => {
    d(y) && l?.status && (l.status.textContent = h);
  }, p = () => {
    o += 1;
    for (const [h, y, m] of c)
      try {
        h.removeEventListener?.(y, m);
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
  }, f = (h, y, m) => {
    h.addEventListener?.(y, m), c.push([h, y, m]);
  }, A = () => {
    if (p(), a || t.isUiGenerationCurrent?.(s) !== !0)
      return r.replaceChildren(), r.hidden = !0, !1;
    let h;
    try {
      h = t.listQuarantinedWalletRecords();
    } catch {
      h = [];
    }
    if (!Array.isArray(h) || h.length === 0)
      return r.replaceChildren(), r.hidden = !0, !1;
    r.hidden = !1, l = ys(r, h, { document: n });
    const y = o;
    for (const m of l.rows) {
      const { entry: $ } = m, v = async (q) => {
        if (q?.isTrusted !== !0 || !d(y)) return;
        if ($.exportable !== !0) {
          u("Quarantined record is too large to export.", y);
          return;
        }
        let O;
        try {
          if (O = t.exportQuarantinedWalletRecord($.key), typeof e?.writeText != "function") throw new Error("clipboard unavailable");
          await e.writeText(O), u("Quarantined record exported to the clipboard.", y);
        } catch {
          u("Quarantined record export failed.", y);
        } finally {
          O = null;
        }
      }, k = (q) => {
        if (!(q?.isTrusted !== !0 || !d(y))) {
          try {
            m.confirmation.value = "";
          } catch {
          }
          try {
            m.confirmation.defaultValue = "";
          } catch {
          }
          m.confirmationGroup.hidden = !1, u(`Type ${$.key} to confirm deletion.`, y);
          try {
            m.confirmation.focus?.();
          } catch {
          }
        }
      }, D = (q) => {
        if (!(q?.isTrusted !== !0 || !d(y))) {
          try {
            m.confirmation.value = "";
          } catch {
          }
          try {
            m.confirmation.defaultValue = "";
          } catch {
          }
          m.confirmationGroup.hidden = !0, u("", y);
        }
      }, L = (q) => {
        if (q?.isTrusted !== !0 || !d(y)) return;
        const O = m.confirmation.value;
        if (O !== $.key) {
          u("Type the exact storage key to confirm deletion.", y);
          return;
        }
        try {
          t.deleteQuarantinedWalletRecord($.key, O);
        } catch {
          u("Quarantined record deletion failed.", y);
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
        A(), a || i();
      };
      f(m.exportButton, "click", v), f(m.deleteButton, "click", k), f(m.cancel, "click", D), f(m.confirm, "click", L);
    }
    return !0;
  }, b = A();
  return Object.freeze({
    container: r,
    destroy() {
      a || (a = !0, p(), r.replaceChildren(), r.remove?.());
    },
    hasEntries: b,
    refresh: A
  });
}
function _o({
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
  const p = n.createElement("input");
  p.type = "password", p.name = "password", p.autocomplete = "current-password", p.required = !0, it(n, d, "Username", u), it(n, d, "Password", p);
  let f = null;
  if (t && typeof t == "object") {
    f = n.createElement("input"), f.type = "checkbox", f.dataset.walletRemember = "prf-only", f.checked = !1, f.defaultChecked = !1;
    let R = !1;
    try {
      R = t.supportsRememberedWallet?.() === !0;
    } catch {
      R = !1;
    }
    f.disabled = !R, it(n, d, "Remember on this device", f);
  }
  const A = n.createElement("p");
  A.dataset.walletRememberStatus = "true";
  let b = () => {
  };
  const h = Ti({
    clipboard: e,
    controller: t,
    document: n,
    onChanged: () => b()
  }), y = n.createElement("div");
  y.className = "wallet-login-actions";
  let m = null, $ = !1;
  try {
    $ = t?.canRestoreRememberedWallet?.() === !0;
  } catch {
    $ = !1;
  }
  $ && r && (m = n.createElement("button"), m.type = "button", m.dataset.walletAction = "unlock-remembered", m.textContent = "Unlock remembered wallet");
  const v = n.createElement("button");
  v.type = "submit", v.dataset.walletAction = "login", v.textContent = "Login";
  const k = n.createElement("button");
  k.type = "button", k.dataset.walletAction = "cancel-login", k.textContent = "Cancel", m && y.append(m), y.append(v, k), d.append(y), o.append(c, l), h?.hasEntries && o.append(h.container), o.append(d, A), a.append(o), t?.registerCredentialControls?.({ passwordControl: p, usernameControl: u });
  let D = !1, L, q;
  const O = new Promise((R, H) => {
    L = R, q = H;
  }), F = () => {
    d.removeEventListener?.("submit", re), k.removeEventListener?.("click", Y), m?.removeEventListener?.("click", U);
  }, w = () => {
    if (h?.destroy?.(), f) {
      try {
        f.checked = !1;
      } catch {
      }
      try {
        f.defaultChecked = !1;
      } catch {
      }
      try {
        f.disabled = !0;
      } catch {
      }
    }
    A.textContent = "";
  }, z = () => {
    F(), Fe(d, [u, p]), w(), o.remove?.();
  }, N = (R) => {
    if (!D) {
      D = !0;
      try {
        t?.revokeNow?.(R);
      } catch {
      }
      z(), q(new V(R));
    }
  }, re = (R) => {
    R?.preventDefault?.(), !(D || R?.isTrusted !== !0) && (D = !0, F(), h?.destroy?.(), L({ passwordControl: p, rememberControl: f, rememberStatus: A, usernameControl: u }));
  }, Y = (R) => {
    R?.isTrusted === !0 && N("USER_CANCELLED");
  }, U = (R) => {
    D || R?.isTrusted !== !0 || (D = !0, F(), h?.destroy?.(), Fe(d, [u, p]), L({ remembered: !0, rememberStatus: A }));
  };
  b = () => {
    if (D || !r) return;
    let R = !1;
    try {
      R = t?.canRestoreRememberedWallet?.() === !0;
    } catch {
      R = !1;
    }
    R && !m ? (m = n.createElement("button"), m.type = "button", m.dataset.walletAction = "unlock-remembered", m.textContent = "Unlock remembered wallet", y.replaceChildren(m, v, k), m.addEventListener?.("click", U)) : !R && m && (m.removeEventListener?.("click", U), m.remove?.(), m = null);
  }, d.addEventListener("submit", re), k.addEventListener("click", Y), m?.addEventListener?.("click", U);
  try {
    u.focus?.();
  } catch {
  }
  return Object.freeze({
    cancel() {
      D ? z() : N("STALE_CONTROLLER");
    },
    controls: Object.freeze({ passwordControl: p, rememberControl: f, rememberStatus: A, usernameControl: u }),
    form: d,
    promise: O,
    remove: z
  });
}
function Ci({
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
  c.name = "mnemonic", c.autocomplete = "off", c.required = !0, it(e, o, "Mnemonic", c);
  const l = e.createElement("button");
  l.type = "submit", l.dataset.walletAction = "confirm-legacy-mnemonic", l.textContent = n;
  const d = e.createElement("button");
  d.type = "button", d.dataset.walletAction = "cancel-legacy-migration", d.textContent = "Cancel", o.append(l, d), s.append(a, o), r.append(s);
  let u = !1, p, f;
  const A = new Promise((v, k) => {
    p = v, f = k;
  }), b = () => {
    o.removeEventListener?.("submit", m), d.removeEventListener?.("click", $);
  }, h = () => {
    b(), Fe(o, [c]), s.remove?.();
  }, y = () => {
    u || (u = !0, h(), f(new V("USER_CANCELLED")));
  }, m = (v) => {
    v?.preventDefault?.(), !(u || v?.isTrusted !== !0) && (u = !0, b(), p({ form: o, mnemonicControl: c, section: s }));
  }, $ = (v) => {
    v?.isTrusted === !0 && y();
  };
  o.addEventListener("submit", m), d.addEventListener("click", $);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({
    cancel() {
      u ? h() : y();
    },
    promise: A,
    remove: h
  });
}
function xo({ document: e, mount: t = null, title: n = "Choose legacy wallet profile" }) {
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
  for (const [v, k] of [
    ["", "Select a legacy profile"],
    ["password-fast-v1-legacy", "Legacy fast-password profile"],
    ["bip39-mnemonic-v1-legacy", "Legacy BIP-39 mnemonic import"]
  ]) {
    const D = e.createElement("option");
    D.value = v, D.textContent = k, v === "" && (D.disabled = !0, D.selected = !0), c.append(D);
  }
  it(e, o, "Legacy profile", c);
  const l = e.createElement("button");
  l.type = "submit", l.dataset.walletAction = "continue-legacy-login", l.textContent = "Continue";
  const d = e.createElement("button");
  d.type = "button", d.dataset.walletAction = "cancel-legacy-login", d.textContent = "Cancel", o.append(l, d), r.append(s, a, o), i.append(r);
  let u = !1, p, f;
  const A = new Promise((v, k) => {
    p = v, f = k;
  }), b = () => {
    o.removeEventListener?.("submit", m), d.removeEventListener?.("click", $);
  }, h = () => {
    try {
      c.value = "";
    } catch {
    }
    Fe(o, [c]), r.remove?.();
  }, y = () => {
    u || (u = !0, b(), h(), f(new V("USER_CANCELLED")));
  }, m = (v) => {
    if (v?.preventDefault?.(), u || v?.isTrusted !== !0) return;
    const k = c.value;
    k !== "password-fast-v1-legacy" && k !== "bip39-mnemonic-v1-legacy" || (u = !0, b(), h(), p(k));
  }, $ = (v) => {
    v?.isTrusted === !0 && y();
  };
  o.addEventListener("submit", m), d.addEventListener("click", $);
  try {
    c.focus?.();
  } catch {
  }
  return Object.freeze({ cancel: y, promise: A });
}
function dt(e, t, n, i) {
  const r = e.createElement("div"), s = e.createElement("strong");
  s.textContent = `${n}: `;
  const a = e.createElement("span");
  a.textContent = String(i ?? ""), r.append(s, a), t.append(r);
}
function _i(e, t, n, i = null) {
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
function xi(e, t = "The wallet request could not be completed. Close this window.", n = null) {
  const i = n ?? e?.body;
  if (!e?.createElement || !i?.replaceChildren) return;
  const r = e.createElement("section");
  r.className = "wallet-terminal-error";
  const s = e.createElement("h1");
  s.textContent = "Wallet request stopped";
  const a = e.createElement("p");
  a.textContent = t, r.append(s, a), i.replaceChildren(r);
}
function Do({
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
  const d = n.createElement("div"), u = ps(d, i, { document: n }), p = n.createElement("section");
  p.className = "wallet-approval-card";
  const f = n.createElement("h2");
  f.textContent = "Asset review approval";
  const A = n.createElement("p"), b = n.createElement("div"), h = n.createElement("button");
  h.type = "button", h.dataset.walletAction = "copy-approval", h.textContent = "Copy approval configuration", h.disabled = !u.approvalAvailable, u.approvalAvailable || (A.textContent = an), p.append(f, A, h, b);
  const y = n.createElement("button");
  y.type = "button", y.dataset.walletAction = "logout", y.textContent = "Logout";
  const m = n.createElement("button");
  m.type = "button", m.dataset.walletAction = "return-to-site", m.textContent = "Return to site";
  const $ = n.createElement("section");
  $.className = "wallet-legacy-migration";
  const v = bs($, { document: n });
  let k = !1;
  try {
    k = t.canForgetRememberedWallet?.() === !0;
  } catch {
    k = !1;
  }
  const D = k ? n.createElement("section") : null, L = D ? hs(D, { document: n }) : null;
  L && (L.launch.dataset.walletAction = "forget-stored-wallet");
  const q = Ti({
    clipboard: e,
    controller: t,
    document: n
  }), O = n.createElement("p");
  O.className = "wallet-account-exit-status", l.append(d, p), D && l.append(D), q?.hasEntries && l.append(q.container), l.append(m, y, $), a.replaceChildren(l);
  const F = new gs({
    credentialRound: async (S) => {
      const j = s({
        controller: null,
        document: n,
        mount: a,
        title: `Confirm account (${S} of 2)`
      });
      j?.promise && w.add(j);
      try {
        return j?.promise ? await j.promise : await j;
      } finally {
        j?.promise && w.delete(j);
      }
    },
    expectedIdentity: i,
    wasm: c
  }), w = /* @__PURE__ */ new Set(), z = /* @__PURE__ */ new Set(), N = /* @__PURE__ */ new Set(), re = /* @__PURE__ */ new Set();
  let Y = !1, U = !1, R = !1, H = null, X = !1, K = !1, G = !1, te = 0, se = !1, ne = 0;
  const ce = (S) => !U && ne === S, J = (S) => {
    if (!ce(S)) throw new Error("account surface closed");
  }, Re = (S) => (S instanceof Uint8Array && re.add(S), S), Ue = (S) => {
    Fn(S), re.delete(S);
  }, We = () => {
    const S = [...re];
    re.clear();
    for (const j of S) Fn(j);
  }, Ie = () => {
    const S = Y || G || X;
    m.disabled = R, y.disabled = R, U || (h.disabled = S || !u.approvalAvailable, v.launch.disabled = S, L && (L.launch.disabled = S || K, L.confirm.disabled = S, L.cancel.disabled = S));
  }, je = (S) => {
    if (!z.has(S)) return !0;
    if (N.has(S)) return !1;
    N.add(S);
    try {
      return (c?.sdn ?? c).destroySdnIdentity(S), z.delete(S), !0;
    } catch {
      return !1;
    } finally {
      N.delete(S);
    }
  }, qe = () => {
    for (const S of [...z]) je(S);
    return z.size === 0;
  }, ze = async (S) => {
    if (S?.isTrusted !== !0 || Y || G || X) return;
    const j = ne;
    Y = !0, Ie(), A.textContent = "Confirm the same account twice to enable Copy.";
    try {
      const fe = await F.confirm();
      J(j);
      const Z = await ms(fe, {
        assertCurrent: () => J(j),
        clipboard: e,
        container: b,
        document: n
      });
      J(j), A.textContent = Z ? "Approval configuration copied." : "Clipboard unavailable. Copy the exact configuration shown below.";
    } catch {
      ce(j) && (A.textContent = "The two entries did not produce the same account.");
    } finally {
      Y = !1, ce(j) && Ie();
    }
  }, ve = async (S) => {
    if (S?.isTrusted !== !0 || G || Y || X) return;
    const j = v.select.value || "password-fast-v1-legacy";
    if (j !== "password-fast-v1-legacy" && j !== "bip39-mnemonic-v1-legacy") {
      v.result.textContent = "Legacy profile unavailable.";
      return;
    }
    const fe = ne;
    G = !0, te += 1, Ie(), v.result.textContent = "Enter the selected legacy credentials to compare accounts.";
    let Z = null, Ce, _t, xt, de = null;
    try {
      let Dt;
      if (j === "password-fast-v1-legacy") {
        Z = s({
          controller: null,
          document: n,
          mount: a,
          title: "Enter the legacy fast-password account"
        }), Z?.promise && w.add(Z);
        const ee = Z?.promise ? await Z.promise : await Z;
        J(fe);
        const Ge = ee?.usernameControl?.value, yn = ee?.passwordControl?.value;
        if (!Bt(Ge) || !Bt(yn)) throw new Error("invalid legacy credentials");
        Ce = Re(jt.encode(Ge)), _t = Re(jt.encode(yn));
        const mn = ee?.usernameControl?.form ?? ee?.passwordControl?.form ?? Z?.form, Ui = mn?.parentNode;
        Fe(mn, [ee?.usernameControl, ee?.passwordControl]), Ui?.remove?.(), Dt = { passwordUtf8: _t, usernameUtf8: Ce };
      } else {
        Z = Ci({ document: n, mount: a }), w.add(Z);
        const ee = await Z.promise;
        J(fe);
        const Ge = ee?.mnemonicControl?.value;
        if (!Bt(Ge)) throw new Error("invalid legacy credentials");
        xt = Re(jt.encode(Ge)), Fe(ee.form, [ee.mnemonicControl]), ee.section?.remove?.(), Dt = { mnemonicUtf8: xt };
      }
      if (Z?.promise && w.delete(Z), de = await As({
        accountIndex: 0,
        credentials: Dt,
        operation: "sdn.auth.raw-challenge.v1",
        profile: j,
        wasm: c,
        assertCurrent: () => J(fe),
        ownHandle: (ee) => z.add(ee)
      }), J(fe), !de?.handle) throw new Error("legacy derivation failed");
      const Di = j === "password-fast-v1-legacy" ? "sdn-fast-password-auth-v1-legacy" : "sdn-bip39-auth-v1-legacy", lt = Array.isArray(de.identity?.keys) ? de.identity.keys.find((ee) => ee?.purpose === "sdn-authentication") : null;
      if (de.identity?.identityScheme !== Di || de.identity?.seedProfile !== j || typeof de.identity?.accountXpub != "string" || !lt || typeof lt.publicKeyHex != "string" || !/^[0-9a-f]{64}$/u.test(lt.publicKeyHex))
        throw new Error("legacy identity invalid");
      const Pi = de.identity;
      if (!je(de.handle)) throw new Error("legacy destruction failed");
      de = null, J(fe);
      const ki = i.keys.find((ee) => ee.purpose === "sdn-authentication");
      v.result.replaceChildren(), dt(n, v.result, "Current account xpub", i.accountXpub), dt(n, v.result, "Legacy account xpub", Pi.accountXpub), dt(n, v.result, "Current authentication key", ki?.publicKeyHex), dt(n, v.result, "Legacy authentication key", lt.publicKeyHex);
    } catch {
      ce(fe) && (v.result.textContent = "Legacy account comparison could not be completed.");
    } finally {
      de?.handle && je(de.handle), Z?.promise && w.delete(Z), Ue(Ce), Ue(_t), Ue(xt), qe(), te -= 1, G = !1, ce(fe) && Ie();
    }
  }, Oe = () => {
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
  }, He = (S) => {
    if (!(!L || S?.isTrusted !== !0 || Y || G || X || K || U)) {
      Oe(), L.confirmationGroup.hidden = !1, L.status.textContent = `Type ${L.confirmationKey} to confirm.`;
      try {
        L.confirmation.focus?.();
      } catch {
      }
    }
  }, at = (S) => {
    !L || S?.isTrusted !== !0 || X || U || (Oe(), L.confirmationGroup.hidden = !0, L.status.textContent = "Forget cancelled.");
  }, Ne = (S) => {
    if (!L || S?.isTrusted !== !0 || Y || G || X || K || U) return;
    const j = L.confirmation.value;
    if (j !== L.confirmationKey) {
      L.status.textContent = "Type the exact storage key to confirm.";
      return;
    }
    X = !0, Ie(), Oe();
    try {
      t.forgetRememberedWallet(j), K = !0, L.confirmationGroup.hidden = !0, L.status.textContent = "Stored wallet forgotten. This account remains signed in.";
    } catch {
      L.status.textContent = "Stored wallet could not be forgotten.";
    } finally {
      X = !1, U || Ie();
    }
  }, Ke = (S) => {
    if (U)
      We();
    else {
      U = !0, ne += 1, h.disabled = !0, v.launch.disabled = !0, L && (L.launch.disabled = !0, L.confirm.disabled = !0, L.cancel.disabled = !0, Oe(), L.launch.removeEventListener?.("click", He), L.confirm.removeEventListener?.("click", Ne), L.cancel.removeEventListener?.("click", at)), q?.destroy?.(), We(), h.removeEventListener?.("click", ze), v.launch.removeEventListener?.("click", ve);
      for (const j of w) j.cancel?.();
      w.clear();
    }
    S && !se && (O.textContent = "Secure cleanup is still pending. Retry Return or Logout.", l.replaceChildren(O, m, y), m.disabled = !1, y.disabled = !1);
  }, ct = () => {
    const S = F.destroy(), j = qe();
    return S && j && te === 0 && !G && !Y;
  }, Te = () => (Ke(!1), se || (se = !0, m.disabled = !0, y.disabled = !0, m.removeEventListener?.("click", pn), y.removeEventListener?.("click", hn), l.remove?.()), ct()), Ct = (S, j, fe, { requireTrustedEvent: Z = !0 } = {}) => Z && S?.isTrusted !== !0 ? Promise.resolve() : H || (R || se ? Promise.reject(new V("STALE_CONTROLLER")) : (Ke(!0), ct() ? (R = !0, m.disabled = !0, y.disabled = !0, H = (async () => {
    o(), _i(n, null, fe, a);
    try {
      const Ce = await j();
      if (!r()) throw new V("STALE_CONTROLLER");
      return Ce;
    } catch (Ce) {
      throw r() && xi(n, void 0, a), Ce;
    }
  })(), H) : (O.textContent = "Secure cleanup is still pending. Retry Return or Logout.", m.disabled = !1, y.disabled = !1, Promise.reject(new V("DESTRUCTION_FAILED"))))), pn = (S) => {
    Ct(
      S,
      () => t.returnToSite(),
      "Returning to the requesting site."
    ).catch(() => {
    });
  }, hn = (S) => {
    Ct(
      S,
      () => t.logout(),
      "Logged out. Returning to the requesting site."
    ).catch(() => {
    });
  };
  return h.addEventListener("click", ze), v.launch.addEventListener("click", ve), L?.launch.addEventListener?.("click", He), L?.confirm.addEventListener?.("click", Ne), L?.cancel.addEventListener?.("click", at), m.addEventListener("click", pn), y.addEventListener("click", hn), Object.freeze({
    destroy: Te,
    logout: () => Ct(
      null,
      () => t.logout(),
      "Logged out. Returning to the requesting site.",
      { requireTrustedEvent: !1 }
    )
  });
}
function Po(e) {
  const t = e?.pathname, n = e?.search ?? "", i = e?.hash ?? "";
  if (typeof t != "string" || n !== "" || i !== "") throw Wn();
  const r = No.exec(t);
  if (!r) throw Wn();
  return r[1];
}
function ko(e) {
  const t = e?.window ?? globalThis.window, n = e?.document ?? t?.document ?? globalThis.document, i = e?.mount ?? n?.body, r = typeof t?.fetch == "function" ? t.fetch.bind(t) : typeof globalThis.fetch == "function" ? globalThis.fetch.bind(globalThis) : null, s = e?.relay ?? (e?.controller ? null : ro({
    fetch: e?.fetch ?? r,
    location: e?.location ?? t?.location
  })), a = e?.registry ?? Co, o = e?.controller ?? new ao({
    ...e,
    document: n,
    registry: a,
    relay: s,
    window: t
  }), c = e?.credentialPrompt ?? ((w) => _o(w));
  let l = null, d = null, u = null, p = null, f = null, A = [], b = 0, h = !1;
  const y = () => {
    (l?.destroy?.() ?? !0) && (l = null), d = null;
  }, m = () => {
    try {
      i?.replaceChildren?.();
    } catch {
    }
  }, $ = () => (b = b >= Number.MAX_SAFE_INTEGER ? 1 : b + 1, b), v = () => {
    h = !0, $();
  }, k = (w) => !h && b === w, D = (w) => {
    if (!k(w)) throw new V("STALE_CONTROLLER");
  }, L = (w) => {
    try {
      w?.remove?.();
    } catch {
    }
    u === w && (u = null);
  }, q = () => {
    v(), u?.cancel?.(), u = null, y(), m();
  }, O = (w, z, N) => {
    w?.addEventListener?.(z, N), A.push([w, z, N]);
  }, F = () => {
    for (const [w, z, N] of A)
      try {
        w?.removeEventListener?.(z, N);
      } catch {
      }
    A = [];
  };
  return O(t, "pagehide", q), O(n, "freeze", q), O(t, "beforeunload", q), O(t, "pageshow", (w) => {
    w?.persisted === !0 && q();
  }), Object.freeze({
    controller: o,
    logout() {
      return l ? l.logout() : (v(), u?.cancel?.(), u = null, d = null, o.logout());
    },
    start() {
      if (p) return p;
      const w = b;
      return h ? (p = Promise.reject(new V("STALE_CONTROLLER")), p) : (p = (async () => {
        try {
          const z = Po(t?.location);
          if (typeof o.prepare != "function" || typeof o.executePrepared != "function" || typeof o.unlockPassword != "function") {
            const U = await o.execute(z);
            return D(w), U;
          }
          const N = await o.prepare(z);
          if (D(w), N.transaction.operation === "sdn.auth.raw-challenge.v1") {
            const U = xo({
              document: n,
              mount: i,
              title: `Choose the legacy profile for ${N.binding.clientDisplayName}`
            });
            u = U;
            const R = await U.promise;
            D(w), u === U && (u = null);
            const H = R === "password-fast-v1-legacy" ? c({
              controller: null,
              document: n,
              mount: i,
              title: `Sign in to ${N.binding.clientDisplayName}`,
              transaction: N
            }) : Ci({
              document: n,
              mount: i,
              submitLabel: "Continue",
              title: `Sign in to ${N.binding.clientDisplayName}`
            });
            u = H?.promise ? H : null;
            const X = H?.promise ? await H.promise : await H;
            D(w);
            const K = await o.unlockLegacy({
              ...X,
              operation: N.transaction.operation,
              profile: R
            });
            D(w), d = K;
          } else {
            let U = !1;
            for (; ; ) {
              const R = c({
                controller: o,
                document: n,
                mount: i,
                offerRememberedUnlock: !U,
                title: `Sign in to ${N.binding.clientDisplayName}`,
                transaction: N
              });
              U && R?.controls?.rememberStatus && (R.controls.rememberStatus.textContent = "Remembered wallet unavailable. Enter username and password."), u = R?.promise ? R : null;
              const H = R?.promise ? await R.promise : await R;
              if (D(w), H?.remembered === !0)
                try {
                  const K = await o.unlockRemembered();
                  D(w), d = K;
                  break;
                } catch {
                  if (!k(w))
                    throw L(R), new V("STALE_CONTROLLER");
                  U = !0, L(R);
                  continue;
                }
              const X = await o.unlockPassword(H);
              D(w), d = X;
              break;
            }
          }
          const Y = await o.executePrepared(N);
          return D(w), L(u), N.transaction.operation === "sdn.wallet.account.v1" ? (d = o.copyPublicIdentity(), l = Do({
            clipboard: e?.clipboard ?? globalThis.navigator?.clipboard,
            controller: o,
            document: n,
            identity: d,
            isAppCurrent: () => k(w),
            makeCredentialPrompt: c,
            mount: i,
            onClear: y,
            wasm: e?.wasm
          })) : (d = null, n?.createElement && i?.replaceChildren && _i(
            n,
            t,
            "The wallet request completed successfully.",
            i
          )), Y;
        } catch (z) {
          const N = !k(w);
          throw u?.cancel?.(), u = null, y(), F(), N || xi(n, z?.code === "USER_CANCELLED" ? "Cancelled. You may close this window." : void 0, i), await (f ?? o.destroy(N ? "stale-startup" : "startup-failure")), N && z?.code !== "DESTRUCTION_FAILED" ? new V("STALE_CONTROLLER") : z;
        }
      })(), p);
    },
    stop(w = "close") {
      v(), u?.cancel?.(), u = null, y(), m();
      const z = o.destroy(w);
      return f || (F(), f = z), f;
    }
  });
}
async function jo(e) {
  const t = ko(e);
  return await t.start(), t;
}
export {
  _o as createPasswordCredentialPrompt,
  ko as createWalletOriginApp,
  jo as mountWalletOriginApp,
  Po as transactionIdFromLocation
};
