//go:build wasip1

// Package main provides a WASI-compatible SDN module.
// This module can run in any WASI-compliant runtime (Wasmtime, Wasmer, WasmEdge).
//
// The module exports functions for:
// - Schema validation
// - FlatBuffer conversion
// - Cryptographic operations
// - Message processing
//
// Network I/O is handled by the host runtime via imported functions.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"unsafe"
)

// ============================================
// Host Imports (provided by runtime)
// ============================================

// These functions are imported from the host environment.
// The host runtime must provide implementations.

//go:wasmimport env host_log
func hostLog(ptr, len uint32)

//go:wasmimport env host_send_message
func hostSendMessage(topicPtr, topicLen, dataPtr, dataLen uint32) uint32

//go:wasmimport env host_subscribe
func hostSubscribe(topicPtr, topicLen uint32) uint32

//go:wasmimport env host_get_peer_id
func hostGetPeerID(bufPtr, bufLen uint32) uint32

//go:wasmimport env host_store_data
func hostStoreData(schemaPtr, schemaLen, dataPtr, dataLen uint32) uint64

//go:wasmimport env host_load_data
func hostLoadData(cidPtr, cidLen, bufPtr, bufLen uint32) uint32

// ============================================
// Memory Management (for host interaction)
// ============================================

// Shared buffer for host communication
var sharedBuffer = make([]byte, 65536)

//export sdn_alloc
func sdnAlloc(size uint32) uint32 {
	if int(size) > len(sharedBuffer) {
		sharedBuffer = make([]byte, size)
	}
	return uint32(uintptr(unsafe.Pointer(&sharedBuffer[0])))
}

//export sdn_free
func sdnFree(ptr uint32) {
	// No-op for now, using shared buffer
}

//export sdn_get_buffer_ptr
func sdnGetBufferPtr() uint32 {
	return uint32(uintptr(unsafe.Pointer(&sharedBuffer[0])))
}

//export sdn_get_buffer_len
func sdnGetBufferLen() uint32 {
	return uint32(len(sharedBuffer))
}

// ============================================
// Schema Registry
// ============================================

var schemas = map[string][]byte{}
var schemaIDs = map[string]int{}
var nextSchemaID = 1

//export sdn_register_schema
func sdnRegisterSchema(namePtr, nameLen, contentPtr, contentLen uint32) int32 {
	name := string(getBytes(namePtr, nameLen))
	content := getBytes(contentPtr, contentLen)

	schemas[name] = make([]byte, len(content))
	copy(schemas[name], content)

	schemaIDs[name] = nextSchemaID
	nextSchemaID++

	log(fmt.Sprintf("Registered schema: %s (id=%d)", name, schemaIDs[name]))
	return int32(schemaIDs[name])
}

//export sdn_get_schema_id
func sdnGetSchemaID(namePtr, nameLen uint32) int32 {
	name := string(getBytes(namePtr, nameLen))
	if id, ok := schemaIDs[name]; ok {
		return int32(id)
	}
	return -1
}

//export sdn_list_schemas
func sdnListSchemas() uint32 {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	data, _ := json.Marshal(names)
	copy(sharedBuffer, data)
	return uint32(len(data))
}

// ============================================
// Validation
// ============================================

//export sdn_validate
func sdnValidate(schemaID int32, dataPtr, dataLen uint32) int32 {
	// Basic validation - in production, use FlatBuffer verifier
	data := getBytes(dataPtr, dataLen)

	if len(data) < 4 {
		return -1 // Too short for FlatBuffer
	}

	// Check FlatBuffer magic bytes or minimum structure
	// For now, just check non-empty
	if len(data) == 0 {
		return -1
	}

	return 0 // Valid
}

// ============================================
// Message Processing
// ============================================

// MaxQueueSize is the maximum number of messages that can be queued
const MaxQueueSize = 1000

// Message represents an SDS message
type Message struct {
	Schema    string `json:"schema"`
	Data      []byte `json:"data"`
	Signature []byte `json:"signature"`
	From      string `json:"from"`
}

var messageQueue = make([]Message, 0, 100)

//export sdn_process_message
func sdnProcessMessage(schemaPtr, schemaLen, dataPtr, dataLen, sigPtr, sigLen, fromPtr, fromLen uint32) int32 {
	schema := string(getBytes(schemaPtr, schemaLen))
	data := getBytes(dataPtr, dataLen)
	sig := getBytes(sigPtr, sigLen)
	from := string(getBytes(fromPtr, fromLen))

	// Check queue size limit
	if len(messageQueue) >= MaxQueueSize {
		log(fmt.Sprintf("Message queue full (max=%d), rejecting message", MaxQueueSize))
		return -3
	}

	// Validate
	schemaID, ok := schemaIDs[schema]
	if !ok {
		log(fmt.Sprintf("Unknown schema: %s", schema))
		return -1
	}

	if sdnValidate(int32(schemaID), dataPtr, dataLen) != 0 {
		log(fmt.Sprintf("Validation failed for schema: %s", schema))
		return -2
	}

	// Queue message
	msg := Message{
		Schema:    schema,
		Data:      make([]byte, len(data)),
		Signature: make([]byte, len(sig)),
		From:      from,
	}
	copy(msg.Data, data)
	copy(msg.Signature, sig)
	messageQueue = append(messageQueue, msg)

	// Store via host
	hostStoreData(schemaPtr, schemaLen, dataPtr, dataLen)

	log(fmt.Sprintf("Processed message: schema=%s, from=%s, size=%d", schema, from, len(data)))
	return 0
}

//export sdn_get_message_count
func sdnGetMessageCount() uint32 {
	return uint32(len(messageQueue))
}

//export sdn_get_message
func sdnGetMessage(index uint32) uint32 {
	if int(index) >= len(messageQueue) {
		return 0
	}

	data, _ := json.Marshal(messageQueue[index])
	copy(sharedBuffer, data)
	return uint32(len(data))
}

//export sdn_clear_messages
func sdnClearMessages() {
	messageQueue = messageQueue[:0]
}

// ============================================
// Publishing
// ============================================

//export sdn_publish
func sdnPublish(schemaPtr, schemaLen, dataPtr, dataLen uint32) int32 {
	schema := string(getBytes(schemaPtr, schemaLen))

	// Validate first
	schemaID, ok := schemaIDs[schema]
	if !ok {
		return -1
	}

	if sdnValidate(int32(schemaID), dataPtr, dataLen) != 0 {
		return -2
	}

	// Build topic name
	topic := "/spacedatanetwork/sds/" + schema
	topicBytes := []byte(topic)

	// Send via host
	result := hostSendMessage(
		uint32(uintptr(unsafe.Pointer(&topicBytes[0]))), uint32(len(topicBytes)),
		dataPtr, dataLen,
	)

	if result != 0 {
		return -3
	}

	return 0
}

// ============================================
// Subscription
// ============================================

var subscriptions = make(map[string]bool)

//export sdn_subscribe
func sdnSubscribe(schemaPtr, schemaLen uint32) int32 {
	schema := string(getBytes(schemaPtr, schemaLen))

	if _, ok := schemaIDs[schema]; !ok {
		return -1 // Unknown schema
	}

	topic := "/spacedatanetwork/sds/" + schema
	topicBytes := []byte(topic)

	result := hostSubscribe(
		uint32(uintptr(unsafe.Pointer(&topicBytes[0]))), uint32(len(topicBytes)),
	)

	if result == 0 {
		subscriptions[schema] = true
		log(fmt.Sprintf("Subscribed to: %s", schema))
		return 0
	}

	return -2
}

//export sdn_is_subscribed
func sdnIsSubscribed(schemaPtr, schemaLen uint32) int32 {
	schema := string(getBytes(schemaPtr, schemaLen))
	if subscriptions[schema] {
		return 1
	}
	return 0
}

// ============================================
// Cryptography
// ============================================

//export sdn_hash_sha256
func sdnHashSHA256(dataPtr, dataLen uint32) uint32 {
	data := getBytes(dataPtr, dataLen)

	// Use Go's standard crypto/sha256 package for proper hashing
	hash := sha256.Sum256(data)

	copy(sharedBuffer, hash[:])
	return 32 // SHA256 output is 32 bytes
}

//export sdn_verify_signature
func sdnVerifySignature(pubKeyPtr, pubKeyLen, msgPtr, msgLen, sigPtr, sigLen uint32) int32 {
	// Ed25519 signature verification using Go's standard crypto/ed25519 package
	pubKey := getBytes(pubKeyPtr, pubKeyLen)
	msg := getBytes(msgPtr, msgLen)
	sig := getBytes(sigPtr, sigLen)

	// Ed25519 public keys are 32 bytes, signatures are 64 bytes
	if len(pubKey) != ed25519.PublicKeySize {
		return -1 // Invalid public key length
	}
	if len(sig) != ed25519.SignatureSize {
		return -2 // Invalid signature length
	}

	// Verify the signature using ed25519
	if ed25519.Verify(pubKey, msg, sig) {
		return 0 // Signature is valid
	}
	return -3 // Signature verification failed
}

// ============================================
// Utilities
// ============================================

func getBytes(ptr, len uint32) []byte {
	if len == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), len)
}

func log(msg string) {
	msgBytes := []byte(msg)
	hostLog(uint32(uintptr(unsafe.Pointer(&msgBytes[0]))), uint32(len(msgBytes)))
}

// ============================================
// Initialization
// ============================================

//export sdn_init
func sdnInit() int32 {
	log("SDN WASI module initialized")

	// Register default schemas
	defaultSchemas := []string{
		"EPM.fbs", "PNM.fbs", "OMM.fbs", "OEM.fbs", "CDM.fbs",
		"CAT.fbs", "CSM.fbs", "LDM.fbs", "IDM.fbs", "PLD.fbs",
	}

	for _, schema := range defaultSchemas {
		schemaBytes := []byte(schema)
		sdnRegisterSchema(
			uint32(uintptr(unsafe.Pointer(&schemaBytes[0]))), uint32(len(schemaBytes)),
			0, 0, // No content yet
		)
	}

	return 0
}

//export sdn_version
func sdnVersion() uint32 {
	version := "sdn-wasi/1.0.0"
	copy(sharedBuffer, version)
	return uint32(len(version))
}

// RPCRequest represents a JSON-RPC style request
type RPCRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	ID     int             `json:"id"`
}

// RPCResponse represents a JSON-RPC style response
type RPCResponse struct {
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
	ID     int         `json:"id"`
}

// handleRPC processes a JSON-RPC request
func handleRPC(req RPCRequest) RPCResponse {
	switch req.Method {
	case "version":
		length := sdnVersion()
		return RPCResponse{Result: string(sharedBuffer[:length]), ID: req.ID}

	case "list_schemas":
		length := sdnListSchemas()
		var names []string
		json.Unmarshal(sharedBuffer[:length], &names)
		return RPCResponse{Result: names, ID: req.ID}

	case "register_schema":
		var params struct {
			Name    string `json:"name"`
			Content string `json:"content,omitempty"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return RPCResponse{Error: err.Error(), ID: req.ID}
		}
		nameBytes := []byte(params.Name)
		contentBytes := []byte(params.Content)
		var contentPtr, contentLen uint32
		if len(contentBytes) > 0 {
			contentPtr = uint32(uintptr(unsafe.Pointer(&contentBytes[0])))
			contentLen = uint32(len(contentBytes))
		}
		id := sdnRegisterSchema(
			uint32(uintptr(unsafe.Pointer(&nameBytes[0]))), uint32(len(nameBytes)),
			contentPtr, contentLen,
		)
		return RPCResponse{Result: id, ID: req.ID}

	case "get_message_count":
		return RPCResponse{Result: sdnGetMessageCount(), ID: req.ID}

	case "clear_messages":
		sdnClearMessages()
		return RPCResponse{Result: "ok", ID: req.ID}

	default:
		return RPCResponse{Error: "unknown method: " + req.Method, ID: req.ID}
	}
}

// Main entry point (required for WASI)
func main() {
	// Initialize when running as standalone
	sdnInit()

	// Check for arguments to determine mode
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println("sdn-wasi/1.0.0")
		case "list-schemas":
			length := sdnListSchemas()
			fmt.Println(string(sharedBuffer[:length]))
		case "rpc":
			// JSON-RPC mode - read from stdin, write to stdout
			decoder := json.NewDecoder(os.Stdin)
			encoder := json.NewEncoder(os.Stdout)
			for {
				var req RPCRequest
				if err := decoder.Decode(&req); err != nil {
					break
				}
				resp := handleRPC(req)
				encoder.Encode(resp)
			}
		case "help":
			fmt.Println("SDN WASI Module")
			fmt.Println("Usage: sdn-wasi [command]")
			fmt.Println("")
			fmt.Println("Commands:")
			fmt.Println("  version       Show version")
			fmt.Println("  list-schemas  List registered schemas")
			fmt.Println("  rpc           JSON-RPC mode (stdin/stdout)")
			fmt.Println("  help          Show this help")
			fmt.Println("")
			fmt.Println("For full library support, build with TinyGo.")
		default:
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
			os.Exit(1)
		}
	} else {
		fmt.Fprintln(os.Stderr, "SDN WASI module initialized")
		fmt.Fprintln(os.Stderr, "Run with 'help' for usage or 'rpc' for JSON-RPC mode")
	}
}
