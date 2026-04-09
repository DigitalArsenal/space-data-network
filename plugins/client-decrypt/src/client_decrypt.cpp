/**
 * SDN Client Decrypt Module (C++ / Crypto++)
 *
 * Decrypts an ecies-x25519-hkdf-sha256-aes-256-gcm encrypted artifact
 * envelope using a recipient X25519 private key.
 *
 * Plugin ABI: plugin_invoke_stream (direct surface), WASI-standalone.
 *
 * Methods:
 *   decrypt_artifact  — input[0]: JSON envelope (UTF-8)
 *                       input[1]: recipient X25519 private key (32 bytes)
 *                     — output[0]: decrypted plaintext bytes
 */

#include <stdint.h>
#include <stddef.h>
#include <string.h>
#include <stdlib.h>

#include <memory>
#include <string>
#include <vector>

#include <flatbuffers/flatbuffers.h>
#include "PluginInvokeRequest_generated.h"
#include "PluginInvokeResponse_generated.h"

#include <cryptopp/aes.h>
#include <cryptopp/gcm.h>
#include <cryptopp/hkdf.h>
#include <cryptopp/osrng.h>
#include <cryptopp/sha.h>
#include <cryptopp/xed25519.h>
#include <cryptopp/secblock.h>

// ── Constants ─────────────────────────────────────────────────────────────────

static const char WRAP_INFOS[][64] = {
    "orbpro-key-server-artifact-wrap-v1",
    "plugin-key-server-artifact-wrap-v1",
};
static const size_t WRAP_INFO_COUNT = 2;
static const size_t HKDF_KEY_BYTES = 32;
static const size_t GCM_TAG_BYTES = 16;

// ── Base64 decode ─────────────────────────────────────────────────────────────

static int b64_char_value(char c) {
    if (c >= 'A' && c <= 'Z') return c - 'A';
    if (c >= 'a' && c <= 'z') return c - 'a' + 26;
    if (c >= '0' && c <= '9') return c - '0' + 52;
    if (c == '+') return 62;
    if (c == '/') return 63;
    if (c == '-') return 62; // URL-safe
    if (c == '_') return 63; // URL-safe
    return -1;
}

static std::vector<uint8_t> base64_decode(const char* in, size_t in_len) {
    std::vector<uint8_t> out;
    out.reserve(in_len * 3 / 4);
    int val = 0, bits = -8;
    for (size_t i = 0; i < in_len; i++) {
        int v = b64_char_value(in[i]);
        if (v < 0) continue;
        val = (val << 6) + v;
        bits += 6;
        if (bits >= 0) {
            out.push_back(static_cast<uint8_t>((val >> bits) & 0xff));
            bits -= 8;
        }
    }
    return out;
}

static std::vector<uint8_t> hex_decode(const char* in, size_t in_len) {
    std::vector<uint8_t> out;
    out.reserve(in_len / 2);
    for (size_t i = 0; i + 1 < in_len; i += 2) {
        auto hex_nibble = [](char c) -> int {
            if (c >= '0' && c <= '9') return c - '0';
            if (c >= 'a' && c <= 'f') return c - 'a' + 10;
            if (c >= 'A' && c <= 'F') return c - 'A' + 10;
            return -1;
        };
        int hi = hex_nibble(in[i]);
        int lo = hex_nibble(in[i + 1]);
        if (hi < 0 || lo < 0) break;
        out.push_back(static_cast<uint8_t>((hi << 4) | lo));
    }
    return out;
}

// ── Minimal JSON string extractor ─────────────────────────────────────────────
// Only extracts string values from flat key:"value" pairs — no nesting needed
// beyond the two-level structure of the envelope.

static std::string json_get_string(const char* json, size_t json_len,
                                   const char* key)
{
    // Find  "key":"  pattern
    std::string needle = std::string("\"") + key + "\":\"";
    const char* p = json;
    const char* end = json + json_len;
    while (p < end) {
        p = static_cast<const char*>(
            memmem(p, (size_t)(end - p), needle.c_str(), needle.size()));
        if (!p) return {};
        const char* val_start = p + needle.size();
        const char* val_end = val_start;
        // Scan to closing quote (no escape handling needed for base64/hex)
        while (val_end < end && *val_end != '"') val_end++;
        return std::string(val_start, val_end);
    }
    return {};
}

// ── Decrypt implementation ────────────────────────────────────────────────────

struct DecryptResult {
    bool ok;
    std::vector<uint8_t> plaintext;
    std::string error;
};

static DecryptResult decrypt_artifact(
    const char* envelope_json, size_t json_len,
    const uint8_t* priv_key, size_t priv_len)
{
    DecryptResult result;
    result.ok = false;

    if (priv_len != 32) {
        result.error = "private key must be 32 bytes";
        return result;
    }

    // Extract envelope fields
    auto eph_pub_hex    = json_get_string(envelope_json, json_len, "ephemeralPublicKeyHex");
    auto hkdf_salt_b64  = json_get_string(envelope_json, json_len, "hkdfSaltB64");
    auto wrap_iv_b64    = json_get_string(envelope_json, json_len, "wrapIvB64");
    auto wrapped_key_b64 = json_get_string(envelope_json, json_len, "wrappedKeyB64");
    auto wrapped_tag_b64 = json_get_string(envelope_json, json_len, "wrappedKeyTagB64");
    auto content_iv_b64 = json_get_string(envelope_json, json_len, "ivB64");
    auto content_tag_b64 = json_get_string(envelope_json, json_len, "tagB64");
    auto ciphertext_b64 = json_get_string(envelope_json, json_len, "ciphertextB64");

    if (eph_pub_hex.empty() || hkdf_salt_b64.empty() || wrap_iv_b64.empty() ||
        wrapped_key_b64.empty() || wrapped_tag_b64.empty() ||
        content_iv_b64.empty() || content_tag_b64.empty() || ciphertext_b64.empty())
    {
        result.error = "missing envelope fields";
        return result;
    }

    auto eph_pub_bytes   = hex_decode(eph_pub_hex.data(), eph_pub_hex.size());
    auto hkdf_salt       = base64_decode(hkdf_salt_b64.data(), hkdf_salt_b64.size());
    auto wrap_iv         = base64_decode(wrap_iv_b64.data(), wrap_iv_b64.size());
    auto wrapped_key     = base64_decode(wrapped_key_b64.data(), wrapped_key_b64.size());
    auto wrapped_tag     = base64_decode(wrapped_tag_b64.data(), wrapped_tag_b64.size());
    auto content_iv      = base64_decode(content_iv_b64.data(), content_iv_b64.size());
    auto content_tag     = base64_decode(content_tag_b64.data(), content_tag_b64.size());
    auto ciphertext      = base64_decode(ciphertext_b64.data(), ciphertext_b64.size());

    if (eph_pub_bytes.size() != 32) {
        result.error = "invalid ephemeral public key";
        return result;
    }

    try {
        CryptoPP::AutoSeededRandomPool rng;

        // 1. X25519 ECDH
        CryptoPP::x25519 x25519_scheme;
        CryptoPP::SecByteBlock shared_secret(32);
        if (!x25519_scheme.Agree(shared_secret, priv_key, eph_pub_bytes.data())) {
            result.error = "X25519 key agreement failed";
            return result;
        }

        // 2. Try each HKDF info string
        CryptoPP::SecByteBlock content_key(HKDF_KEY_BYTES);
        bool unwrapped = false;
        for (size_t i = 0; i < WRAP_INFO_COUNT && !unwrapped; i++) {
            const char* info = WRAP_INFOS[i];
            size_t info_len = strlen(info);

            CryptoPP::SecByteBlock wrap_key(HKDF_KEY_BYTES);
            CryptoPP::HKDF<CryptoPP::SHA256> hkdf;
            hkdf.DeriveKey(
                wrap_key, HKDF_KEY_BYTES,
                shared_secret, 32,
                hkdf_salt.data(), hkdf_salt.size(),
                reinterpret_cast<const uint8_t*>(info), info_len
            );

            // Decrypt content key: ciphertext = wrapped_key, tag = wrapped_tag
            std::vector<uint8_t> wrapped_combined;
            wrapped_combined.insert(wrapped_combined.end(), wrapped_key.begin(), wrapped_key.end());
            wrapped_combined.insert(wrapped_combined.end(), wrapped_tag.begin(), wrapped_tag.end());

            try {
                CryptoPP::GCM<CryptoPP::AES>::Decryption dec;
                dec.SetKeyWithIV(wrap_key, HKDF_KEY_BYTES,
                                 wrap_iv.data(), wrap_iv.size());
                CryptoPP::ArraySink sink(content_key, HKDF_KEY_BYTES);
                CryptoPP::AuthenticatedDecryptionFilter adf(dec, &sink,
                    CryptoPP::AuthenticatedDecryptionFilter::DEFAULT_FLAGS,
                    GCM_TAG_BYTES);
                adf.Put(wrapped_combined.data(), wrapped_combined.size());
                adf.MessageEnd();
                unwrapped = true;
            } catch (...) {
                // Try next info string
            }
        }

        if (!unwrapped) {
            result.error = "failed to unwrap content key with any HKDF info string";
            return result;
        }

        // 3. Decrypt content
        std::vector<uint8_t> ct_with_tag;
        ct_with_tag.insert(ct_with_tag.end(), ciphertext.begin(), ciphertext.end());
        ct_with_tag.insert(ct_with_tag.end(), content_tag.begin(), content_tag.end());

        result.plaintext.resize(ciphertext.size());
        CryptoPP::GCM<CryptoPP::AES>::Decryption dec;
        dec.SetKeyWithIV(content_key, HKDF_KEY_BYTES,
                         content_iv.data(), content_iv.size());
        CryptoPP::ArraySink sink(result.plaintext.data(), result.plaintext.size());
        CryptoPP::AuthenticatedDecryptionFilter adf(dec, &sink,
            CryptoPP::AuthenticatedDecryptionFilter::DEFAULT_FLAGS,
            GCM_TAG_BYTES);
        adf.Put(ct_with_tag.data(), ct_with_tag.size());
        adf.MessageEnd();

        result.ok = true;
    } catch (const std::exception& ex) {
        result.error = ex.what();
        result.plaintext.clear();
    }

    return result;
}

// ── FlatBuffer helpers ────────────────────────────────────────────────────────

using namespace orbpro::invoke;

static flatbuffers::DetachedBuffer build_error_response(const char* msg) {
    flatbuffers::FlatBufferBuilder fbb(256);
    auto msg_off = fbb.CreateString(msg);
    PluginInvokeResponseBuilder rb(fbb);
    rb.add_status_code(1);
    rb.add_error_message(msg_off);
    fbb.Finish(rb.Finish());
    return fbb.Release();
}

static flatbuffers::DetachedBuffer build_bytes_response(
    const uint8_t* data, size_t len)
{
    flatbuffers::FlatBufferBuilder fbb(len + 512);
    auto arena_vec = fbb.CreateVector(data, len);
    using namespace orbpro::stream;
    TypedArenaBufferBuilder tb(fbb);
    tb.add_offset(0);
    tb.add_size(static_cast<uint32_t>(len));
    auto frame_off = tb.Finish();
    auto frames_vec = fbb.CreateVector(&frame_off, 1);
    PluginInvokeResponseBuilder rb(fbb);
    rb.add_status_code(0);
    rb.add_output_frames(frames_vec);
    rb.add_payload_arena(arena_vec);
    fbb.Finish(rb.Finish());
    return fbb.Release();
}

// ── Method dispatch ───────────────────────────────────────────────────────────

static flatbuffers::DetachedBuffer handle_decrypt_artifact(
    const PluginInvokeRequest* req)
{
    const auto* frames = req->input_frames();
    if (!frames || frames->size() < 2) {
        return build_error_response("decrypt_artifact requires 2 input frames");
    }

    const auto* arena = req->payload_arena();
    if (!arena) {
        return build_error_response("missing payload arena");
    }

    // Frame 0: JSON envelope
    const auto* frame0 = frames->Get(0);
    uint32_t json_offset = frame0->offset();
    uint32_t json_size = frame0->size();
    if (json_size == 0 || json_offset + json_size > arena->size()) {
        return build_error_response("envelope JSON is empty");
    }
    const char* json_ptr = reinterpret_cast<const char*>(arena->data() + json_offset);

    // Frame 1: private key
    const auto* frame1 = frames->Get(1);
    uint32_t key_offset = frame1->offset();
    uint32_t key_size = frame1->size();
    if (key_size != 32 || key_offset + key_size > arena->size()) {
        return build_error_response("private key must be 32 bytes");
    }
    const uint8_t* priv_key = arena->data() + key_offset;

    auto res = decrypt_artifact(json_ptr, json_size, priv_key, key_size);
    if (!res.ok) {
        return build_error_response(res.error.c_str());
    }

    return build_bytes_response(res.plaintext.data(), res.plaintext.size());
}

// ── Plugin ABI exports ────────────────────────────────────────────────────────

extern "C" {

__attribute__((visibility("default")))
uint8_t* plugin_alloc(uint32_t size) {
    return static_cast<uint8_t*>(malloc(size));
}

__attribute__((visibility("default")))
void plugin_free(uint8_t* ptr, uint32_t /*size*/) {
    free(ptr);
}

__attribute__((visibility("default")))
uint8_t* plugin_invoke_stream(const uint8_t* req_ptr, uint32_t req_len,
                              uint32_t* out_len_ptr)
{
    if (!req_ptr || req_len == 0 || !out_len_ptr) {
        *out_len_ptr = 0;
        return nullptr;
    }

    flatbuffers::Verifier verifier(req_ptr, req_len);
    if (!VerifyPluginInvokeRequestBuffer(verifier)) {
        *out_len_ptr = 0;
        return nullptr;
    }
    const PluginInvokeRequest* req = GetPluginInvokeRequest(req_ptr);

    flatbuffers::DetachedBuffer response;
    const char* method = req->method_id() ? req->method_id()->c_str() : "";

    if (strcmp(method, "decrypt_artifact") == 0) {
        response = handle_decrypt_artifact(req);
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
