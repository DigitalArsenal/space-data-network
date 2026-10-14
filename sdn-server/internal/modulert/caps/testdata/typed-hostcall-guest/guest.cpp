// A module built by the space-data-module-sdk compiler that calls the node through typedHostcall.hpp (SDS $PIV
// hostcalls: HostOps aligned records + raw / list frames). Each t_* export makes its calls and writes a one-line
// report to out_buffer (guest_out_ptr / guest_out_len) that typed_hostcall_e2e_test.go reads. Rebuild with
// build.mjs (see README.md).
#include <cstdarg>
#include <cstdio>
#include <cstring>
#include <string>
#include <vector>
#include <unistd.h>
#include "typedHostcall.hpp"

namespace A = sdn::runtime::Aligned;

static char out_buffer[4096];
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

// plugin.getConfig: one ModuleConfig FlatBuffer (MCFG) on raw "config", read in place through sdm_piv::get_config();
// dotted paths reach into nested tables and lists.
int t_config(void) {
  const sdm_piv::Config c = sdm_piv::get_config();
  if (!c.ok()) return report("0 %s %s", c.error_code().c_str(), c.error_message().c_str());
  const auto widths = c.numbers("widths");
  const auto names = c.strings("names");
  return report("%d %zu %g %s %zu %g %s %d %zu %s", c.ok(), c.size(), c.number("nwp.max_lead", -1),
                std::string(c.string("targets.0.provider_id", "-")).c_str(), widths.size(), c.number("widths.1", -1),
                names.size() > 1 ? std::string(names[1]).c_str() : "-", (int)c.boolean("dotted.key"), c.tables("targets"),
                std::string(c.table("nwp").string("model", "-")).c_str());
}

// A large config: the heap grows by about the ModuleConfig's size (the response kept whole, read in place).
int t_big_config(void) {
  const uintptr_t before = (uintptr_t)sbrk(0);
  const sdm_piv::Config c = sdm_piv::get_config();
  if (!c.ok()) return report("0 %s %s", c.error_code().c_str(), c.error_message().c_str());
  const uintptr_t after = (uintptr_t)sbrk(0);
  const auto index = c.numbers("index");
  double sum = 0;
  for (double v : index) sum += v;
  return report("1 %zu %.0f %zu %zu", index.size(), sum, c.byte_size(), (size_t)(after - before));
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

// records: a size-prefixed record stream the test put in guest memory; with list "provenance" (one Provenance) and
// list "provenance_attributes" (in order)
int t_ingest(int ptr, int len) {
  A::StorageIngestParams p{};
  sdm_piv::put(p.SCHEMA, "OMM.fbs"); p.set_has_SCHEMA(true);
  sdm_piv::put(p.PROVIDER_ID, "typed-test"); p.set_has_PROVIDER_ID(true);
  sdm_piv::put(p.SOURCE_NAME, "typed-src"); p.set_has_SOURCE_NAME(true);
  sdm_piv::put(p.BATCH_ID, "typed-batch-1"); p.set_has_BATCH_ID(true);
  // a 128-byte LICENSE: wider than the 0.9.0 bound (64)
  const std::string license = "LicenseRef-" + std::string(117, 'L');
  sdm_piv::put(p.LICENSE, license); p.set_has_LICENSE(true);
  p.RECONCILE = A::reconcileKind_None;
  A::Provenance prov{};
  sdm_piv::put(prov.SOURCE_URL, "https://example.test/omm.csv"); prov.set_has_SOURCE_URL(true);
  prov.HTTP_STATUS = 200;
  // a 200-byte ETAG and a 100-byte PARSER: wider than the 0.9.0 bounds (128, 64)
  sdm_piv::put(prov.ETAG, std::string(200, 'e')); prov.set_has_ETAG(true);
  sdm_piv::put(prov.PARSER, std::string(100, 'p')); prov.set_has_PARSER(true);
  prov.NORMALIZED_COUNT = 2;
  A::ProvenanceAttribute attrs[3]{};
  sdm_piv::put(attrs[0].NAME, "method"); attrs[0].set_has_NAME(true);
  sdm_piv::put(attrs[0].VALUE, "SGP4 mean elements"); attrs[0].set_has_VALUE(true);
  sdm_piv::put(attrs[1].NAME, "units"); attrs[1].set_has_NAME(true);
  sdm_piv::put(attrs[1].VALUE, "rev/day"); attrs[1].set_has_VALUE(true);
  sdm_piv::put(attrs[2].NAME, "date_range"); attrs[2].set_has_NAME(true);
  sdm_piv::put(attrs[2].VALUE, "2026-10-13/2026-10-14"); attrs[2].set_has_VALUE(true);
  auto r = sdm_piv::call("storage.ingest_with_source", "StorageIngestParams", p,
                         {sdm_piv::raw("records", reinterpret_cast<const uint8_t*>((intptr_t)ptr), (size_t)len),
                          sdm_piv::list("provenance", "Provenance", &prov, 1),
                          sdm_piv::list("provenance_attributes", "ProvenanceAttribute", attrs, 3)});
  const auto* res = r.result<A::StorageIngestResult>("StorageIngestResult");
  return report("%d %llu %s %s|%s", r.ok(), res ? (unsigned long long)res->INSERTED : 0ull, res ? res->SCHEMA.str().c_str() : "-",
                res ? res->PROVENANCE_FILE.str().c_str() : "-", r.error_message().c_str());
}

// storage.flatsql_query_stream with raw "params": an i64, a null, a 300-byte string, bytes and an i64 beyond 2^53;
// the WHERE clause holds only when every value arrives whole.
int t_sql(void) {
  A::SqlStreamParams s{};
  const std::string text(300, 'q');
  const uint8_t blob[3] = {0x00, 0xff, 0x10};
  sdm_piv::SqlValues v;
  v.i64(25544).null().str(text).bytes(blob, 3).i64(9007199254740993LL);
  const char* sql =
      "SELECT _data FROM OMM WHERE NORAD_CAT_ID = ? AND ? IS NULL AND length(?) = 300 AND hex(?) = '00FF10' "
      "AND ? = 9007199254740993";
  auto r = sdm_piv::call("storage.flatsql_query_stream", "SqlStreamParams", s, {sdm_piv::raw("sql", sql), v.frame()});
  const auto* res = r.result<A::StreamResult>("StreamResult");
  // a malformed run (a bool carrying 2 bytes) is refused invalid-params
  const char bad[] = {1, 2, 0, 0, 0, 1, 1};
  auto b = sdm_piv::call("storage.flatsql_query_stream", "SqlStreamParams", s,
                         {sdm_piv::raw("sql", sql), sdm_piv::raw("params", std::string_view(bad, sizeof bad))});
  return report("%d %llu %zu %d %s|%s", r.ok(), res ? (unsigned long long)res->ROWS : 0ull, r.raw("stream").size(), b.ok(),
                b.error_code().c_str(), r.error_message().c_str());
}

// storage.query_sandboxed WANT Rows: list "columns" + raw "cells" (tagged SQL values, row-major)
int t_rows(void) {
  A::SandboxQueryParams q{};
  q.WANT = A::sandboxWant_Rows;
  sdm_piv::SqlValues v;
  v.null().f64(0.5).str("typed");
  auto r = sdm_piv::call("storage.query_sandboxed", "SandboxQueryParams", q,
                         {sdm_piv::raw("sql", "SELECT NORAD_CAT_ID, OBJECT_NAME, ? AS empty, ? AS half, ? AS word FROM OMM ORDER BY NORAD_CAT_ID"),
                          v.frame()});
  const auto* res = r.result<A::SandboxQueryResult>("SandboxQueryResult");
  std::string columns;
  const auto cols = r.list<A::TextValue>("columns", "TextValue");
  for (size_t i = 0; i < cols.second; ++i) columns += (i ? "," : "") + cols.first[i].VALUE.str();
  std::vector<sdm_piv::SqlValue> cells;
  std::string described;
  if (!sdm_piv::read_sql_values(r.raw("cells"), cells)) described = "malformed";
  for (const auto& c : cells) {
    char one[96];
    switch (c.tag) {
      case sdm_piv::kSqlNull: std::snprintf(one, sizeof one, "null"); break;
      case sdm_piv::kSqlBool: std::snprintf(one, sizeof one, "b%d", (int)c.as_bool()); break;
      case sdm_piv::kSqlI64: std::snprintf(one, sizeof one, "i%lld", (long long)c.as_i64()); break;
      case sdm_piv::kSqlF64: std::snprintf(one, sizeof one, "f%g", c.as_f64()); break;
      case sdm_piv::kSqlStr: std::snprintf(one, sizeof one, "s%.*s", (int)c.payload.size(), c.payload.data()); break;
      default: std::snprintf(one, sizeof one, "t%d", (int)c.tag);
    }
    described += (described.empty() ? "" : " ") + std::string(one);
  }
  return report("%d %d %llu %u %s|%s|%s", r.ok(), res ? (int)res->KIND : -1, res ? (unsigned long long)res->ROWS : 0ull,
                res ? (unsigned)res->COLUMNS : 0u, columns.c_str(), described.c_str(), r.error_message().c_str());
}

static const char* kProvider = "16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3";

// p2p.peers_snapshot: lists "peers", "peer_addresses", "peer_standards" and raw "records" ($EPM frames)
int t_peers(void) {
  A::P2pParams p{};
  auto r = sdm_piv::call("p2p.peers_snapshot", "P2pParams", p);
  const auto* sum = r.result<A::P2pSummary>("P2pSummary");
  const auto peers = r.list<A::P2pPeer>("peers", "P2pPeer");
  const auto addrs = r.list<A::P2pPeerValue>("peer_addresses", "P2pPeerValue");
  const auto stds = r.list<A::P2pPeerValue>("peer_standards", "P2pPeerValue");
  std::string out;
  for (size_t i = 0; i < peers.second; ++i) {
    const auto& e = peers.first[i];
    char one[256];
    std::snprintf(one, sizeof one, "%s%s:%d%d%d:%s", i ? " " : "", e.PEER_ID.str().c_str(), (int)e.CONNECTED, (int)e.SELF, (int)e.EPM_INDEX,
                  e.AGENT_VERSION.str().c_str());
    out += one;
  }
  std::string values;
  for (size_t i = 0; i < addrs.second; ++i) values += " a:" + addrs.first[i].PEER_ID.str().substr(0, 8) + "=" + addrs.first[i].VALUE.str();
  for (size_t i = 0; i < stds.second; ++i) values += " s:" + stds.first[i].PEER_ID.str().substr(0, 8) + "=" + stds.first[i].VALUE.str();
  return report("%d %s %zu|%s|%s|%s", r.ok(), sum ? sum->SELF.str().c_str() : "-", r.raw("records").size(), out.c_str(), values.c_str() + (values.empty() ? 0 : 1),
                r.error_message().c_str());
}

// p2p.standards_snapshot + p2p.pnm_history: P2pPnmEntry lists (FILE_ID up to 255 bytes) and raw "records" ($PNM frames)
int t_pnms(void) {
  A::P2pParams all{};
  auto snap = sdm_piv::call("p2p.standards_snapshot", "P2pParams", all);
  const auto entries = snap.list<A::P2pPnmEntry>("entries", "P2pPnmEntry");
  std::string out;
  for (size_t i = 0; i < entries.second; ++i) {
    const auto& e = entries.first[i];
    char one[400];
    std::snprintf(one, sizeof one, "%s%s:%zu:%s:%llu:%d", i ? " " : "", e.STANDARD.str().c_str(), e.FILE_ID.str().size(), e.CID.str().c_str(),
                  (unsigned long long)e.PUBLISH_MS, (int)e.PNM_INDEX);
    out += one;
  }
  A::P2pParams mine{};
  sdm_piv::put(mine.PEER_ID, kProvider); mine.set_has_PEER_ID(true);
  mine.LIMIT = 1;
  auto hist = sdm_piv::call("p2p.pnm_history", "P2pParams", mine);
  const auto* sum = hist.result<A::P2pSummary>("P2pSummary");
  const auto h = hist.list<A::P2pPnmEntry>("entries", "P2pPnmEntry");
  std::string first = "-";
  if (h.second) {
    const auto& e = h.first[0];
    char one[700];
    std::snprintf(one, sizeof one, "%s:%zu:%llu:%s:%zu:%d:%s:%zu:%s:%s", e.PUBLISHER_PEER_ID.str().substr(0, 8).c_str(), e.FILE_ID.str().size(),
                  (unsigned long long)e.PUBLISH_MS, e.SIGNATURE_TYPE.str().c_str(), e.SIGNATURE_HEX.str().size(), (int)e.SIGNATURE_VERIFIED,
                  e.ATTRIBUTION.str().c_str(), e.PUBLISHER_KEY_HEX.str().size(), e.PUBLISHER_KEY_SOURCE.str().c_str(), e.FILE_NAME.str().c_str());
    first = one;
  }
  return report("%d %zu %zu|%s|%d %d %u %zu %zu|%s|%s", snap.ok(), entries.second, snap.raw("records").size(), out.c_str(), hist.ok(),
                sum ? (int)sum->PUBLISHER_KEY_AVAILABLE : -1, sum ? (unsigned)sum->GOSSIP_ONLY_EXCLUDED : 0u, h.second, hist.raw("records").size(),
                first.c_str(), (snap.error_message() + hist.error_message()).c_str());
}

// p2p.latest_dataset: the batch inline on raw "stream", then by reference (DELIVER Ref -> REF)
int t_latest(void) {
  A::P2pParams p{};
  sdm_piv::put(p.PEER_ID, kProvider); p.set_has_PEER_ID(true);
  sdm_piv::put(p.STANDARD, "OMM"); p.set_has_STANDARD(true);
  auto in = sdm_piv::call("p2p.latest_dataset", "P2pParams", p);
  const auto* a = in.result<A::P2pLatestDataset>("P2pLatestDataset");
  p.DELIVER = A::deliverMode_Ref;
  auto ref = sdm_piv::call("p2p.latest_dataset", "P2pParams", p);
  const auto* b = ref.result<A::P2pLatestDataset>("P2pLatestDataset");
  if (!a || !b) return report("%d %d %s %s", in.ok(), ref.ok(), in.error_message().c_str(), ref.error_message().c_str());
  return report("%d %d%d%d%d %s %s %zu %llu %s %s %s %llu %llu %u %llu %s %zu|%d %zu %llu %lld %s", in.ok(), (int)a->KNOWN, (int)a->PINNED,
                (int)a->SELF, (int)a->FRESH, a->STANDARD.str().c_str(), a->SCHEMA.str().c_str(), a->PNM_FILE_ID.str().size(),
                (unsigned long long)a->PNM_PUBLISH_MS, a->PNM_ATTRIBUTION.str().c_str(), a->PROVIDER_ID.str().c_str(), a->SOURCE_NAME.str().c_str(),
                (unsigned long long)a->RECORD_COUNT, (unsigned long long)a->BYTE_COUNT, (unsigned)a->PARTS, (unsigned long long)a->PUBLISHED_MS,
                a->ETAG_FNV1A64.str().c_str(), in.raw("stream").size(), ref.ok(), ref.raw("stream").size(), (unsigned long long)b->REF.SIZE,
                (long long)b->REF.FRAMES, b->REF.FNV1A64.str().c_str());
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
