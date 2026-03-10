# SDN Plugin Build — requires wasi-sdk
# Install: https://github.com/WebAssembly/wasi-sdk/releases
#
# Usage:
#   make              # Build plugin.wasm
#   make clean        # Remove build artifacts
#   make WASI_SDK=/custom/path  # Override wasi-sdk location

WASI_SDK ?= /opt/wasi-sdk
CC := $(WASI_SDK)/bin/clang
TARGET := plugin.wasm
SRC := plugin.c

CFLAGS := \
	--target=wasm32-wasi \
	-O2 \
	-nostartfiles \
	-Wall -Wextra

LDFLAGS := \
	-Wl,--no-entry \
	-Wl,--export=malloc \
	-Wl,--export=free \
	-Wl,--export=plugin_init \
	-Wl,--export=plugin_handle_request \
	-Wl,--export=plugin_get_public_key \
	-Wl,--export=plugin_get_metadata \
	-Wl,--export=plugin_request_challenge

.PHONY: all clean

all: $(TARGET)

$(TARGET): $(SRC)
	$(CC) $(CFLAGS) $(LDFLAGS) -o $@ $<
	@echo "Built $(TARGET) ($$(wc -c < $(TARGET)) bytes)"

clean:
	rm -f $(TARGET)
