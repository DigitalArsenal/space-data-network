// A module built by the space-data-module-sdk compiler that calls the node through typedHostcall.hpp (SDS $PIV
// hostcalls: HostOps aligned records + raw / list frames). Each t_* export makes its calls and writes a one-line
// report to out_buffer (guest_out_ptr / guest_out_len) that typed_hostcall_e2e_test.go reads. Rebuild with
// build.mjs (see README.md).
#include <cstdarg>
#include <cstdio>
#include <cstring>
#include <string>
#include "typedHostcall.hpp"

namespace A = sdn::runtime::Aligned;

static char out_buffer[2048];
static int out_length = 0;
static int report(const char* format, ...) __attribute__((format(printf, 1, 2)));
static int report(const char* format, ...) {
  va_list args;
  va_start(args, format);
  out_length = std::vsnprintf(out_buffer, sizeof out_buffer, format, args);
  va_end(args);
  return 0;
}
static std::string hex(std::string_view bytes, size_t n) {
  static const char* digits = "0123456789abcdef";
  std::string s;
  for (size_t i = 0; i < n && i < bytes.size(); ++i) {
    s += digits[(unsigned char)bytes[i] >> 4];
    s += digits[(unsigned char)bytes[i] & 15];
  }
  return s;
}

extern "C" {
int guest_out_ptr(void) { return (int)(intptr_t)out_buffer; }
int guest_out_len(void) { return out_length; }

int t_clock(void) {
  auto r = sdm_piv::call("clock.now");
  const auto* t = r.result<A::TimeValue>("TimeValue");
  return report("%d %llu", r.ok(), t ? (unsigned long long)t->UNIX_MS : 0ull);
}

int t_random(void) {
  A::RandomBytesParams p{};
  p.LENGTH = 16;
  auto r = sdm_piv::call("random.bytes", "RandomBytesParams", p);
  A::RandomBytesParams big{};
  big.LENGTH = 9000;
  auto b = sdm_piv::call("random.bytes", "RandomBytesParams", big);
  return report("%d %zu %d %s", r.ok(), r.raw("data").size(), b.ok(), b.error_code().c_str());
}

int t_config(void) {
  auto r = sdm_piv::call("plugin.getConfig");
  const auto entries = r.list<A::ConfigEntry>("entries", "ConfigEntry");
  double lead = -1;
  std::string provider = "-";
  for (size_t i = 0; i < entries.second; ++i) {
    const auto& e = entries.first[i];
    if (e.KEY.str() == "nwp.max_lead" && e.KIND == A::configEntryKind_Number) lead = e.NUMBER_VALUE;
    if (e.KEY.str() == "targets.0.provider_id" && e.KIND == A::configEntryKind_String) provider = e.STRING_VALUE.str();
  }
  return report("%d %zu %g %s", r.ok(), entries.second, lead, provider.c_str());
}

int t_ops(void) {
  auto r = sdm_piv::call("host.listOperations");
  const auto items = r.list<A::TextValue>("items", "TextValue");
  int expire = 0, query = 0;
  for (size_t i = 0; i < items.second; ++i) {
    expire |= items.first[i].VALUE.str() == "storage.expire";
    query |= items.first[i].VALUE.str() == "storage.query";
  }
  return report("%d %d %d", r.ok(), expire, query);
}

// records: a size-prefixed record stream the test put in guest memory
int t_ingest(int ptr, int len) {
  A::StorageIngestParams p{};
  sdm_piv::put(p.SCHEMA, "OMM.fbs"); p.set_has_SCHEMA(true);
  sdm_piv::put(p.PROVIDER_ID, "typed-test"); p.set_has_PROVIDER_ID(true);
  sdm_piv::put(p.SOURCE_NAME, "typed-src"); p.set_has_SOURCE_NAME(true);
  sdm_piv::put(p.BATCH_ID, "typed-batch-1"); p.set_has_BATCH_ID(true);
  p.RECONCILE = A::reconcileKind_None;
  auto r = sdm_piv::call("storage.ingest_with_source", "StorageIngestParams", p,
                         {sdm_piv::raw("records", reinterpret_cast<const uint8_t*>((intptr_t)ptr), (size_t)len)});
  const auto* res = r.result<A::StorageIngestResult>("StorageIngestResult");
  return report("%d %llu %s %s", r.ok(), res ? (unsigned long long)res->INSERTED : 0ull, res ? res->SCHEMA.str().c_str() : "-",
                r.error_message().c_str());
}

int t_expire(void) {
  A::StorageExpireParams p{};
  sdm_piv::put(p.SCHEMA, "OMM.fbs"); p.set_has_SCHEMA(true);
  sdm_piv::put(p.PROVIDER_ID, "typed-test"); p.set_has_PROVIDER_ID(true);
  sdm_piv::put(p.SOURCE_NAME, "typed-src"); p.set_has_SOURCE_NAME(true);
  auto now = sdm_piv::call("clock.now");
  p.BEFORE_MS = now.result<A::TimeValue>("TimeValue")->UNIX_MS + 3600000ull;
  p.MAX_DELETES = 1000000;  // a million: the JSON path's %v once read this as 1
  auto r = sdm_piv::call("storage.expire", "StorageExpireParams", p);
  const auto* res = r.result<A::StorageExpireResult>("StorageExpireResult");
  return report("%d %llu %d %s", r.ok(), res ? (unsigned long long)res->DELETED : 0ull, res ? (int)res->CAPPED : -1, r.error_message().c_str());
}

int t_denied(void) {
  A::StorageQueryParams p{};
  sdm_piv::put(p.SCHEMA, "OMM.fbs"); p.set_has_SCHEMA(true);
  auto r = sdm_piv::call("storage.query", "StorageQueryParams", p);
  return report("%d %s", r.ok(), r.error_code().c_str());
}

int t_sha(void) {
  A::CryptoParams p{};
  p.ALGORITHM = A::cryptoAlgorithm_Sha256;
  auto r = sdm_piv::call("crypto.hash", "CryptoParams", p, {sdm_piv::raw("data", "abc")});
  return report("%d %s %zu", r.ok(), hex(r.raw("data"), 4).c_str(), r.raw("data").size());
}

int t_sign(void) {
  A::CryptoParams p{};
  p.ALGORITHM = A::cryptoAlgorithm_Ed25519;
  auto key = sdm_piv::call("crypto.generate_key", "CryptoParams", p);
  const std::string priv(key.raw("private_key")), pub(key.raw("public_key"));
  auto sig = sdm_piv::call("crypto.sign", "CryptoParams", p, {sdm_piv::raw("private_key", priv), sdm_piv::raw("data", "weather")});
  const std::string signature(sig.raw("data"));
  auto good = sdm_piv::call("crypto.verify", "CryptoParams", p,
                            {sdm_piv::raw("public_key", pub), sdm_piv::raw("data", "weather"), sdm_piv::raw("signature", signature)});
  auto bad = sdm_piv::call("crypto.verify", "CryptoParams", p,
                           {sdm_piv::raw("public_key", pub), sdm_piv::raw("data", "Weather"), sdm_piv::raw("signature", signature)});
  const auto* g = good.result<A::BoolValue>("BoolValue");
  const auto* b = bad.result<A::BoolValue>("BoolValue");
  return report("%d %zu %zu %zu %d %d", key.ok(), priv.size(), pub.size(), signature.size(), g ? (int)g->VALUE : -1, b ? (int)b->VALUE : -1);
}

int t_http(int port) {
  char url[96];
  std::snprintf(url, sizeof url, "http://127.0.0.1:%d/gfs.t06z.pgrb2.0p25.f006.idx", port);
  A::HttpParams h{};
  sdm_piv::put(h.METHOD, "GET"); h.set_has_METHOD(true);
  h.NO_REDIRECTS = true;
  auto r = sdm_piv::call("http.request", "HttpParams", h, {sdm_piv::raw("url", url), sdm_piv::raw("headers", "Range: bytes=0-63")});
  const auto* res = r.result<A::HttpResult>("HttpResult");
  const std::string_view headers = r.raw("headers");
  return report("%d %d %s|%d|%s", r.ok(), res ? res->STATUS : 0, hex(r.raw("body"), 4).c_str(),
                (int)(headers.find("Content-Range: bytes 0-3/4") != std::string_view::npos), r.error_message().c_str());
}

int t_gcm(void) {
  A::CryptoParams p{};
  p.ALGORITHM = A::cryptoAlgorithm_Aes256Gcm;
  const std::string key(32, '\x07'), nonce(12, '\x05');
  auto enc = sdm_piv::call("crypto.encrypt", "CryptoParams", p,
                           {sdm_piv::raw("key", key), sdm_piv::raw("nonce", nonce), sdm_piv::raw("data", "hello")});
  const std::string sealed(enc.raw("data"));
  auto dec = sdm_piv::call("crypto.decrypt", "CryptoParams", p,
                           {sdm_piv::raw("key", key), sdm_piv::raw("nonce", nonce), sdm_piv::raw("data", sealed)});
  // a 5-byte nonce panicked the node's GCM: now it is refused
  auto bad = sdm_piv::call("crypto.encrypt", "CryptoParams", p,
                           {sdm_piv::raw("key", key), sdm_piv::raw("nonce", "short"), sdm_piv::raw("data", "hello")});
  return report("%d %zu %d %s %d %s", enc.ok(), sealed.size(), dec.ok(), std::string(dec.raw("data")).c_str(), bad.ok(), bad.error_code().c_str());
}
}
