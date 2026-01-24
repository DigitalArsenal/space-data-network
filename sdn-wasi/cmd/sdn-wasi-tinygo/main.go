//go:build tinygo

// Package main provides a WASI-compatible SDN module for TinyGo.
// TinyGo properly supports //export for WASI targets.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"unsafe"
)

// ============================================
// Host Imports (provided by runtime)
// ============================================

//go:wasm-module env
//export host_log
func hostLog(ptr, length uint32)

//go:wasm-module env
//export host_send_message
func hostSendMessage(topicPtr, topicLen, dataPtr, dataLen uint32) uint32

//go:wasm-module env
//export host_subscribe
func hostSubscribe(topicPtr, topicLen uint32) uint32

//go:wasm-module env
//export host_get_peer_id
func hostGetPeerID(bufPtr, bufLen uint32) uint32

//go:wasm-module env
//export host_store_data
func hostStoreData(schemaPtr, schemaLen, dataPtr, dataLen uint32) uint64

//go:wasm-module env
//export host_load_data
func hostLoadData(cidPtr, cidLen, bufPtr, bufLen uint32) uint32

// ============================================
// Memory Management
// ============================================

var sharedBuffer [65536]byte
var sharedBufferLen uint32 = 65536

//export sdn_alloc
func sdnAlloc(size uint32) uint32 {
	return uint32(uintptr(unsafe.Pointer(&sharedBuffer[0])))
}

//export sdn_free
func sdnFree(ptr uint32) {}

//export sdn_get_buffer_ptr
func sdnGetBufferPtr() uint32 {
	return uint32(uintptr(unsafe.Pointer(&sharedBuffer[0])))
}

//export sdn_get_buffer_len
func sdnGetBufferLen() uint32 {
	return sharedBufferLen
}

// ============================================
// Schema Registry
// ============================================

type schemaEntry struct {
	name    string
	content []byte
	id      int32
}

var schemas = make([]schemaEntry, 0, 32)
var nextSchemaID int32 = 1

//export sdn_register_schema
func sdnRegisterSchema(namePtr, nameLen, contentPtr, contentLen uint32) int32 {
	name := getString(namePtr, nameLen)
	content := getBytes(contentPtr, contentLen)

	// Check if exists
	for i := range schemas {
		if schemas[i].name == name {
			return schemas[i].id
		}
	}

	entry := schemaEntry{
		name:    name,
		content: make([]byte, len(content)),
		id:      nextSchemaID,
	}
	copy(entry.content, content)
	schemas = append(schemas, entry)
	nextSchemaID++

	logMsg("Registered schema: " + name)
	return entry.id
}

//export sdn_get_schema_id
func sdnGetSchemaID(namePtr, nameLen uint32) int32 {
	name := getString(namePtr, nameLen)
	for _, s := range schemas {
		if s.name == name {
			return s.id
		}
	}
	return -1
}

//export sdn_list_schemas
func sdnListSchemas() uint32 {
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = s.name
	}
	data, _ := json.Marshal(names)
	copy(sharedBuffer[:], data)
	return uint32(len(data))
}

// ============================================
// Validation
// ============================================

//export sdn_validate
func sdnValidate(schemaID int32, dataPtr, dataLen uint32) int32 {
	if dataLen < 4 {
		return -1
	}
	return 0
}

// ============================================
// Message Processing
// ============================================

// MaxQueueSize is the maximum number of messages that can be queued
const MaxQueueSize = 1000

type Message struct {
	Schema    string `json:"schema"`
	Data      []byte `json:"data"`
	Signature []byte `json:"signature"`
	From      string `json:"from"`
}

var messageQueue = make([]Message, 0, 100)

//export sdn_process_message
func sdnProcessMessage(schemaPtr, schemaLen, dataPtr, dataLen, sigPtr, sigLen, fromPtr, fromLen uint32) int32 {
	schema := getString(schemaPtr, schemaLen)
	data := getBytes(dataPtr, dataLen)
	sig := getBytes(sigPtr, sigLen)
	from := getString(fromPtr, fromLen)

	// Check queue size limit
	if len(messageQueue) >= MaxQueueSize {
		logMsg("Message queue full, rejecting message")
		return -3
	}

	// Validate schema exists
	schemaID := sdnGetSchemaID(schemaPtr, schemaLen)
	if schemaID < 0 {
		return -1
	}

	if sdnValidate(schemaID, dataPtr, dataLen) != 0 {
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
	copy(sharedBuffer[:], data)
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
	schema := getString(schemaPtr, schemaLen)

	schemaID := sdnGetSchemaID(schemaPtr, schemaLen)
	if schemaID < 0 {
		return -1
	}

	if sdnValidate(schemaID, dataPtr, dataLen) != 0 {
		return -2
	}

	topic := "/spacedatanetwork/sds/" + schema
	topicBytes := []byte(topic)
	topicPtr := uint32(uintptr(unsafe.Pointer(&topicBytes[0])))

	result := hostSendMessage(topicPtr, uint32(len(topicBytes)), dataPtr, dataLen)
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
	schema := getString(schemaPtr, schemaLen)

	if sdnGetSchemaID(schemaPtr, schemaLen) < 0 {
		return -1
	}

	topic := "/spacedatanetwork/sds/" + schema
	topicBytes := []byte(topic)
	topicPtr := uint32(uintptr(unsafe.Pointer(&topicBytes[0])))

	result := hostSubscribe(topicPtr, uint32(len(topicBytes)))
	if result == 0 {
		subscriptions[schema] = true
		return 0
	}

	return -2
}

//export sdn_is_subscribed
func sdnIsSubscribed(schemaPtr, schemaLen uint32) int32 {
	schema := getString(schemaPtr, schemaLen)
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

	copy(sharedBuffer[:], hash[:])
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

func getBytes(ptr, length uint32) []byte {
	if length == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
}

func getString(ptr, length uint32) string {
	if length == 0 {
		return ""
	}
	return string(getBytes(ptr, length))
}

func logMsg(msg string) {
	msgBytes := []byte(msg)
	hostLog(uint32(uintptr(unsafe.Pointer(&msgBytes[0]))), uint32(len(msgBytes)))
}

// ============================================
// Initialization
// ============================================

//export sdn_init
func sdnInit() int32 {
	logMsg("SDN WASI module initialized (TinyGo)")

	defaultSchemas := []string{
		"EPM.fbs", "PNM.fbs", "OMM.fbs", "OEM.fbs", "CDM.fbs",
		"CAT.fbs", "CSM.fbs", "LDM.fbs", "IDM.fbs", "PLD.fbs",
	}

	for _, schema := range defaultSchemas {
		schemaBytes := []byte(schema)
		sdnRegisterSchema(
			uint32(uintptr(unsafe.Pointer(&schemaBytes[0]))), uint32(len(schemaBytes)),
			0, 0,
		)
	}

	return 0
}

//export sdn_version
func sdnVersion() uint32 {
	version := "sdn-wasi/1.0.0-tinygo"
	copy(sharedBuffer[:], []byte(version))
	return uint32(len(version))
}

// Main is required but we use init for setup
func main() {
	// TinyGo reactor modules don't need main to do anything
}

func init() {
	// Auto-initialize on module load
	sdnInit()
}
