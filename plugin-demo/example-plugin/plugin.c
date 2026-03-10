/**
 * SDN Example Plugin — Annotated Reference Implementation
 *
 * This file demonstrates all the required WASM exports for an SDN plugin.
 * Compile with wasi-sdk:
 *
 *   /opt/wasi-sdk/bin/clang \
 *     --target=wasm32-wasi \
 *     -O2 -nostartfiles \
 *     -Wl,--no-entry \
 *     -Wl,--export=malloc \
 *     -Wl,--export=free \
 *     -Wl,--export=plugin_init \
 *     -Wl,--export=plugin_handle_request \
 *     -Wl,--export=plugin_get_public_key \
 *     -Wl,--export=plugin_get_metadata \
 *     -Wl,--export=plugin_request_challenge \
 *     -o plugin.wasm plugin.c
 *
 * The SDN runtime (Wazero) loads this module and calls the exported functions.
 * Communication uses FlatBuffer-encoded binary messages passed through shared
 * linear memory via malloc/free.
 */

#include <stdint.h>
#include <stddef.h>
#include <string.h>

/* =========================================================================
 * Host function imports — provided by the SDN runtime
 * ========================================================================= */

/* Returns current time in milliseconds since epoch. */
__attribute__((import_module("sdn"), import_name("clock_now_ms")))
extern int64_t sdn_clock_now_ms(void);

/* Fill buffer with cryptographically secure random bytes.
 * Returns 0 on success, -1 on failure. Max 8192 bytes per call. */
__attribute__((import_module("sdn"), import_name("random_bytes")))
extern int32_t sdn_random_bytes(uint8_t* buf, int32_t len);

/* Log a message to the SDN daemon log.
 * level: 0=debug, 1=info, 2=warn, 3=error */
__attribute__((import_module("sdn"), import_name("log")))
extern void sdn_log(int32_t level, const char* msg, int32_t msg_len);

/* =========================================================================
 * Simple memory allocator (bump allocator for demo purposes)
 *
 * Production plugins should use a proper allocator (e.g., dlmalloc from
 * wasi-libc). This simplified version demonstrates the contract.
 * ========================================================================= */

/* 1MB heap — SDN limits plugins to 32MB total (512 WASM pages). */
#define HEAP_SIZE (1024 * 1024)
static uint8_t heap[HEAP_SIZE];
static size_t heap_offset = 0;

void* malloc(size_t size) {
    /* Align to 8 bytes */
    size = (size + 7) & ~7;
    if (heap_offset + size > HEAP_SIZE) {
        return (void*)0;  /* Out of memory */
    }
    void* ptr = &heap[heap_offset];
    heap_offset += size;
    return ptr;
}

void free(void* ptr) {
    /* Bump allocator doesn't free — production plugins should use dlmalloc */
    (void)ptr;
}

/* =========================================================================
 * Plugin state
 * ========================================================================= */

/* The identity seed provided by SDN during init (32 bytes Ed25519 seed) */
static uint8_t identity_seed[32];
static int32_t initialized = 0;

/* Simulated public key (in real plugin: derive from identity_seed) */
static uint8_t public_key[32];

/* Plugin metadata as JSON */
static const char METADATA[] =
    "{"
    "\"id\":\"example-sensor-plugin\","
    "\"version\":\"1.0.0\","
    "\"name\":\"Example Sensor Data Plugin\","
    "\"description\":\"Demonstrates the SDN WASM plugin API\","
    "\"protocols\":[\"/example/sensor-data/1.0.0\"],"
    "\"capabilities\":[\"publish\",\"subscribe\"]"
    "}";

/* =========================================================================
 * Helper: log a string to SDN daemon
 * ========================================================================= */

static void log_info(const char* msg) {
    int32_t len = 0;
    while (msg[len]) len++;
    sdn_log(1, msg, len);
}

static void log_error(const char* msg) {
    int32_t len = 0;
    while (msg[len]) len++;
    sdn_log(3, msg, len);
}

/* =========================================================================
 * Required export: plugin_init
 *
 * Called once after module load. Receives the node's identity seed (32 bytes)
 * which the plugin can use to derive signing/encryption keys.
 *
 * Returns: 0 on success, non-zero on failure
 * ========================================================================= */

__attribute__((export_name("plugin_init")))
int32_t plugin_init(uint8_t* seed_ptr, int32_t seed_len) {
    if (seed_len < 32) {
        log_error("plugin_init: seed too short");
        return -1;
    }

    /* Store the identity seed */
    memcpy(identity_seed, seed_ptr, 32);

    /* In a real plugin, you would derive keys here:
     *   - Ed25519 signing key from seed
     *   - P-256 ECDH key for key exchange
     *   - X25519 key for encryption
     *
     * For this demo, we just copy the seed as a "public key" placeholder.
     */
    memcpy(public_key, seed_ptr, 32);

    /* Use host functions to log initialization */
    log_info("example-sensor-plugin initialized");

    /* Demonstrate random bytes */
    uint8_t nonce[16];
    if (sdn_random_bytes(nonce, 16) != 0) {
        log_error("failed to get random bytes");
        return -2;
    }

    /* Demonstrate clock */
    int64_t now_ms = sdn_clock_now_ms();
    (void)now_ms;  /* Would use for timestamp in real plugin */

    initialized = 1;
    return 0;
}

/* =========================================================================
 * Required export: plugin_handle_request
 *
 * Called when a request arrives (via HTTP bridge or libp2p stream bridge).
 * The request is a FlatBuffer binary blob. The response should also be
 * a FlatBuffer binary blob written to out_ptr.
 *
 * Parameters:
 *   req_ptr, req_len   — input request data (FlatBuffer bytes)
 *   out_ptr, out_cap   — output buffer allocated by host via malloc()
 *
 * Returns: number of bytes written to out_ptr, or negative on error
 *
 * IMPORTANT: The FlatBuffer bytes include the 4-byte file identifier at
 * offset 4-7. Check this to dispatch different message types.
 * ========================================================================= */

__attribute__((export_name("plugin_handle_request")))
int32_t plugin_handle_request(
    uint8_t* req_ptr, int32_t req_len,
    uint8_t* out_ptr, int32_t out_cap
) {
    if (!initialized) {
        log_error("plugin_handle_request: not initialized");
        return -1;
    }

    if (req_len < 8) {
        log_error("plugin_handle_request: request too small for FlatBuffer");
        return -2;
    }

    /* ── Read the file identifier (bytes 4-7) to determine message type ── */
    char file_id[5] = {
        (char)req_ptr[4], (char)req_ptr[5],
        (char)req_ptr[6], (char)req_ptr[7], '\0'
    };

    log_info("handling request with file_id");

    /* ── Example: echo back a simple response ──
     *
     * In a real plugin, you would:
     * 1. Parse the request FlatBuffer (use flatcc or manual offset reading)
     * 2. Process the request (key exchange, data transform, etc.)
     * 3. Build a response FlatBuffer
     * 4. Write it to out_ptr
     *
     * For this demo, we write a minimal valid FlatBuffer response:
     */

    /* Minimal FlatBuffer: 4-byte root offset + 4-byte file_id + vtable */
    /* This is a placeholder — real plugins build proper FlatBuffers */
    if (out_cap < 24) {
        return -3;  /* Output buffer too small */
    }

    /* Write a minimal "ack" response: just the file_id back */
    memset(out_ptr, 0, 24);
    /* Root table offset at position 0 (points to offset 12) */
    out_ptr[0] = 12;
    /* File identifier at bytes 4-7 */
    out_ptr[4] = 'A'; out_ptr[5] = 'C'; out_ptr[6] = 'K'; out_ptr[7] = '!';
    /* Minimal vtable at offset 8: vtable size=4, table size=4 */
    out_ptr[8] = 4; out_ptr[10] = 4;
    /* Table at offset 12: vtable offset (negative) */
    out_ptr[12] = 4;  /* Points back 4 bytes to vtable at offset 8 */

    return 24;
}

/* =========================================================================
 * Required export: plugin_get_public_key
 *
 * Returns the plugin's public key (32 bytes for Ed25519, 33/65 for ECDSA).
 * The host uses this to verify plugin identity and for key exchange protocols.
 *
 * Returns: number of bytes written to out_ptr, or negative on error
 * ========================================================================= */

__attribute__((export_name("plugin_get_public_key")))
int32_t plugin_get_public_key(uint8_t* out_ptr, int32_t out_cap) {
    if (!initialized) {
        return -1;
    }
    if (out_cap < 32) {
        return -2;
    }
    memcpy(out_ptr, public_key, 32);
    return 32;
}

/* =========================================================================
 * Required export: plugin_get_metadata
 *
 * Returns JSON metadata describing the plugin. The host uses this for:
 *   - Plugin manifest (/api/v1/plugins/manifest)
 *   - UI display (name, icon, description)
 *   - Protocol registration (which protocols to announce)
 *
 * Returns: number of bytes written to out_ptr, or negative on error
 * ========================================================================= */

__attribute__((export_name("plugin_get_metadata")))
int32_t plugin_get_metadata(uint8_t* out_ptr, int32_t out_cap) {
    int32_t len = sizeof(METADATA) - 1;  /* Exclude null terminator */
    if (out_cap < len) {
        return -1;
    }
    memcpy(out_ptr, METADATA, len);
    return len;
}

/* =========================================================================
 * Required export: plugin_request_challenge
 *
 * Implements challenge-response authentication. The host sends a challenge
 * (random bytes), and the plugin signs it with its private key to prove
 * identity.
 *
 * For key-broker plugins, this is used during the ECDH key exchange:
 *   1. Client sends challenge via /orbpro/challenge/1.0.0
 *   2. Host calls plugin_request_challenge with the challenge bytes
 *   3. Plugin signs and returns the signature
 *
 * Returns: number of bytes written to out_ptr, or negative on error
 * ========================================================================= */

__attribute__((export_name("plugin_request_challenge")))
int32_t plugin_request_challenge(
    uint8_t* req_ptr, int32_t req_len,
    uint8_t* out_ptr, int32_t out_cap
) {
    if (!initialized) {
        return -1;
    }

    /* In a real plugin, you would:
     * 1. Parse the challenge from req_ptr
     * 2. Sign the challenge with the plugin's Ed25519 private key
     * 3. Write the 64-byte signature to out_ptr
     *
     * For this demo, we XOR the challenge with the seed as a placeholder.
     */
    if (out_cap < req_len) {
        return -2;
    }

    for (int32_t i = 0; i < req_len; i++) {
        out_ptr[i] = req_ptr[i] ^ identity_seed[i % 32];
    }

    return req_len;
}
