/**
 * SDN Plugin Delivery Module (C++ / Crypto++)
 *
 * Fetches a plugin artifact from IPFS, encrypts it using the
 * ecies-x25519-hkdf-sha256-aes-256-gcm envelope scheme, and returns the
 * JSON envelope to the client.
 *
 * Plugin ABI: plugin_invoke_stream (direct surface), WASI imports + sdn_host.
 *
 * Methods:
 *   deliver_plugin  — input[0]: client X25519 pub key (32 bytes)
 *                     input[1]: plugin CID (UTF-8 string)
 *                   — output[0]: JSON envelope (UTF-8)
 *
 *   get_public_key  — output[0]: server X25519 pub key (32 bytes)
 *
 * The server's X25519 private key is baked at build time (placeholder
 * replaced by build script before compilation).
 */

#include <stdint.h>
#include <stddef.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>

#include <array>
#include <cstring>
#include <memory>
#include <string>
#include <vector>

#include <flatbuffers/flatbuffers.h>
#include "PluginInvokeRequest_generated.h"
#include "PluginInvokeResponse_generated.h"

#include <cryptopp/aes.h>
#include <cryptopp/gcm.h>
#include <cryptopp/hkdf.h>
#include <cryptopp/sha.h>
#include <cryptopp/xed25519.h>
#include <cryptopp/secblock.h>

// ── WASI-compatible RNG (replaces AutoSeededRandomPool) ─────────────────────
// In standalone WASM, /dev/urandom is not available. Use WASI random_get
// which the WASI shim provides.

#ifdef __wasi__
#include <wasi/api.h>
#endif

class WasiRNG : public CryptoPP::RandomNumberGenerator {
public:
    void GenerateBlock(CryptoPP::byte* output, size_t size) override {
#ifdef __wasi__
        __wasi_random_get(output, size);
#else
        // Fallback: use stdlib (not cryptographically secure, test only)
        for (size_t i = 0; i < size; i++)
            output[i] = static_cast<CryptoPP::byte>(rand());
#endif
    }
    void IncorporateEntropy(const CryptoPP::byte*, size_t) override {}
};

// ── sdn_host imports ──────────────────────────────────────────────────────────
// Available when compiled as WASI plugin with -DSDN_WASI_PLUGIN=1

#if defined(SDN_WASI_PLUGIN)
extern "C" __attribute__((import_module("sdn_host"), import_name("call_json")))
int32_t sdn_host_call_json(const char* op_ptr, int32_t op_len,
                           const char* payload_ptr, int32_t payload_len);

extern "C" __attribute__((import_module("sdn_host"), import_name("response_len")))
int32_t sdn_host_response_len(void);

extern "C" __attribute__((import_module("sdn_host"), import_name("read_response")))
int32_t sdn_host_read_response(char* dst_ptr, int32_t dst_len);

extern "C" __attribute__((import_module("sdn_host"), import_name("clear_response")))
int32_t sdn_host_clear_response(void);
#endif

// ── Baked key material (replaced by build script) ─────────────────────────────
// SDN_BAKED_SERVER_PRIVATE_KEY — 32-byte X25519 private key as C byte literal

static const uint8_t SERVER_PRIVATE_KEY[32] = { SDN_BAKED_SERVER_PRIVATE_KEY };

// ── Constants ─────────────────────────────────────────────────────────────────

static const char HKDF_WRAP_INFO[] = "orbpro-key-server-artifact-wrap-v1";
static const size_t HKDF_KEY_BYTES = 32;
static const size_t GCM_IV_BYTES = 12;
static const size_t GCM_TAG_BYTES = 16;
static const size_t HKDF_SALT_BYTES = 32;

// ── Base64 encoding ───────────────────────────────────────────────────────────

static const char B64_CHARS[] =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

static std::string base64_encode(const uint8_t* data, size_t len) {
    std::string out;
    out.reserve(((len + 2) / 3) * 4);
    for (size_t i = 0; i < len; i += 3) {
        uint32_t n = (uint32_t)data[i] << 16;
        if (i + 1 < len) n |= (uint32_t)data[i + 1] << 8;
        if (i + 2 < len) n |= (uint32_t)data[i + 2];
        out += B64_CHARS[(n >> 18) & 0x3f];
        out += B64_CHARS[(n >> 12) & 0x3f];
        out += (i + 1 < len) ? B64_CHARS[(n >> 6) & 0x3f] : '=';
        out += (i + 2 < len) ? B64_CHARS[(n >> 0) & 0x3f] : '=';
    }
    return out;
}

static std::string bytes_to_hex(const uint8_t* data, size_t len) {
    static const char HEX[] = "0123456789abcdef";
    std::string out;
    out.reserve(len * 2);
    for (size_t i = 0; i < len; i++) {
        out += HEX[(data[i] >> 4) & 0xf];
        out += HEX[data[i] & 0xf];
    }
    return out;
}

// ── Crypto helpers ────────────────────────────────────────────────────────────

struct EncryptedEnvelope {
    std::string ephemeralPublicKeyHex;
    std::string hkdfSaltB64;
    std::string wrapIvB64;
    std::string wrappedKeyB64;
    std::string wrappedKeyTagB64;
    std::string contentIvB64;
    std::string contentTagB64;
    std::string ciphertextB64;
    bool ok;
    std::string error;
};

static EncryptedEnvelope encrypt_artifact(
    const uint8_t* plaintext, size_t plaintext_len,
    const uint8_t* recipient_pub_key, size_t recipient_pub_len)
{
    EncryptedEnvelope env;
    env.ok = false;

    if (recipient_pub_len != 32) {
        env.error = "recipient public key must be 32 bytes";
        return env;
    }

    try {
        WasiRNG rng;

        // 1. Generate ephemeral X25519 key pair
        CryptoPP::x25519 x25519_scheme;
        CryptoPP::SecByteBlock ephemeral_priv(32), ephemeral_pub(32);
        x25519_scheme.GeneratePrivateKey(rng, ephemeral_priv);
        x25519_scheme.GeneratePublicKey(rng, ephemeral_priv, ephemeral_pub);

        // 2. ECDH with recipient public key
        CryptoPP::SecByteBlock shared_secret(32);
        if (!x25519_scheme.Agree(shared_secret, ephemeral_priv, recipient_pub_key)) {
            env.error = "X25519 key agreement failed";
            return env;
        }

        // 3. HKDF to derive wrap key
        uint8_t hkdf_salt[HKDF_SALT_BYTES];
        rng.GenerateBlock(hkdf_salt, HKDF_SALT_BYTES);

        CryptoPP::SecByteBlock wrap_key(HKDF_KEY_BYTES);
        CryptoPP::HKDF<CryptoPP::SHA256> hkdf;
        hkdf.DeriveKey(
            wrap_key, HKDF_KEY_BYTES,
            shared_secret, shared_secret.size(),
            hkdf_salt, HKDF_SALT_BYTES,
            reinterpret_cast<const uint8_t*>(HKDF_WRAP_INFO),
            sizeof(HKDF_WRAP_INFO) - 1
        );

        // 4. Generate random content key (32 bytes)
        CryptoPP::SecByteBlock content_key(HKDF_KEY_BYTES);
        rng.GenerateBlock(content_key, HKDF_KEY_BYTES);

        // 5. Wrap the content key with AES-256-GCM
        uint8_t wrap_iv[GCM_IV_BYTES];
        rng.GenerateBlock(wrap_iv, GCM_IV_BYTES);

        std::vector<uint8_t> wrapped_key_and_tag(HKDF_KEY_BYTES + GCM_TAG_BYTES);
        {
            CryptoPP::GCM<CryptoPP::AES>::Encryption enc;
            enc.SetKeyWithIV(wrap_key, HKDF_KEY_BYTES, wrap_iv, GCM_IV_BYTES);
            CryptoPP::ArraySink sink(wrapped_key_and_tag.data(), wrapped_key_and_tag.size());
            CryptoPP::AuthenticatedEncryptionFilter aef(enc, &sink, false, GCM_TAG_BYTES);
            aef.Put(content_key, HKDF_KEY_BYTES);
            aef.MessageEnd();
        }

        // 6. Encrypt the plaintext with AES-256-GCM
        uint8_t content_iv[GCM_IV_BYTES];
        rng.GenerateBlock(content_iv, GCM_IV_BYTES);

        std::vector<uint8_t> ciphertext_and_tag(plaintext_len + GCM_TAG_BYTES);
        {
            CryptoPP::GCM<CryptoPP::AES>::Encryption enc;
            enc.SetKeyWithIV(content_key, HKDF_KEY_BYTES, content_iv, GCM_IV_BYTES);
            CryptoPP::ArraySink sink(ciphertext_and_tag.data(), ciphertext_and_tag.size());
            CryptoPP::AuthenticatedEncryptionFilter aef(enc, &sink, false, GCM_TAG_BYTES);
            aef.Put(plaintext, plaintext_len);
            aef.MessageEnd();
        }

        // 7. Fill envelope
        env.ephemeralPublicKeyHex = bytes_to_hex(ephemeral_pub, 32);
        env.hkdfSaltB64 = base64_encode(hkdf_salt, HKDF_SALT_BYTES);
        env.wrapIvB64 = base64_encode(wrap_iv, GCM_IV_BYTES);
        // Crypto++ GCM appends tag at end: split ciphertext | tag
        env.wrappedKeyB64 = base64_encode(wrapped_key_and_tag.data(), HKDF_KEY_BYTES);
        env.wrappedKeyTagB64 = base64_encode(wrapped_key_and_tag.data() + HKDF_KEY_BYTES, GCM_TAG_BYTES);
        env.contentIvB64 = base64_encode(content_iv, GCM_IV_BYTES);
        env.ciphertextB64 = base64_encode(ciphertext_and_tag.data(), plaintext_len);
        env.contentTagB64 = base64_encode(ciphertext_and_tag.data() + plaintext_len, GCM_TAG_BYTES);
        env.ok = true;

    } catch (const std::exception& ex) {
        env.error = ex.what();
    }

    return env;
}

// ── Server public key derivation ──────────────────────────────────────────────

// NullRNG — deterministic public key derivation doesn't need randomness.
// AutoSeededRandomPool fails in standalone WASM without filesystem.
class NullRNG : public CryptoPP::RandomNumberGenerator {
public:
    void GenerateBlock(CryptoPP::byte*, size_t) override {}
    void IncorporateEntropy(const CryptoPP::byte*, size_t) override {}
};

static void derive_server_public_key(uint8_t out_pub[32]) {
    NullRNG rng;
    CryptoPP::x25519 x25519_scheme;
    x25519_scheme.GeneratePublicKey(rng, SERVER_PRIVATE_KEY, out_pub);
}

// ── JSON envelope builder ─────────────────────────────────────────────────────

static std::string build_envelope_json(const EncryptedEnvelope& env) {
    // Simple inline JSON builder — avoids a JSON library dependency
    std::string j;
    j += "{\"keyEncryption\":{";
    j += "\"scheme\":\"ecies-x25519-hkdf-sha256-aes-256-gcm\",";
    j += "\"ephemeralPublicKeyHex\":\""; j += env.ephemeralPublicKeyHex; j += "\",";
    j += "\"hkdfSaltB64\":\""; j += env.hkdfSaltB64; j += "\",";
    j += "\"wrapIvB64\":\""; j += env.wrapIvB64; j += "\",";
    j += "\"wrappedKeyB64\":\""; j += env.wrappedKeyB64; j += "\",";
    j += "\"wrappedKeyTagB64\":\""; j += env.wrappedKeyTagB64; j += "\"";
    j += "},\"contentEncryption\":{";
    j += "\"algorithm\":\"aes-256-gcm\",";
    j += "\"ivB64\":\""; j += env.contentIvB64; j += "\",";
    j += "\"tagB64\":\""; j += env.contentTagB64; j += "\",";
    j += "\"ciphertextB64\":\""; j += env.ciphertextB64; j += "\"";
    j += "}}";
    return j;
}

// ── sdn_host IPFS fetch helper ────────────────────────────────────────────────

#if defined(SDN_WASI_PLUGIN)

// Base64 decode for hostcall response parsing
static std::vector<uint8_t> b64_decode_vec(const char* in, size_t in_len) {
    std::vector<uint8_t> out;
    out.reserve(in_len * 3 / 4);
    int val = 0, bits = -8;
    for (size_t i = 0; i < in_len; i++) {
        char c = in[i];
        int v;
        if (c >= 'A' && c <= 'Z') v = c - 'A';
        else if (c >= 'a' && c <= 'z') v = c - 'a' + 26;
        else if (c >= '0' && c <= '9') v = c - '0' + 52;
        else if (c == '+' || c == '-') v = 62;
        else if (c == '/' || c == '_') v = 63;
        else continue;
        val = (val << 6) + v;
        bits += 6;
        if (bits >= 0) {
            out.push_back(static_cast<uint8_t>((val >> bits) & 0xff));
            bits -= 8;
        }
    }
    return out;
}

static bool fetch_ipfs_bytes(const char* cid, size_t cid_len,
                             std::vector<uint8_t>& out_bytes)
{
    // Build JSON payload: {"cid":"<cid>"}
    std::string payload = "{\"cid\":\"";
    payload.append(cid, cid_len);
    payload += "\"}";

    static const char OP[] = "ipfs.cat";
    int32_t status = sdn_host_call_json(OP, sizeof(OP) - 1,
                                        payload.c_str(), (int32_t)payload.size());
    if (status != 0) return false;

    int32_t resp_len = sdn_host_response_len();
    if (resp_len <= 0) return false;

    // Read JSON envelope: {"ok":true,"result":{"__type":"bytes","base64":"..."}}
    std::vector<char> json_buf((size_t)resp_len);
    int32_t read = sdn_host_read_response(json_buf.data(), resp_len);
    sdn_host_clear_response();
    if (read != resp_len) return false;

    // Extract the base64 value from the JSON envelope
    std::string json_str(json_buf.data(), (size_t)resp_len);

    // Check for "ok":true
    if (json_str.find("\"ok\":true") == std::string::npos &&
        json_str.find("\"ok\": true") == std::string::npos) {
        return false;
    }

    // Find "base64":"<value>"
    const char* b64_key = "\"base64\":\"";
    size_t b64_pos = json_str.find(b64_key);
    if (b64_pos == std::string::npos) {
        // Fallback: if response doesn't have base64 wrapper, treat as raw bytes
        out_bytes.assign(json_buf.begin(), json_buf.end());
        return true;
    }

    size_t val_start = b64_pos + strlen(b64_key);
    size_t val_end = json_str.find('"', val_start);
    if (val_end == std::string::npos) return false;

    out_bytes = b64_decode_vec(json_str.data() + val_start, val_end - val_start);
    return !out_bytes.empty();
}
#else
// Stub for host-side builds
static bool fetch_ipfs_bytes(const char*, size_t, std::vector<uint8_t>&) {
    return false;
}
#endif

// ── FlatBuffer helpers ────────────────────────────────────────────────────────

using namespace orbpro::invoke;

static flatbuffers::DetachedBuffer build_error_response(const char* msg) {
    flatbuffers::FlatBufferBuilder fbb(256);
    auto msg_off = fbb.CreateString(msg);
    PluginInvokeResponseBuilder rb(fbb);
    rb.add_status_code(1); // error
    rb.add_error_message(msg_off);
    FinishPluginInvokeResponseBuffer(fbb, rb.Finish());
    return fbb.Release();
}

static flatbuffers::DetachedBuffer build_bytes_response(
    const uint8_t* data, size_t len, uint8_t status = 0)
{
    flatbuffers::FlatBufferBuilder fbb(len + 512);
    // Arena: payload at offset 0
    auto arena_vec = fbb.CreateVector(data, len);
    // TypedArenaBuffer frame: offset=0, size=len
    using namespace orbpro::stream;
    TypedArenaBufferBuilder tb(fbb);
    tb.add_offset(0);
    tb.add_size(static_cast<uint32_t>(len));
    auto frame_off = tb.Finish();
    auto frames_vec = fbb.CreateVector(&frame_off, 1);

    PluginInvokeResponseBuilder rb(fbb);
    rb.add_status_code(status);
    rb.add_output_frames(frames_vec);
    rb.add_payload_arena(arena_vec);
    FinishPluginInvokeResponseBuffer(fbb, rb.Finish());
    return fbb.Release();
}

// ── Method dispatch ───────────────────────────────────────────────────────────

static flatbuffers::DetachedBuffer handle_deliver_plugin(
    const PluginInvokeRequest* req)
{
    // input[0]: client X25519 pub key (32 bytes)
    // input[1]: plugin CID string (UTF-8)
    const auto* frames = req->input_frames();
    if (!frames || frames->size() < 2) {
        return build_error_response("deliver_plugin requires 2 input frames");
    }

    const auto* arena = req->payload_arena();
    if (!arena) {
        return build_error_response("missing payload arena");
    }

    // Frame 0: client pub key
    const auto* frame0 = frames->Get(0);
    uint32_t key_offset = frame0->offset();
    uint32_t key_size = frame0->size();
    if (key_size != 32 || key_offset + key_size > arena->size()) {
        return build_error_response("client pub key must be 32 bytes");
    }
    const uint8_t* client_pub = arena->data() + key_offset;

    // Frame 1: CID string
    const auto* frame1 = frames->Get(1);
    uint32_t cid_offset = frame1->offset();
    uint32_t cid_size = frame1->size();
    if (cid_size == 0 || cid_offset + cid_size > arena->size()) {
        return build_error_response("CID is empty");
    }
    const char* cid_ptr = reinterpret_cast<const char*>(arena->data() + cid_offset);

    // Fetch plugin bytes from IPFS
    std::vector<uint8_t> plugin_bytes;
    if (!fetch_ipfs_bytes(cid_ptr, cid_size, plugin_bytes)) {
        return build_error_response("IPFS fetch failed");
    }
    if (plugin_bytes.empty()) {
        return build_error_response("IPFS returned empty data");
    }

    // Encrypt artifact for client's public key
    auto env = encrypt_artifact(plugin_bytes.data(), plugin_bytes.size(),
                                client_pub, 32);
    if (!env.ok) {
        return build_error_response(env.error.c_str());
    }

    std::string json = build_envelope_json(env);
    return build_bytes_response(
        reinterpret_cast<const uint8_t*>(json.data()), json.size());
}

static flatbuffers::DetachedBuffer handle_get_public_key() {
    uint8_t pub[32];
    derive_server_public_key(pub);
    return build_bytes_response(pub, 32);
}

// ── Plugin ABI exports ────────────────────────────────────────────────────────

extern "C" {

// Memory allocator for the invoke ABI
__attribute__((visibility("default")))
uint8_t* plugin_alloc(uint32_t size) {
    return static_cast<uint8_t*>(malloc(size));
}

__attribute__((visibility("default")))
void plugin_free(uint8_t* ptr, uint32_t /*size*/) {
    free(ptr);
}

// Direct-surface invoke entry point
__attribute__((visibility("default")))
uint8_t* plugin_invoke_stream(const uint8_t* req_ptr, uint32_t req_len,
                              uint32_t* out_len_ptr)
{
    if (!req_ptr || req_len == 0 || !out_len_ptr) {
        *out_len_ptr = 0;
        return nullptr;
    }

    // Parse FlatBuffer request
    flatbuffers::Verifier verifier(req_ptr, req_len);
    if (!VerifyPluginInvokeRequestBuffer(verifier)) {
        *out_len_ptr = 0;
        return nullptr;
    }
    const PluginInvokeRequest* req = GetPluginInvokeRequest(req_ptr);

    flatbuffers::DetachedBuffer response;
    const char* method = req->method_id() ? req->method_id()->c_str() : "";

    if (strcmp(method, "deliver_plugin") == 0) {
        response = handle_deliver_plugin(req);
    } else if (strcmp(method, "get_public_key") == 0) {
        response = handle_get_public_key();
    } else {
        response = build_error_response("unknown method");
    }

    uint32_t rlen = static_cast<uint32_t>(response.size());
    uint8_t* rptr = static_cast<uint8_t*>(malloc(rlen));
    if (!rptr) { *out_len_ptr = 0; return nullptr; }
    memcpy(rptr, response.data(), rlen);
    *out_len_ptr = rlen;
    return rptr;
}

} // extern "C"
