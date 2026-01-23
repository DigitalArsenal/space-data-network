// AUTO-GENERATED - DO NOT EDIT
// Build time: 2026-01-23T01:28:53.943Z
// Relay count: 3

#include <cstdint>
#include <cstring>
#include <string>

#include "edge-relays-data.h"

// ChaCha20 implementation (simplified for WASM)
static void chacha20_block(uint32_t output[16], const uint32_t input[16]) {
    for (int i = 0; i < 16; i++) output[i] = input[i];

    for (int i = 0; i < 10; i++) {
        // Quarter rounds
        #define QUARTERROUND(a,b,c,d) \
            a += b; d ^= a; d = (d << 16) | (d >> 16); \
            c += d; b ^= c; b = (b << 12) | (b >> 20); \
            a += b; d ^= a; d = (d << 8) | (d >> 24); \
            c += d; b ^= c; b = (b << 7) | (b >> 25);

        QUARTERROUND(output[0], output[4], output[8], output[12]);
        QUARTERROUND(output[1], output[5], output[9], output[13]);
        QUARTERROUND(output[2], output[6], output[10], output[14]);
        QUARTERROUND(output[3], output[7], output[11], output[15]);
        QUARTERROUND(output[0], output[5], output[10], output[15]);
        QUARTERROUND(output[1], output[6], output[11], output[12]);
        QUARTERROUND(output[2], output[7], output[8], output[13]);
        QUARTERROUND(output[3], output[4], output[9], output[14]);
    }

    for (int i = 0; i < 16; i++) output[i] += input[i];
}

static void chacha20_decrypt(uint8_t* output, const uint8_t* input, size_t len,
                             const uint8_t* key, const uint8_t* nonce) {
    uint32_t state[16] = {
        0x61707865, 0x3320646e, 0x79622d32, 0x6b206574,
        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
    };

    memcpy(&state[4], key, 32);
    memcpy(&state[14], nonce, 8);

    uint32_t keystream[16];
    for (size_t i = 0; i < len; i += 64) {
        state[12]++;
        chacha20_block(keystream, state);
        size_t chunk = (len - i < 64) ? (len - i) : 64;
        for (size_t j = 0; j < chunk; j++) {
            output[i + j] = input[i + j] ^ ((uint8_t*)keystream)[j];
        }
    }
}

static std::string cached_result;

extern "C" {
    const char* get_edge_relays() {
        if (!cached_result.empty()) {
            return cached_result.c_str();
        }

        // Deobfuscate key
        uint8_t key[32];
        for (int i = 0; i < 32; i++) {
            key[i] = KEY_MATERIAL[i] ^ KEY_MATERIAL[32 + i];
        }

        // Skip nonce (first 24 bytes) and decrypt
        const uint8_t* ciphertext = ENCRYPTED_RELAYS + 24;
        size_t ciphertext_len = ENCRYPTED_RELAYS_LEN - 24 - 16; // minus nonce and tag

        uint8_t* decrypted = new uint8_t[ciphertext_len + 1];
        chacha20_decrypt(decrypted, ciphertext, ciphertext_len, key, ENCRYPTED_RELAYS);
        decrypted[ciphertext_len] = 0;

        cached_result = std::string((char*)decrypted);
        delete[] decrypted;

        // Clear key from memory
        memset(key, 0, 32);

        return cached_result.c_str();
    }

    int get_relay_count() {
        const char* json = get_edge_relays();
        int count = 0;
        int slashes = 0;
        for (const char* p = json; *p; p++) {
            if (*p == '/') slashes++;
            if (*p == '"' && slashes > 0) {
                count++;
                slashes = 0;
            }
        }
        return count;
    }
}
