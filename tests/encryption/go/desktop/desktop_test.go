// Package desktop provides tests for desktop-to-desktop encrypted communication.
// This simulates Electron app (sdn-desktop) encrypted communication
// with large payload sizes (ephemeris data).
package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/tests/encryption/go/ecies"
)

// DesktopNode represents a simulated sdn-desktop Electron application
type DesktopNode struct {
	ID      string
	KeyPair *ecies.KeyPair
	Peers   map[string]*ecies.KeyPair

	// Simulated IPC channel for Electron
	MainToRenderer chan *Message
	RendererToMain chan *Message

	mu sync.Mutex
}

// Message represents a message in the desktop app
type Message struct {
	Type      string
	Schema    string
	Data      []byte
	Encrypted *ecies.EncryptedMessage
	From      string
	To        string
	Timestamp time.Time
}

// EphemerisDataBlock represents a block of ephemeris data (OEM format)
type EphemerisDataBlock struct {
	ObjectName     string            `json:"OBJECT_NAME"`
	ObjectID       string            `json:"OBJECT_ID"`
	CenterName     string            `json:"CENTER_NAME"`
	RefFrame       string            `json:"REF_FRAME"`
	TimeSystem     string            `json:"TIME_SYSTEM"`
	StartTime      string            `json:"START_TIME"`
	StopTime       string            `json:"STOP_TIME"`
	Interpolation  string            `json:"INTERPOLATION"`
	InterpolationDegree int          `json:"INTERPOLATION_DEGREE"`
	DataPoints     []StateVector     `json:"DATA_POINTS"`
	CovarianceData []CovarianceBlock `json:"COVARIANCE_DATA,omitempty"`
}

// StateVector represents a single ephemeris state vector
type StateVector struct {
	Epoch string  `json:"EPOCH"`
	X     float64 `json:"X"`
	Y     float64 `json:"Y"`
	Z     float64 `json:"Z"`
	XDot  float64 `json:"X_DOT"`
	YDot  float64 `json:"Y_DOT"`
	ZDot  float64 `json:"Z_DOT"`
}

// CovarianceBlock represents covariance data
type CovarianceBlock struct {
	Epoch    string    `json:"EPOCH"`
	RefFrame string    `json:"COV_REF_FRAME"`
	CXX      float64   `json:"CX_X"`
	CYX      float64   `json:"CY_X"`
	CYY      float64   `json:"CY_Y"`
	CZX      float64   `json:"CZ_X"`
	CZY      float64   `json:"CZ_Y"`
	CZZ      float64   `json:"CZ_Z"`
	CXdotX   float64   `json:"CX_DOT_X"`
	CXdotY   float64   `json:"CX_DOT_Y"`
	CXdotZ   float64   `json:"CX_DOT_Z"`
	CXdotXdot float64  `json:"CX_DOT_X_DOT"`
	CYdotX   float64   `json:"CY_DOT_X"`
	CYdotY   float64   `json:"CY_DOT_Y"`
	CYdotZ   float64   `json:"CY_DOT_Z"`
	CYdotXdot float64  `json:"CY_DOT_X_DOT"`
	CYdotYdot float64  `json:"CY_DOT_Y_DOT"`
	CZdotX   float64   `json:"CZ_DOT_X"`
	CZdotY   float64   `json:"CZ_DOT_Y"`
	CZdotZ   float64   `json:"CZ_DOT_Z"`
	CZdotXdot float64  `json:"CZ_DOT_X_DOT"`
	CZdotYdot float64  `json:"CZ_DOT_Y_DOT"`
	CZdotZdot float64  `json:"CZ_DOT_Z_DOT"`
}

// NewDesktopNode creates a new desktop node
func NewDesktopNode(id string) (*DesktopNode, error) {
	kp, err := ecies.GenerateKeyPair(ecies.CurveX25519)
	if err != nil {
		return nil, err
	}

	return &DesktopNode{
		ID:             id,
		KeyPair:        kp,
		Peers:          make(map[string]*ecies.KeyPair),
		MainToRenderer: make(chan *Message, 100),
		RendererToMain: make(chan *Message, 100),
	}, nil
}

// RegisterPeer registers a peer's public key
func (d *DesktopNode) RegisterPeer(peerID string, publicKey []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Peers[peerID] = &ecies.KeyPair{
		PublicKey: publicKey,
		CurveType: ecies.CurveX25519,
	}
}

// SendEncrypted sends an encrypted message to a peer
func (d *DesktopNode) SendEncrypted(peerID string, schema string, data []byte) (*Message, error) {
	d.mu.Lock()
	peer, ok := d.Peers[peerID]
	d.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("peer %s not registered", peerID)
	}

	encrypted, err := ecies.Encrypt(peer.PublicKey, data, ecies.CurveX25519)
	if err != nil {
		return nil, err
	}

	return &Message{
		Type:      "encrypted",
		Schema:    schema,
		Encrypted: encrypted,
		From:      d.ID,
		To:        peerID,
		Timestamp: time.Now(),
	}, nil
}

// ReceiveEncrypted decrypts a received message
func (d *DesktopNode) ReceiveEncrypted(msg *Message) ([]byte, error) {
	return ecies.Decrypt(d.KeyPair.PrivateKey, msg.Encrypted)
}

// GenerateLargeEphemeris generates a large ephemeris data block for testing
func GenerateLargeEphemeris(objectName string, durationDays int, intervalSeconds int) *EphemerisDataBlock {
	totalPoints := (durationDays * 24 * 60 * 60) / intervalSeconds
	startTime := time.Now()

	ephemeris := &EphemerisDataBlock{
		ObjectName:          objectName,
		ObjectID:            "TEST-SAT-001",
		CenterName:          "EARTH",
		RefFrame:            "ICRF",
		TimeSystem:          "UTC",
		StartTime:           startTime.Format(time.RFC3339),
		StopTime:            startTime.Add(time.Duration(durationDays) * 24 * time.Hour).Format(time.RFC3339),
		Interpolation:       "HERMITE",
		InterpolationDegree: 7,
		DataPoints:          make([]StateVector, totalPoints),
	}

	// Generate realistic orbital data (simplified circular orbit)
	semiMajorAxis := 6778.0 // km (ISS altitude)
	period := 2 * math.Pi * math.Sqrt(math.Pow(semiMajorAxis*1000, 3)/(3.986004418e14))
	meanMotion := 2 * math.Pi / period

	for i := 0; i < totalPoints; i++ {
		t := float64(i * intervalSeconds)
		angle := meanMotion * t

		ephemeris.DataPoints[i] = StateVector{
			Epoch: startTime.Add(time.Duration(i*intervalSeconds) * time.Second).Format(time.RFC3339),
			X:     semiMajorAxis * math.Cos(angle),
			Y:     semiMajorAxis * math.Sin(angle),
			Z:     0,
			XDot:  -semiMajorAxis * meanMotion * math.Sin(angle) / 1000, // km/s
			YDot:  semiMajorAxis * meanMotion * math.Cos(angle) / 1000,
			ZDot:  0,
		}
	}

	return ephemeris
}

// GenerateEphemerisWithCovariance generates ephemeris with covariance data
func GenerateEphemerisWithCovariance(objectName string, durationDays int, intervalSeconds int) *EphemerisDataBlock {
	ephemeris := GenerateLargeEphemeris(objectName, durationDays, intervalSeconds)

	// Add covariance data (typically at sparse intervals)
	covarianceInterval := 3600 // Every hour
	covariancePoints := (durationDays * 24 * 60 * 60) / covarianceInterval
	startTime := time.Now()

	ephemeris.CovarianceData = make([]CovarianceBlock, covariancePoints)
	for i := 0; i < covariancePoints; i++ {
		ephemeris.CovarianceData[i] = CovarianceBlock{
			Epoch:     startTime.Add(time.Duration(i*covarianceInterval) * time.Second).Format(time.RFC3339),
			RefFrame:  "RTN",
			CXX:       1.0e6 + float64(i)*100,
			CYX:       1.0e4,
			CYY:       5.0e6,
			CZX:       1.0e3,
			CZY:       1.0e4,
			CZZ:       1.0e6,
			CXdotX:    1.0,
			CXdotY:    0.5,
			CXdotZ:    0.1,
			CXdotXdot: 1.0e-4,
			CYdotX:    0.5,
			CYdotY:    2.0,
			CYdotZ:    0.2,
			CYdotXdot: 0.5e-4,
			CYdotYdot: 2.0e-4,
			CZdotX:    0.1,
			CZdotY:    0.2,
			CZdotZ:    1.5,
			CZdotXdot: 0.1e-4,
			CZdotYdot: 0.2e-4,
			CZdotZdot: 1.5e-4,
		}
	}

	return ephemeris
}

func TestDesktopToDesktopEncryption(t *testing.T) {
	// Create two desktop nodes
	desktop1, err := NewDesktopNode("desktop-1")
	if err != nil {
		t.Fatalf("Failed to create desktop1: %v", err)
	}

	desktop2, err := NewDesktopNode("desktop-2")
	if err != nil {
		t.Fatalf("Failed to create desktop2: %v", err)
	}

	// Register each other as peers
	desktop1.RegisterPeer(desktop2.ID, desktop2.KeyPair.PublicKey)
	desktop2.RegisterPeer(desktop1.ID, desktop1.KeyPair.PublicKey)

	// Test small message
	smallData := []byte(`{"test": "small message"}`)
	msg, err := desktop1.SendEncrypted(desktop2.ID, "OMM", smallData)
	if err != nil {
		t.Fatalf("Failed to encrypt small message: %v", err)
	}

	decrypted, err := desktop2.ReceiveEncrypted(msg)
	if err != nil {
		t.Fatalf("Failed to decrypt small message: %v", err)
	}

	if !bytes.Equal(decrypted, smallData) {
		t.Error("Small message decryption mismatch")
	}
}

func TestLargeEphemerisEncryption(t *testing.T) {
	desktop1, _ := NewDesktopNode("desktop-1")
	desktop2, _ := NewDesktopNode("desktop-2")

	desktop1.RegisterPeer(desktop2.ID, desktop2.KeyPair.PublicKey)
	desktop2.RegisterPeer(desktop1.ID, desktop1.KeyPair.PublicKey)

	testCases := []struct {
		name         string
		durationDays int
		intervalSecs int
	}{
		{"1-day-1min", 1, 60},
		{"7-day-1min", 7, 60},
		{"14-day-5min", 14, 300},
		{"30-day-10min", 30, 600},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ephemeris := GenerateLargeEphemeris("TEST-SAT", tc.durationDays, tc.intervalSecs)

			data, err := json.Marshal(ephemeris)
			if err != nil {
				t.Fatalf("Failed to marshal ephemeris: %v", err)
			}

			dataSize := float64(len(data)) / 1024 / 1024
			t.Logf("Ephemeris size: %.2f MB (%d data points)", dataSize, len(ephemeris.DataPoints))

			// Encrypt
			startEncrypt := time.Now()
			msg, err := desktop1.SendEncrypted(desktop2.ID, "OEM", data)
			encryptDuration := time.Since(startEncrypt)
			if err != nil {
				t.Fatalf("Encryption failed: %v", err)
			}
			t.Logf("Encryption time: %v (%.2f MB/s)", encryptDuration,
				dataSize/(encryptDuration.Seconds()))

			// Decrypt
			startDecrypt := time.Now()
			decrypted, err := desktop2.ReceiveEncrypted(msg)
			decryptDuration := time.Since(startDecrypt)
			if err != nil {
				t.Fatalf("Decryption failed: %v", err)
			}
			t.Logf("Decryption time: %v (%.2f MB/s)", decryptDuration,
				dataSize/(decryptDuration.Seconds()))

			// Verify
			var decryptedEphemeris EphemerisDataBlock
			if err := json.Unmarshal(decrypted, &decryptedEphemeris); err != nil {
				t.Fatalf("Failed to unmarshal decrypted ephemeris: %v", err)
			}

			if len(decryptedEphemeris.DataPoints) != len(ephemeris.DataPoints) {
				t.Errorf("Data point count mismatch: got %d, want %d",
					len(decryptedEphemeris.DataPoints), len(ephemeris.DataPoints))
			}

			// Verify a few data points
			for i := 0; i < min(10, len(ephemeris.DataPoints)); i++ {
				if ephemeris.DataPoints[i].X != decryptedEphemeris.DataPoints[i].X {
					t.Errorf("Data point %d X mismatch", i)
				}
			}
		})
	}
}

func TestEphemerisWithCovarianceEncryption(t *testing.T) {
	desktop1, _ := NewDesktopNode("desktop-1")
	desktop2, _ := NewDesktopNode("desktop-2")

	desktop1.RegisterPeer(desktop2.ID, desktop2.KeyPair.PublicKey)
	desktop2.RegisterPeer(desktop1.ID, desktop1.KeyPair.PublicKey)

	// Generate 7-day ephemeris with covariance
	ephemeris := GenerateEphemerisWithCovariance("ISS", 7, 60)

	data, err := json.Marshal(ephemeris)
	if err != nil {
		t.Fatalf("Failed to marshal ephemeris: %v", err)
	}

	dataSize := float64(len(data)) / 1024 / 1024
	t.Logf("Ephemeris with covariance size: %.2f MB", dataSize)
	t.Logf("State vectors: %d, Covariance blocks: %d",
		len(ephemeris.DataPoints), len(ephemeris.CovarianceData))

	// Encrypt and decrypt
	msg, err := desktop1.SendEncrypted(desktop2.ID, "OEM", data)
	if err != nil {
		t.Fatalf("Encryption failed: %v", err)
	}

	decrypted, err := desktop2.ReceiveEncrypted(msg)
	if err != nil {
		t.Fatalf("Decryption failed: %v", err)
	}

	// Verify
	var decryptedEphemeris EphemerisDataBlock
	if err := json.Unmarshal(decrypted, &decryptedEphemeris); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if len(decryptedEphemeris.CovarianceData) != len(ephemeris.CovarianceData) {
		t.Errorf("Covariance data count mismatch: got %d, want %d",
			len(decryptedEphemeris.CovarianceData), len(ephemeris.CovarianceData))
	}
}

func TestBidirectionalDesktopCommunication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	desktop1, _ := NewDesktopNode("desktop-1")
	desktop2, _ := NewDesktopNode("desktop-2")

	desktop1.RegisterPeer(desktop2.ID, desktop2.KeyPair.PublicKey)
	desktop2.RegisterPeer(desktop1.ID, desktop1.KeyPair.PublicKey)

	var wg sync.WaitGroup
	var received1, received2 [][]byte
	var mu sync.Mutex

	// Desktop 1 sends messages
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			data := []byte(fmt.Sprintf(`{"from": "desktop-1", "seq": %d}`, i))
			msg, _ := desktop1.SendEncrypted(desktop2.ID, "OMM", data)
			select {
			case desktop2.MainToRenderer <- msg:
			case <-ctx.Done():
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Desktop 2 sends messages
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			data := []byte(fmt.Sprintf(`{"from": "desktop-2", "seq": %d}`, i))
			msg, _ := desktop2.SendEncrypted(desktop1.ID, "CDM", data)
			select {
			case desktop1.MainToRenderer <- msg:
			case <-ctx.Done():
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Desktop 1 receives
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case msg := <-desktop1.MainToRenderer:
				decrypted, _ := desktop1.ReceiveEncrypted(msg)
				mu.Lock()
				received1 = append(received1, decrypted)
				mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Desktop 2 receives
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case msg := <-desktop2.MainToRenderer:
				decrypted, _ := desktop2.ReceiveEncrypted(msg)
				mu.Lock()
				received2 = append(received2, decrypted)
				mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Wait for sending to complete
	time.Sleep(500 * time.Millisecond)
	cancel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if len(received1) != 10 {
		t.Errorf("Desktop1 received %d messages, expected 10", len(received1))
	}
	if len(received2) != 10 {
		t.Errorf("Desktop2 received %d messages, expected 10", len(received2))
	}
}

func TestElectronIPCSimulation(t *testing.T) {
	// Simulate Electron IPC: Main process <-> Renderer process
	desktop, _ := NewDesktopNode("electron-app")

	// Simulate renderer requesting encryption
	rendererRequest := struct {
		Action string `json:"action"`
		PeerID string `json:"peerId"`
		Data   string `json:"data"`
	}{
		Action: "encrypt",
		PeerID: "remote-peer",
		Data:   `{"OBJECT_NAME": "ISS"}`,
	}

	// Generate "remote peer" key for testing
	remotePeer, _ := ecies.GenerateKeyPair(ecies.CurveX25519)
	desktop.RegisterPeer("remote-peer", remotePeer.PublicKey)

	// Main process handles encryption request
	requestJSON, _ := json.Marshal(rendererRequest)
	t.Logf("Renderer IPC request: %s", requestJSON)

	msg, err := desktop.SendEncrypted(rendererRequest.PeerID, "OMM", []byte(rendererRequest.Data))
	if err != nil {
		t.Fatalf("IPC encryption failed: %v", err)
	}

	// Serialize for IPC transfer
	serialized := msg.Encrypted.Serialize()
	t.Logf("Encrypted message size for IPC: %d bytes", len(serialized))

	// Verify "remote peer" can decrypt
	deserialized, err := ecies.DeserializeEncryptedMessage(serialized)
	if err != nil {
		t.Fatalf("Deserialization failed: %v", err)
	}

	decrypted, err := ecies.Decrypt(remotePeer.PrivateKey, deserialized)
	if err != nil {
		t.Fatalf("Remote peer decryption failed: %v", err)
	}

	if string(decrypted) != rendererRequest.Data {
		t.Error("Decrypted data mismatch")
	}
}

func BenchmarkDesktopEncryption(b *testing.B) {
	desktop1, _ := NewDesktopNode("desktop-1")
	desktop2, _ := NewDesktopNode("desktop-2")
	desktop1.RegisterPeer(desktop2.ID, desktop2.KeyPair.PublicKey)

	sizes := []struct {
		name string
		size int
	}{
		{"1KB", 1024},
		{"10KB", 10 * 1024},
		{"100KB", 100 * 1024},
		{"1MB", 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
	}

	for _, size := range sizes {
		data := make([]byte, size.size)
		for i := range data {
			data[i] = byte(i % 256)
		}

		b.Run(fmt.Sprintf("Encrypt-%s", size.name), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				desktop1.SendEncrypted(desktop2.ID, "OEM", data)
			}
		})

		msg, _ := desktop1.SendEncrypted(desktop2.ID, "OEM", data)
		b.Run(fmt.Sprintf("Decrypt-%s", size.name), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				desktop2.ReceiveEncrypted(msg)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
