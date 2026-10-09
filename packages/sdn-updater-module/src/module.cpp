// org.spacedatanetwork.updater: the update coordinator.
//
// Owner 2026-10-09: "a module ... that is the update coordinator, into which we
// load the distribution signing key", and the key "MUST be entered every time
// for distribution to occur". The updater page in the dashboard loads the
// recovery phrase into this module (openKey); the module derives the
// distribution key, signs a release's manifest and signal (signRelease) and
// wipes the key (closeKey). Nothing is persisted: the key lives in this
// instance's memory between openKey and closeKey and nowhere else.
//
// The key is the SLIP-0010 Ed25519 child at m/44'/0'/0'/3'/0' of the phrase's
// BIP-39 seed: purpose 3 of the node key grammar
// (sdn-server/internal/wasm/hdwallet_purpose.go), reserved for update
// distribution. Signatures are what sdn-server/internal/update verifies:
// Ed25519 over DOMAIN || 0x00 || sha256(canonical document), where the
// canonical document is Go's encoding/json output of the document with
// signing.signature removed (update.CanonicalManifestBytes).
//
// This file is concatenated after the vendored Monocypher sources by build.mjs.

#include "space_data_module_invoke.h"

#include <stdint.h>
#include <string.h>

#include <algorithm>
#include <string>
#include <utility>
#include <vector>

#include "bip39_english.inc"

namespace {

// ---------------------------------------------------------------- SHA-256 --

struct Sha256 {
  uint32_t h[8] = {0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
                   0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19};
  uint8_t block[64];
  size_t fill = 0;
  uint64_t bits = 0;

  static uint32_t rotr(uint32_t x, int n) { return (x >> n) | (x << (32 - n)); }

  void compress(const uint8_t *p) {
    static const uint32_t k[64] = {
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
        0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
        0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
        0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
        0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
        0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
        0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2};
    uint32_t w[64];
    for (int i = 0; i < 16; i++) {
      w[i] = uint32_t(p[4 * i]) << 24 | uint32_t(p[4 * i + 1]) << 16 | uint32_t(p[4 * i + 2]) << 8 | p[4 * i + 3];
    }
    for (int i = 16; i < 64; i++) {
      const uint32_t s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >> 3);
      const uint32_t s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >> 10);
      w[i] = w[i - 16] + s0 + w[i - 7] + s1;
    }
    uint32_t a = h[0], b = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], hh = h[7];
    for (int i = 0; i < 64; i++) {
      const uint32_t t1 = hh + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + k[i] + w[i];
      const uint32_t t2 = (rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c));
      hh = g; g = f; f = e; e = d + t1; d = c; c = b; b = a; a = t1 + t2;
    }
    h[0] += a; h[1] += b; h[2] += c; h[3] += d; h[4] += e; h[5] += f; h[6] += g; h[7] += hh;
  }

  void update(const uint8_t *data, size_t size) {
    bits += uint64_t(size) * 8;
    while (size > 0) {
      const size_t take = std::min(size, sizeof(block) - fill);
      memcpy(block + fill, data, take);
      fill += take; data += take; size -= take;
      if (fill == sizeof(block)) { compress(block); fill = 0; }
    }
  }

  void final(uint8_t out[32]) {
    const uint64_t total = bits;
    const uint8_t pad = 0x80;
    update(&pad, 1);
    const uint8_t zero = 0;
    while (fill != 56) update(&zero, 1);
    uint8_t length[8];
    for (int i = 0; i < 8; i++) length[i] = uint8_t(total >> (56 - 8 * i));
    update(length, 8);
    for (int i = 0; i < 8; i++) {
      out[4 * i] = uint8_t(h[i] >> 24); out[4 * i + 1] = uint8_t(h[i] >> 16);
      out[4 * i + 2] = uint8_t(h[i] >> 8); out[4 * i + 3] = uint8_t(h[i]);
    }
    crypto_wipe(this, sizeof(*this));
  }
};

void sha256(const uint8_t *data, size_t size, uint8_t out[32]) {
  Sha256 ctx;
  ctx.update(data, size);
  ctx.final(out);
}

// ------------------------------------------------------ text encodings --

std::string hex(const uint8_t *data, size_t size) {
  static const char digits[] = "0123456789abcdef";
  std::string out;
  for (size_t i = 0; i < size; i++) { out += digits[data[i] >> 4]; out += digits[data[i] & 15]; }
  return out;
}

std::string base64(const uint8_t *data, size_t size) {
  static const char alphabet[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  std::string out;
  for (size_t i = 0; i < size; i += 3) {
    const uint32_t n = uint32_t(data[i]) << 16 | (i + 1 < size ? uint32_t(data[i + 1]) << 8 : 0) | (i + 2 < size ? data[i + 2] : 0);
    out += alphabet[(n >> 18) & 63];
    out += alphabet[(n >> 12) & 63];
    out += i + 1 < size ? alphabet[(n >> 6) & 63] : '=';
    out += i + 2 < size ? alphabet[n & 63] : '=';
  }
  return out;
}

// ------------------------------------------------------------------ JSON --
//
// A document is parsed into a tree that keeps every number's literal text, and
// written back the way Go's encoding/json writes a map[string]any decoded with
// UseNumber and encoded with SetEscapeHTML(false): keys sorted bytewise, no
// whitespace, numbers verbatim, and Go's string escapes. What Go would rewrite
// silently (invalid UTF-8, lone surrogates, duplicate keys) is refused instead.

struct Json {
  enum Kind { Null, Bool, Number, String, Array, Object } kind = Null;
  bool boolean = false;
  std::string text;  // number literal or decoded string
  std::vector<Json> items;
  std::vector<std::pair<std::string, Json>> members;

  Json *member(const std::string &key) {
    for (auto &m : members) if (m.first == key) return &m.second;
    return nullptr;
  }
  void erase(const std::string &key) {
    members.erase(std::remove_if(members.begin(), members.end(), [&](const auto &m) { return m.first == key; }), members.end());
  }
  void set(const std::string &key, const std::string &value) {
    Json *existing = member(key);
    Json v; v.kind = String; v.text = value;
    if (existing) *existing = v; else members.emplace_back(key, v);
  }
};

struct Parser {
  const char *p, *end;
  std::string error;
  int depth = 0;

  bool fail(const char *why) { if (error.empty()) error = why; return false; }
  void space() { while (p < end && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r')) p++; }

  static void put_utf8(std::string &out, uint32_t c) {
    if (c < 0x80) out += char(c);
    else if (c < 0x800) { out += char(0xc0 | (c >> 6)); out += char(0x80 | (c & 63)); }
    else if (c < 0x10000) { out += char(0xe0 | (c >> 12)); out += char(0x80 | ((c >> 6) & 63)); out += char(0x80 | (c & 63)); }
    else { out += char(0xf0 | (c >> 18)); out += char(0x80 | ((c >> 12) & 63)); out += char(0x80 | ((c >> 6) & 63)); out += char(0x80 | (c & 63)); }
  }

  bool hex4(uint32_t &out) {
    if (end - p < 4) return fail("truncated \\u escape");
    out = 0;
    for (int i = 0; i < 4; i++, p++) {
      const char c = *p;
      out <<= 4;
      if (c >= '0' && c <= '9') out |= uint32_t(c - '0');
      else if (c >= 'a' && c <= 'f') out |= uint32_t(c - 'a' + 10);
      else if (c >= 'A' && c <= 'F') out |= uint32_t(c - 'A' + 10);
      else return fail("invalid \\u escape");
    }
    return true;
  }

  // One UTF-8 sequence starting at p, validated (no overlongs, surrogates or
  // values above U+10FFFF), copied to out.
  bool utf8(std::string &out) {
    const uint8_t b0 = uint8_t(*p);
    int n; uint32_t c, min;
    if (b0 >= 0xc2 && b0 <= 0xdf) { n = 1; c = b0 & 0x1f; min = 0x80; }
    else if (b0 >= 0xe0 && b0 <= 0xef) { n = 2; c = b0 & 0x0f; min = 0x800; }
    else if (b0 >= 0xf0 && b0 <= 0xf4) { n = 3; c = b0 & 0x07; min = 0x10000; }
    else return fail("invalid UTF-8");
    if (end - p < n + 1) return fail("invalid UTF-8");
    for (int i = 1; i <= n; i++) {
      const uint8_t b = uint8_t(p[i]);
      if ((b & 0xc0) != 0x80) return fail("invalid UTF-8");
      c = (c << 6) | (b & 0x3f);
    }
    if (c < min || c > 0x10ffff || (c >= 0xd800 && c <= 0xdfff)) return fail("invalid UTF-8");
    out.append(p, size_t(n + 1));
    p += n + 1;
    return true;
  }

  bool string(std::string &out) {
    if (p >= end || *p != '"') return fail("expected a string");
    p++;
    while (p < end && *p != '"') {
      const uint8_t c = uint8_t(*p);
      if (c < 0x20) return fail("control character in a string");
      if (c >= 0x80) { if (!utf8(out)) return false; continue; }
      if (c != '\\') { out += char(c); p++; continue; }
      if (++p >= end) return fail("truncated escape");
      const char e = *p++;
      switch (e) {
        case '"': out += '"'; break;
        case '\\': out += '\\'; break;
        case '/': out += '/'; break;
        case 'b': out += '\b'; break;
        case 'f': out += '\f'; break;
        case 'n': out += '\n'; break;
        case 'r': out += '\r'; break;
        case 't': out += '\t'; break;
        case 'u': {
          uint32_t u;
          if (!hex4(u)) return false;
          if (u >= 0xdc00 && u <= 0xdfff) return fail("lone surrogate");
          if (u >= 0xd800 && u <= 0xdbff) {
            uint32_t low;
            if (end - p < 2 || p[0] != '\\' || p[1] != 'u') return fail("lone surrogate");
            p += 2;
            if (!hex4(low)) return false;
            if (low < 0xdc00 || low > 0xdfff) return fail("lone surrogate");
            u = 0x10000 + ((u - 0xd800) << 10) + (low - 0xdc00);
          }
          put_utf8(out, u);
          break;
        }
        default: return fail("invalid escape");
      }
    }
    if (p >= end) return fail("unterminated string");
    p++;
    return true;
  }

  bool number(std::string &out) {
    const char *start = p;
    if (p < end && *p == '-') p++;
    if (p < end && *p == '0') p++;
    else if (p < end && *p >= '1' && *p <= '9') { while (p < end && *p >= '0' && *p <= '9') p++; }
    else return fail("invalid number");
    if (p < end && *p == '.') {
      p++;
      if (p >= end || *p < '0' || *p > '9') return fail("invalid number");
      while (p < end && *p >= '0' && *p <= '9') p++;
    }
    if (p < end && (*p == 'e' || *p == 'E')) {
      p++;
      if (p < end && (*p == '+' || *p == '-')) p++;
      if (p >= end || *p < '0' || *p > '9') return fail("invalid number");
      while (p < end && *p >= '0' && *p <= '9') p++;
    }
    out.assign(start, size_t(p - start));
    return true;
  }

  bool literal(const char *word) {
    const size_t n = strlen(word);
    if (size_t(end - p) < n || memcmp(p, word, n) != 0) return fail("invalid literal");
    p += n;
    return true;
  }

  bool value(Json &out) {
    if (++depth > 64) return fail("document nests too deeply");
    space();
    if (p >= end) return fail("truncated document");
    bool ok;
    switch (*p) {
      case '{': {
        out.kind = Json::Object; p++; space();
        ok = true;
        if (p < end && *p == '}') { p++; break; }
        for (;;) {
          std::string key;
          space();
          if (!string(key)) { ok = false; break; }
          if (out.member(key)) { ok = fail("duplicate key"); break; }
          space();
          if (p >= end || *p != ':') { ok = fail("expected ':'"); break; }
          p++;
          Json v;
          if (!value(v)) { ok = false; break; }
          out.members.emplace_back(std::move(key), std::move(v));
          space();
          if (p < end && *p == ',') { p++; continue; }
          if (p < end && *p == '}') { p++; break; }
          ok = fail("expected ',' or '}'"); break;
        }
        break;
      }
      case '[': {
        out.kind = Json::Array; p++; space();
        ok = true;
        if (p < end && *p == ']') { p++; break; }
        for (;;) {
          Json v;
          if (!value(v)) { ok = false; break; }
          out.items.push_back(std::move(v));
          space();
          if (p < end && *p == ',') { p++; continue; }
          if (p < end && *p == ']') { p++; break; }
          ok = fail("expected ',' or ']'"); break;
        }
        break;
      }
      case '"': out.kind = Json::String; ok = string(out.text); break;
      case 't': out.kind = Json::Bool; out.boolean = true; ok = literal("true"); break;
      case 'f': out.kind = Json::Bool; ok = literal("false"); break;
      case 'n': out.kind = Json::Null; ok = literal("null"); break;
      default: out.kind = Json::Number; ok = number(out.text); break;
    }
    depth--;
    return ok;
  }
};

bool parse_json(const uint8_t *data, size_t size, Json &out, std::string &error) {
  Parser parser{reinterpret_cast<const char *>(data), reinterpret_cast<const char *>(data) + size, {}};
  if (!parser.value(out)) { error = parser.error; return false; }
  parser.space();
  if (parser.p != parser.end) { error = "trailing data after the document"; return false; }
  return true;
}

void write_string(std::string &out, const std::string &s) {
  static const char digits[] = "0123456789abcdef";
  out += '"';
  for (size_t i = 0; i < s.size(); i++) {
    const uint8_t c = uint8_t(s[i]);
    switch (c) {
      case '"': out += "\\\""; continue;
      case '\\': out += "\\\\"; continue;
      case '\b': out += "\\b"; continue;
      case '\f': out += "\\f"; continue;
      case '\n': out += "\\n"; continue;
      case '\r': out += "\\r"; continue;
      case '\t': out += "\\t"; continue;
      default: break;
    }
    if (c < 0x20) { out += "\\u00"; out += digits[c >> 4]; out += digits[c & 15]; continue; }
    // U+2028 and U+2029 (E2 80 A8 / E2 80 A9): Go escapes both unconditionally.
    if (c == 0xe2 && i + 2 < s.size() && uint8_t(s[i + 1]) == 0x80 && (uint8_t(s[i + 2]) == 0xa8 || uint8_t(s[i + 2]) == 0xa9)) {
      out += uint8_t(s[i + 2]) == 0xa8 ? "\\u2028" : "\\u2029";
      i += 2;
      continue;
    }
    out += char(c);
  }
  out += '"';
}

void write_canonical(std::string &out, const Json &v) {
  switch (v.kind) {
    case Json::Null: out += "null"; break;
    case Json::Bool: out += v.boolean ? "true" : "false"; break;
    case Json::Number: out += v.text; break;
    case Json::String: write_string(out, v.text); break;
    case Json::Array:
      out += '[';
      for (size_t i = 0; i < v.items.size(); i++) { if (i) out += ','; write_canonical(out, v.items[i]); }
      out += ']';
      break;
    case Json::Object: {
      std::vector<const std::pair<std::string, Json> *> sorted;
      for (const auto &m : v.members) sorted.push_back(&m);
      std::sort(sorted.begin(), sorted.end(), [](const auto *a, const auto *b) { return a->first < b->first; });
      out += '{';
      for (size_t i = 0; i < sorted.size(); i++) {
        if (i) out += ',';
        write_string(out, sorted[i]->first);
        out += ':';
        write_canonical(out, sorted[i]->second);
      }
      out += '}';
      break;
    }
  }
}

// ------------------------------------------------------- key derivation --

void hmac_sha512(const uint8_t *key, size_t key_size, const uint8_t *a, size_t a_size, const uint8_t *b, size_t b_size, uint8_t out[64]) {
  crypto_sha512_hmac_ctx ctx;
  crypto_sha512_hmac_init(&ctx, key, key_size);
  crypto_sha512_hmac_update(&ctx, a, a_size);
  if (b_size) crypto_sha512_hmac_update(&ctx, b, b_size);
  crypto_sha512_hmac_final(&ctx, out);
}

// BIP-39 seed: PBKDF2-HMAC-SHA512(phrase, "mnemonic" + passphrase, 2048, 64).
void bip39_seed(const std::string &phrase, const std::string &passphrase, uint8_t seed[64]) {
  std::string salt = "mnemonic" + passphrase;
  salt += std::string("\0\0\0\1", 4);
  uint8_t u[64];
  hmac_sha512(reinterpret_cast<const uint8_t *>(phrase.data()), phrase.size(),
              reinterpret_cast<const uint8_t *>(salt.data()), salt.size(), nullptr, 0, u);
  memcpy(seed, u, 64);
  for (int i = 1; i < 2048; i++) {
    hmac_sha512(reinterpret_cast<const uint8_t *>(phrase.data()), phrase.size(), u, 64, nullptr, 0, u);
    for (int j = 0; j < 64; j++) seed[j] ^= u[j];
  }
  crypto_wipe(u, sizeof(u));
  crypto_wipe(&salt[0], salt.size());
}

// Normalizes and checks a BIP-39 English phrase: 12 to 24 words, every word in
// the list, and the checksum.
bool bip39_phrase(const std::string &input, std::string &phrase, std::string &error) {
  std::vector<int> indices;
  std::string word;
  auto flush = [&]() -> bool {
    if (word.empty()) return true;
    const auto *begin = kBip39English, *end = kBip39English + 2048;
    const auto *it = std::lower_bound(begin, end, word, [](const char *a, const std::string &b) { return strcmp(a, b.c_str()) < 0; });
    if (it == end || word != *it) { error = "\"" + word + "\" is not a recovery phrase word"; return false; }
    indices.push_back(int(it - begin));
    if (!phrase.empty()) phrase += ' ';
    phrase += word;
    crypto_wipe(&word[0], word.size());
    word.clear();
    return true;
  };
  for (const char c : input) {
    if (c == ' ' || c == '\t' || c == '\n' || c == '\r') { if (!flush()) return false; continue; }
    if (c >= 'A' && c <= 'Z') { word += char(c - 'A' + 'a'); continue; }
    if (c >= 'a' && c <= 'z') { word += c; continue; }
    error = "a recovery phrase holds only English BIP-39 words";
    return false;
  }
  if (!flush()) return false;
  const size_t n = indices.size();
  if (n < 12 || n > 24 || n % 3 != 0) { error = "a recovery phrase has 12, 15, 18, 21 or 24 words"; return false; }
  const size_t bits = n * 11, checksum_bits = bits / 33, entropy_bytes = (bits - checksum_bits) / 8;
  uint8_t packed[33] = {0};
  for (size_t i = 0; i < n; i++) {
    for (int b = 0; b < 11; b++) {
      if (indices[i] & (1 << (10 - b))) { const size_t at = i * 11 + size_t(b); packed[at / 8] |= uint8_t(0x80 >> (at % 8)); }
    }
  }
  uint8_t digest[32];
  sha256(packed, entropy_bytes, digest);
  const uint8_t want = uint8_t(digest[0] >> (8 - checksum_bits)), got = uint8_t(packed[entropy_bytes] >> (8 - checksum_bits));
  crypto_wipe(packed, sizeof(packed));
  if (want != got) { error = "the recovery phrase's checksum does not match: check the words"; return false; }
  return true;
}

// SLIP-0010 Ed25519 private key at a fully hardened path.
void slip10_ed25519(const uint8_t seed[64], const uint32_t *path, size_t depth, uint8_t key[32]) {
  static const char curve[] = "ed25519 seed";
  uint8_t node[64];
  hmac_sha512(reinterpret_cast<const uint8_t *>(curve), strlen(curve), seed, 64, nullptr, 0, node);
  for (size_t i = 0; i < depth; i++) {
    uint8_t data[37];
    data[0] = 0;
    memcpy(data + 1, node, 32);
    const uint32_t index = path[i] | 0x80000000u;
    data[33] = uint8_t(index >> 24); data[34] = uint8_t(index >> 16); data[35] = uint8_t(index >> 8); data[36] = uint8_t(index);
    uint8_t next[64];
    hmac_sha512(node + 32, 32, data, sizeof(data), nullptr, 0, next);
    memcpy(node, next, 64);
    crypto_wipe(next, sizeof(next));
    crypto_wipe(data, sizeof(data));
  }
  memcpy(key, node, 32);
  crypto_wipe(node, sizeof(node));
}

// ------------------------------------------------------------ the key --

const char kDistributionPath[] = "m/44'/0'/0'/3'/0'";
const uint32_t kDistributionIndices[] = {44, 0, 0, 3, 0};
const char kManifestDomain[] = "SDN-UPDATE-MANIFEST-V1";
const char kSignalDomain[] = "SDN-UPDATE-SIGNAL-V1";
// DER prefix of an Ed25519 SubjectPublicKeyInfo.
const uint8_t kSpkiPrefix[12] = {0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00};

struct Key {
  bool loaded = false;
  uint8_t secret[64];  // Monocypher's seed || public key
  uint8_t pub[32];
} g_key;

void close_key() {
  crypto_wipe(&g_key, sizeof(g_key));
  g_key.loaded = false;
}

std::string key_id() {
  uint8_t digest[32];
  sha256(g_key.pub, 32, digest);
  return hex(digest, 32).substr(0, 12);
}

std::string public_key_spki() {
  uint8_t der[44];
  memcpy(der, kSpkiPrefix, 12);
  memcpy(der + 12, g_key.pub, 32);
  return base64(der, sizeof(der));
}

// ---------------------------------------------------------- invocation --

int push(const char *port, const std::string &body) {
  return plugin_push_output(port, "raw.json", "JSON", reinterpret_cast<const uint8_t *>(body.data()), uint32_t(body.size())) < 0 ? 1 : 0;
}

int push_error(const std::string &message) {
  std::string body = "{\"error\":";
  write_string(body, message);
  body += '}';
  push("result", body);
  return 1;
}

bool read_request(Json &out, std::string &error) {
  const int32_t index = plugin_find_input_index("request", 0);
  if (index < 0) { error = "the request is missing"; return false; }
  const plugin_input_frame_t *frame = plugin_get_input_frame(uint32_t(index));
  if (!frame || !frame->payload) { error = "the request is missing"; return false; }
  if (!parse_json(frame->payload, frame->payload_length, out, error)) return false;
  if (out.kind != Json::Object) { error = "the request is not a JSON object"; return false; }
  return true;
}

std::string key_report() {
  std::string body = "{\"key_id\":";
  write_string(body, key_id());
  body += ",\"public_key\":";
  write_string(body, public_key_spki());
  body += ",\"path\":";
  write_string(body, kDistributionPath);
  body += '}';
  return body;
}

}  // namespace

// openKey {"phrase": "<recovery phrase>", "passphrase": "<optional>"}
//   -> {"key_id", "public_key" (Ed25519 SPKI, base64), "path"}
extern "C" int openKey(void) {
  close_key();
  Json request;
  std::string error;
  if (!read_request(request, error)) return push_error(error);
  const Json *phrase = request.member("phrase");
  const Json *passphrase = request.member("passphrase");
  if (!phrase || phrase->kind != Json::String) return push_error("enter the recovery phrase");
  if (passphrase && passphrase->kind != Json::String) return push_error("the passphrase must be text");
  // BIP-39 normalizes a passphrase to Unicode NFKD; ASCII is its own NFKD form,
  // so only ASCII is accepted rather than deriving a key other wallets would not.
  if (passphrase) {
    for (const char c : passphrase->text) {
      if (uint8_t(c) >= 0x80) return push_error("the passphrase must be ASCII");
    }
  }
  std::string normalized;
  if (!bip39_phrase(phrase->text, normalized, error)) return push_error(error);
  uint8_t seed[64], child[32];
  bip39_seed(normalized, passphrase ? passphrase->text : std::string(), seed);
  crypto_wipe(&normalized[0], normalized.size());
  for (auto &m : request.members) if (m.second.kind == Json::String) crypto_wipe(&m.second.text[0], m.second.text.size());
  slip10_ed25519(seed, kDistributionIndices, 5, child);
  crypto_wipe(seed, sizeof(seed));
  crypto_ed25519_key_pair(g_key.secret, g_key.pub, child);  // wipes child
  g_key.loaded = true;
  return push("result", key_report());
}

// signRelease {"kind": "manifest" | "signal", "document": {...}}
//   -> the document, canonical, signed with the distribution key
extern "C" int signRelease(void) {
  if (!g_key.loaded) return push_error("enter the distribution key first");
  Json request;
  std::string error;
  if (!read_request(request, error)) return push_error(error);
  const Json *kind = request.member("kind");
  Json *document = request.member("document");
  if (!kind || kind->kind != Json::String || (kind->text != "manifest" && kind->text != "signal")) return push_error("kind must be manifest or signal");
  if (!document || document->kind != Json::Object) return push_error("the document must be a JSON object");
  const char *domain = kind->text == "manifest" ? kManifestDomain : kSignalDomain;
  Json *signing = document->member("signing");
  if (!signing || signing->kind != Json::Object) return push_error("the document has no signing block");
  const Json *statement_domain = signing->member("statement_domain");
  if (!statement_domain || statement_domain->kind != Json::String || statement_domain->text != domain) {
    return push_error(std::string("the document must be signed under ") + domain);
  }
  const Json *algorithm = signing->member("algorithm");
  if (!algorithm || algorithm->kind != Json::String || algorithm->text != "Ed25519") return push_error("the document must name Ed25519");
  // The signer's identity is covered by the signature, so it goes in first.
  signing->set("key_id", key_id());
  signing->set("public_key", public_key_spki());
  signing->erase("signature");
  std::string canonical;
  write_canonical(canonical, *document);
  std::vector<uint8_t> statement(domain, domain + strlen(domain));
  statement.push_back(0);
  statement.resize(statement.size() + 32);
  sha256(reinterpret_cast<const uint8_t *>(canonical.data()), canonical.size(), statement.data() + statement.size() - 32);
  uint8_t signature[64];
  crypto_ed25519_sign(signature, g_key.secret, statement.data(), statement.size());
  signing->set("signature", base64(signature, sizeof(signature)));
  std::string signed_document;
  write_canonical(signed_document, *document);
  return push("result", signed_document);
}

// closeKey {} -> {"closed": true}; the key is wiped.
extern "C" int closeKey(void) {
  close_key();
  return push("result", "{\"closed\":true}");
}
