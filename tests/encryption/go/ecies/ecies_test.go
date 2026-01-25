package ecies

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestKeyPairGeneration(t *testing.T) {
	testCases := []struct {
		name      string
		curveType CurveType
	}{
		{"X25519", CurveX25519},
		{"secp256k1", CurveSecp256k1},
		{"P-256", CurveP256},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kp, err := GenerateKeyPair(tc.curveType)
			if err != nil {
				t.Fatalf("Failed to generate key pair: %v", err)
			}

			if len(kp.PrivateKey) == 0 {
				t.Error("Private key is empty")
			}
			if len(kp.PublicKey) == 0 {
				t.Error("Public key is empty")
			}
			if kp.CurveType != tc.curveType {
				t.Errorf("Curve type mismatch: got %v, want %v", kp.CurveType, tc.curveType)
			}

			t.Logf("Generated %s key pair: private=%d bytes, public=%d bytes",
				tc.name, len(kp.PrivateKey), len(kp.PublicKey))
		})
	}
}

func TestEncryptDecrypt(t *testing.T) {
	testCases := []struct {
		name      string
		curveType CurveType
	}{
		{"X25519", CurveX25519},
		{"secp256k1", CurveSecp256k1},
		{"P-256", CurveP256},
	}

	plaintext := []byte("Hello, Space Data Network!")

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate recipient key pair
			recipientKP, err := GenerateKeyPair(tc.curveType)
			if err != nil {
				t.Fatalf("Failed to generate recipient key pair: %v", err)
			}

			// Encrypt
			encrypted, err := Encrypt(recipientKP.PublicKey, plaintext, tc.curveType)
			if err != nil {
				t.Fatalf("Encryption failed: %v", err)
			}

			if len(encrypted.Ciphertext) == 0 {
				t.Error("Ciphertext is empty")
			}
			if len(encrypted.EphemeralPublicKey) == 0 {
				t.Error("Ephemeral public key is empty")
			}
			if len(encrypted.Nonce) == 0 {
				t.Error("Nonce is empty")
			}
			if len(encrypted.MAC) == 0 {
				t.Error("MAC is empty")
			}

			// Decrypt
			decrypted, err := Decrypt(recipientKP.PrivateKey, encrypted)
			if err != nil {
				t.Fatalf("Decryption failed: %v", err)
			}

			if !bytes.Equal(decrypted, plaintext) {
				t.Errorf("Decrypted text mismatch: got %q, want %q", decrypted, plaintext)
			}
		})
	}
}

func TestEncryptDecryptLargeData(t *testing.T) {
	// Test with large payload (ephemeris data simulation)
	testCases := []struct {
		name      string
		curveType CurveType
		dataSize  int
	}{
		{"X25519-1KB", CurveX25519, 1024},
		{"X25519-1MB", CurveX25519, 1024 * 1024},
		{"P256-1KB", CurveP256, 1024},
		{"P256-1MB", CurveP256, 1024 * 1024},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate random data
			plaintext := make([]byte, tc.dataSize)
			if _, err := rand.Read(plaintext); err != nil {
				t.Fatalf("Failed to generate random data: %v", err)
			}

			// Generate recipient key pair
			recipientKP, err := GenerateKeyPair(tc.curveType)
			if err != nil {
				t.Fatalf("Failed to generate key pair: %v", err)
			}

			// Encrypt
			encrypted, err := Encrypt(recipientKP.PublicKey, plaintext, tc.curveType)
			if err != nil {
				t.Fatalf("Encryption failed: %v", err)
			}

			// Decrypt
			decrypted, err := Decrypt(recipientKP.PrivateKey, encrypted)
			if err != nil {
				t.Fatalf("Decryption failed: %v", err)
			}

			if !bytes.Equal(decrypted, plaintext) {
				t.Error("Decrypted data does not match original")
			}

			t.Logf("Successfully encrypted/decrypted %d bytes with %s", tc.dataSize, tc.name)
		})
	}
}

func TestMACVerification(t *testing.T) {
	// Test that tampered ciphertext is detected
	recipientKP, err := GenerateKeyPair(CurveX25519)
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}

	plaintext := []byte("Test message for MAC verification")
	encrypted, err := Encrypt(recipientKP.PublicKey, plaintext, CurveX25519)
	if err != nil {
		t.Fatalf("Encryption failed: %v", err)
	}

	// Tamper with ciphertext
	if len(encrypted.Ciphertext) > 0 {
		encrypted.Ciphertext[0] ^= 0xFF
	}

	// Decryption should fail due to MAC mismatch
	_, err = Decrypt(recipientKP.PrivateKey, encrypted)
	if err == nil {
		t.Error("Expected decryption to fail due to tampered ciphertext")
	}
}

func TestSerialization(t *testing.T) {
	testCases := []struct {
		name      string
		curveType CurveType
	}{
		{"X25519", CurveX25519},
		{"secp256k1", CurveSecp256k1},
		{"P-256", CurveP256},
	}

	plaintext := []byte("Test message for serialization")

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			recipientKP, err := GenerateKeyPair(tc.curveType)
			if err != nil {
				t.Fatalf("Failed to generate key pair: %v", err)
			}

			encrypted, err := Encrypt(recipientKP.PublicKey, plaintext, tc.curveType)
			if err != nil {
				t.Fatalf("Encryption failed: %v", err)
			}

			// Serialize
			serialized := encrypted.Serialize()
			if len(serialized) == 0 {
				t.Fatal("Serialization produced empty result")
			}

			// Deserialize
			deserialized, err := DeserializeEncryptedMessage(serialized)
			if err != nil {
				t.Fatalf("Deserialization failed: %v", err)
			}

			// Verify fields match
			if deserialized.CurveType != encrypted.CurveType {
				t.Error("Curve type mismatch after deserialization")
			}
			if !bytes.Equal(deserialized.EphemeralPublicKey, encrypted.EphemeralPublicKey) {
				t.Error("Ephemeral public key mismatch after deserialization")
			}
			if !bytes.Equal(deserialized.Ciphertext, encrypted.Ciphertext) {
				t.Error("Ciphertext mismatch after deserialization")
			}
			if !bytes.Equal(deserialized.Nonce, encrypted.Nonce) {
				t.Error("Nonce mismatch after deserialization")
			}
			if !bytes.Equal(deserialized.MAC, encrypted.MAC) {
				t.Error("MAC mismatch after deserialization")
			}

			// Decrypt deserialized message
			decrypted, err := Decrypt(recipientKP.PrivateKey, deserialized)
			if err != nil {
				t.Fatalf("Decryption of deserialized message failed: %v", err)
			}

			if !bytes.Equal(decrypted, plaintext) {
				t.Error("Decrypted message mismatch after serialization roundtrip")
			}
		})
	}
}

func TestWalletKeyDerivation(t *testing.T) {
	// Simulate a wallet signature
	signature := make([]byte, 65)
	if _, err := rand.Read(signature); err != nil {
		t.Fatalf("Failed to generate mock signature: %v", err)
	}

	testCases := []struct {
		name      string
		curveType CurveType
	}{
		{"X25519", CurveX25519},
		{"secp256k1", CurveSecp256k1},
		{"P-256", CurveP256},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kp, err := DeriveKeyFromWallet(signature, tc.curveType)
			if err != nil {
				t.Fatalf("Key derivation failed: %v", err)
			}

			if len(kp.PrivateKey) == 0 {
				t.Error("Derived private key is empty")
			}
			if len(kp.PublicKey) == 0 {
				t.Error("Derived public key is empty")
			}

			// Test that derived keys work for encryption
			plaintext := []byte("Test message with wallet-derived key")
			encrypted, err := Encrypt(kp.PublicKey, plaintext, tc.curveType)
			if err != nil {
				t.Fatalf("Encryption with derived key failed: %v", err)
			}

			decrypted, err := Decrypt(kp.PrivateKey, encrypted)
			if err != nil {
				t.Fatalf("Decryption with derived key failed: %v", err)
			}

			if !bytes.Equal(decrypted, plaintext) {
				t.Error("Decrypted message mismatch with wallet-derived keys")
			}
		})
	}
}

func TestDeterministicWalletDerivation(t *testing.T) {
	// Same signature should produce same key
	signature := []byte("fixed-signature-for-testing-determinism-1234567890")

	kp1, err := DeriveKeyFromWallet(signature, CurveX25519)
	if err != nil {
		t.Fatalf("First key derivation failed: %v", err)
	}

	kp2, err := DeriveKeyFromWallet(signature, CurveX25519)
	if err != nil {
		t.Fatalf("Second key derivation failed: %v", err)
	}

	if !bytes.Equal(kp1.PrivateKey, kp2.PrivateKey) {
		t.Error("Derived keys should be deterministic")
	}
	if !bytes.Equal(kp1.PublicKey, kp2.PublicKey) {
		t.Error("Derived public keys should be deterministic")
	}
}

func TestCrossKeyExchange(t *testing.T) {
	// Test that Alice can encrypt a message to Bob using different ephemeral keys
	// This simulates real-world usage where sender doesn't have a long-term key

	// Bob's long-term key pair
	bobKP, err := GenerateKeyPair(CurveX25519)
	if err != nil {
		t.Fatalf("Failed to generate Bob's key pair: %v", err)
	}

	// Alice sends multiple messages, each with different ephemeral keys
	messages := []string{
		"Message 1 from Alice",
		"Message 2 from Alice",
		"Message 3 from Alice",
	}

	for i, msg := range messages {
		encrypted, err := Encrypt(bobKP.PublicKey, []byte(msg), CurveX25519)
		if err != nil {
			t.Fatalf("Encryption of message %d failed: %v", i+1, err)
		}

		decrypted, err := Decrypt(bobKP.PrivateKey, encrypted)
		if err != nil {
			t.Fatalf("Decryption of message %d failed: %v", i+1, err)
		}

		if string(decrypted) != msg {
			t.Errorf("Message %d mismatch: got %q, want %q", i+1, decrypted, msg)
		}
	}
}

func BenchmarkEncryptDecrypt(b *testing.B) {
	testCases := []struct {
		name      string
		curveType CurveType
		dataSize  int
	}{
		{"X25519-1KB", CurveX25519, 1024},
		{"X25519-10KB", CurveX25519, 10 * 1024},
		{"X25519-100KB", CurveX25519, 100 * 1024},
		{"P256-1KB", CurveP256, 1024},
		{"P256-10KB", CurveP256, 10 * 1024},
		{"P256-100KB", CurveP256, 100 * 1024},
	}

	for _, tc := range testCases {
		b.Run(tc.name+"-Encrypt", func(b *testing.B) {
			recipientKP, _ := GenerateKeyPair(tc.curveType)
			plaintext := make([]byte, tc.dataSize)
			rand.Read(plaintext)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				Encrypt(recipientKP.PublicKey, plaintext, tc.curveType)
			}
		})

		b.Run(tc.name+"-Decrypt", func(b *testing.B) {
			recipientKP, _ := GenerateKeyPair(tc.curveType)
			plaintext := make([]byte, tc.dataSize)
			rand.Read(plaintext)
			encrypted, _ := Encrypt(recipientKP.PublicKey, plaintext, tc.curveType)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				Decrypt(recipientKP.PrivateKey, encrypted)
			}
		})
	}
}

func BenchmarkSerialization(b *testing.B) {
	recipientKP, _ := GenerateKeyPair(CurveX25519)
	plaintext := make([]byte, 1024)
	rand.Read(plaintext)
	encrypted, _ := Encrypt(recipientKP.PublicKey, plaintext, CurveX25519)

	b.Run("Serialize", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			encrypted.Serialize()
		}
	})

	serialized := encrypted.Serialize()
	b.Run("Deserialize", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			DeserializeEncryptedMessage(serialized)
		}
	})
}
