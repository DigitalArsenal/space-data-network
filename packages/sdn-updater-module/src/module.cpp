// org.spacedatanetwork.updater: the update coordinator.
//
// Owner 2026-10-09: releases are approved in this module's page. By default the
// coordinator node signs with its own key when an administrator approves; or
// the administrator uploads another Ed25519 key, which is loaded into this
// module (openKey), signs the release's manifest and signal (signRelease) and
// is wiped (closeKey). An uploaded key is never saved: it lives in this
// instance's memory between openKey and closeKey and nowhere else.
//
// Signatures are what sdn-server/internal/update verifies: Ed25519 over
// DOMAIN || 0x00 || sha256(canonical document), where the canonical document is
// Go's encoding/json output of the document with signing.signature removed
// (update.CanonicalManifestBytes).
//
// This file is concatenated after the vendored Monocypher sources by build.mjs.

#include "space_data_module_invoke.h"

#include <stdint.h>
#include <string.h>

#include <algorithm>
#include <string>
#include <utility>
#include <vector>

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

// ------------------------------------------------------- key files --

int base64_value(char c) {
  if (c >= 'A' && c <= 'Z') return c - 'A';
  if (c >= 'a' && c <= 'z') return c - 'a' + 26;
  if (c >= '0' && c <= '9') return c - '0' + 52;
  if (c == '+') return 62;
  if (c == '/') return 63;
  return -1;
}

bool hex_value(char c, int &out) {
  if (c >= '0' && c <= '9') { out = c - '0'; return true; }
  if (c >= 'a' && c <= 'f') { out = c - 'a' + 10; return true; }
  if (c >= 'A' && c <= 'F') { out = c - 'A' + 10; return true; }
  return false;
}

// The Ed25519 seed in an uploaded key file: a PKCS#8 PEM private key (as
// `openssl genpkey -algorithm ed25519` writes it) or the 32-byte seed as 64 hex
// characters.
bool key_file_seed(const std::string &file, uint8_t seed[32], std::string &error) {
  std::string text;
  for (const char c : file) if (c != ' ' && c != '\t' && c != '\r' && c != '\n') text += c;
  if (text.size() == 64) {
    for (int i = 0; i < 32; i++) {
      int hi, lo;
      if (!hex_value(text[2 * i], hi) || !hex_value(text[2 * i + 1], lo)) { error = "the key is not 64 hex characters"; return false; }
      seed[i] = uint8_t(hi << 4 | lo);
    }
    crypto_wipe(&text[0], text.size());
    return true;
  }
  static const char begin[] = "-----BEGINPRIVATEKEY-----", end[] = "-----ENDPRIVATEKEY-----";
  const size_t b = text.find(begin), e = text.find(end);
  if (b != 0 || e == std::string::npos || e + strlen(end) != text.size()) {
    crypto_wipe(&text[0], text.size());
    error = "upload an Ed25519 private key: a PKCS#8 PEM file or 64 hex characters";
    return false;
  }
  std::vector<uint8_t> der;
  uint32_t acc = 0; int bits = 0;
  for (size_t i = strlen(begin); i < e; i++) {
    if (text[i] == '=') break;
    const int v = base64_value(text[i]);
    if (v < 0) { crypto_wipe(&text[0], text.size()); error = "the PEM key is not base64"; return false; }
    acc = (acc << 6) | uint32_t(v); bits += 6;
    if (bits >= 8) { bits -= 8; der.push_back(uint8_t(acc >> bits)); }
  }
  crypto_wipe(&text[0], text.size());
  // PKCS#8 OneAsymmetricKey for Ed25519 (RFC 8410): a fixed 16-byte prefix,
  // then the 32-byte seed.
  static const uint8_t prefix[16] = {0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20};
  const bool ok = der.size() == 48 && memcmp(der.data(), prefix, 16) == 0;
  if (ok) memcpy(seed, der.data() + 16, 32);
  crypto_wipe(der.data(), der.size());
  if (!ok) error = "the PEM file is not an Ed25519 private key";
  return ok;
}

// ------------------------------------------------------------ the key --

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
  body += '}';
  return body;
}

}  // namespace

// openKey {"key_file": "<uploaded key file>"}
//   -> {"key_id", "public_key" (Ed25519 SPKI, base64)}
extern "C" int openKey(void) {
  close_key();
  Json request;
  std::string error;
  if (!read_request(request, error)) return push_error(error);
  Json *file = request.member("key_file");
  if (!file || file->kind != Json::String) return push_error("upload a key file");
  uint8_t seed[32];
  const bool ok = key_file_seed(file->text, seed, error);
  crypto_wipe(&file->text[0], file->text.size());
  if (!ok) return push_error(error);
  crypto_ed25519_key_pair(g_key.secret, g_key.pub, seed);  // wipes seed
  g_key.loaded = true;
  return push("result", key_report());
}

// signRelease {"kind": "manifest" | "signal", "document": {...}}
//   -> the document, canonical, signed with the distribution key
extern "C" int signRelease(void) {
  if (!g_key.loaded) return push_error("upload the key first");
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
