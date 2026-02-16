/**
 * SDN Encrypted WASM Demo Module
 *
 * Simple computation functions exported from WASM to demonstrate
 * the OrbPro-style DRM key exchange and encrypted WASM loading.
 *
 * Compiled with: emcc demo.c -o demo.wasm -s STANDALONE_WASM=1 -s EXPORTED_FUNCTIONS=[...] --no-entry
 */

#include <stdint.h>

/* Version identifier embedded in the binary */
static const char VERSION[] = "sdn-demo-v1.0.0";

/* Simple addition */
__attribute__((export_name("demo_add")))
int32_t demo_add(int32_t a, int32_t b) {
    return a + b;
}

/* Multiplication */
__attribute__((export_name("demo_multiply")))
int32_t demo_multiply(int32_t a, int32_t b) {
    return a * b;
}

/* Fibonacci — demonstrates non-trivial computation */
__attribute__((export_name("demo_fibonacci")))
int32_t demo_fibonacci(int32_t n) {
    if (n <= 0) return 0;
    if (n == 1) return 1;
    int32_t a = 0, b = 1;
    for (int32_t i = 2; i <= n; i++) {
        int32_t tmp = a + b;
        a = b;
        b = tmp;
    }
    return b;
}

/* Factorial (iterative) */
__attribute__((export_name("demo_factorial")))
int64_t demo_factorial(int32_t n) {
    if (n < 0) return -1;
    int64_t result = 1;
    for (int32_t i = 2; i <= n; i++) {
        result *= i;
    }
    return result;
}

/* Returns a pointer to the version string (caller reads from WASM memory) */
__attribute__((export_name("demo_version")))
const char* demo_version(void) {
    return VERSION;
}

/* Returns the length of the version string */
__attribute__((export_name("demo_version_len")))
int32_t demo_version_len(void) {
    int32_t len = 0;
    while (VERSION[len]) len++;
    return len;
}
