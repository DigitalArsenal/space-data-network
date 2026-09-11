package storefront

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/mr-tron/base58"
)

// TestSolanaDevnetRecordedVerifier verifies an operator-supplied public
// devnet transaction. It is the no-wallet fallback when the public faucet is
// unavailable; the transaction identity stays in the acceptance command and
// evidence report rather than becoming a long-lived test fixture.
func TestSolanaDevnetRecordedVerifier(t *testing.T) {
	if os.Getenv("SDN_SOLANA_DEVNET_RECORDED") != "1" {
		t.Skip("set SDN_SOLANA_DEVNET_RECORDED=1 to verify a public devnet transaction")
	}
	rpcURL := os.Getenv("SDN_SOLANA_DEVNET_RPC_URL")
	txSignature := os.Getenv("SDN_SOLANA_DEVNET_TX")
	recipient := os.Getenv("SDN_SOLANA_DEVNET_RECIPIENT")
	sender := os.Getenv("SDN_SOLANA_DEVNET_SENDER")
	amount, err := strconv.ParseUint(os.Getenv("SDN_SOLANA_DEVNET_AMOUNT"), 10, 64)
	if rpcURL == "" || txSignature == "" || recipient == "" || sender == "" || err != nil || amount == 0 {
		t.Fatal("RPC URL, tx, sender, recipient, and non-zero amount env values are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := NewSolanaVerifier(ChainConfig{RPCURL: rpcURL, RequiredConfirmations: 1}).VerifyTransaction(ctx, &CryptoPaymentRequest{
		TxHash:           txSignature,
		RecipientAddress: recipient,
		Amount:           amount,
		Currency:         "SOL",
		NativeAsset:      true,
	})
	if err != nil {
		t.Fatalf("VerifyTransaction: %v", err)
	}
	if !result.Verified {
		t.Fatalf("recorded devnet transaction was not verified: %s", result.Error)
	}
	if result.RecipientAddress != recipient || result.SenderAddress != sender || result.Amount != amount || result.Chain != "solana" {
		t.Fatalf("verified result does not match recorded public transaction: %+v", result)
	}
	t.Logf("solana recorded devnet tx=%s sender=%s recipient=%s amount=%d slot=%d confirmations=%d", txSignature, sender, recipient, amount, result.ConfirmationBlock, result.Confirmations)
}

// TestSolanaDevnetLiveVerifier is an opt-in acceptance probe for the real
// Solana JSON-RPC verifier. It creates ephemeral in-memory keypairs, funds the
// sender from the devnet faucet, transfers lamports, and verifies the public
// transaction. No private key or mnemonic is persisted or logged.
func TestSolanaDevnetLiveVerifier(t *testing.T) {
	if os.Getenv("SDN_SOLANA_DEVNET_LIVE") != "1" {
		t.Skip("set SDN_SOLANA_DEVNET_LIVE=1 to run the public devnet probe")
	}
	rpcURL := os.Getenv("SDN_SOLANA_DEVNET_RPC_URL")
	if rpcURL == "" {
		t.Fatal("SDN_SOLANA_DEVNET_RPC_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	senderPublic, senderPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipientPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sender := base58.Encode(senderPublic)
	recipient := base58.Encode(recipientPublic)

	airdropRaw, err := rpcCall(ctx, nilHTTPClient(), rpcURL, "requestAirdrop", []interface{}{sender, uint64(1_000_000)})
	if err != nil {
		t.Fatalf("devnet faucet request failed: %v", err)
	}
	var airdropSignature string
	if err := json.Unmarshal(airdropRaw, &airdropSignature); err != nil {
		t.Fatalf("decode faucet response: %v", err)
	}
	waitForSolanaConfirmation(t, ctx, rpcURL, airdropSignature)

	blockhashRaw, err := rpcCall(ctx, nilHTTPClient(), rpcURL, "getLatestBlockhash", []interface{}{map[string]interface{}{"commitment": "confirmed"}})
	if err != nil {
		t.Fatalf("getLatestBlockhash: %v", err)
	}
	var blockhashResult struct {
		Value struct {
			Blockhash string `json:"blockhash"`
		} `json:"value"`
	}
	if err := json.Unmarshal(blockhashRaw, &blockhashResult); err != nil {
		t.Fatalf("decode latest blockhash: %v", err)
	}
	blockhash, err := base58.Decode(blockhashResult.Value.Blockhash)
	if err != nil || len(blockhash) != ed25519.PublicKeySize {
		t.Fatalf("decode latest blockhash: %v", err)
	}

	const amount = uint64(4_900)
	message := solanaSystemTransferMessage(senderPublic, recipientPublic, blockhash, amount)
	signature := ed25519.Sign(senderPrivate, message)
	transaction := append([]byte{1}, signature...)
	transaction = append(transaction, message...)

	sendRaw, err := rpcCall(ctx, nilHTTPClient(), rpcURL, "sendTransaction", []interface{}{
		base64.StdEncoding.EncodeToString(transaction),
		map[string]interface{}{"encoding": "base64", "preflightCommitment": "confirmed"},
	})
	if err != nil {
		t.Fatalf("send devnet transfer: %v", err)
	}
	var txSignature string
	if err := json.Unmarshal(sendRaw, &txSignature); err != nil {
		t.Fatalf("decode transaction signature: %v", err)
	}
	waitForSolanaConfirmation(t, ctx, rpcURL, txSignature)

	result, err := NewSolanaVerifier(ChainConfig{RPCURL: rpcURL, RequiredConfirmations: 1}).VerifyTransaction(ctx, &CryptoPaymentRequest{
		TxHash:           txSignature,
		RecipientAddress: recipient,
		Amount:           amount,
		Currency:         "SOL",
		NativeAsset:      true,
	})
	if err != nil {
		t.Fatalf("VerifyTransaction: %v", err)
	}
	if !result.Verified {
		t.Fatalf("devnet transaction was not verified: %s", result.Error)
	}
	if result.RecipientAddress != recipient || result.SenderAddress != sender || result.Amount != amount || result.Chain != "solana" {
		t.Fatalf("verified result does not match public transaction: %+v", result)
	}
	t.Logf("solana devnet tx=%s sender=%s recipient=%s amount=%d slot=%d confirmations=%d", txSignature, sender, recipient, amount, result.ConfirmationBlock, result.Confirmations)
}

func nilHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func waitForSolanaConfirmation(t *testing.T, ctx context.Context, rpcURL, signature string) {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		raw, err := rpcCall(ctx, nilHTTPClient(), rpcURL, "getSignatureStatuses", []interface{}{
			[]string{signature},
			map[string]interface{}{"searchTransactionHistory": true},
		})
		if err == nil {
			var statuses solSignatureStatusesResult
			if json.Unmarshal(raw, &statuses) == nil && len(statuses.Value) == 1 && statuses.Value[0] != nil {
				status := statuses.Value[0]
				if status.Err != nil {
					t.Fatalf("transaction %s failed on chain: %v", signature, status.Err)
				}
				if status.ConfirmationStatus == "confirmed" || status.ConfirmationStatus == "finalized" {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for transaction %s: %v", signature, ctx.Err())
		case <-ticker.C:
		}
	}
}

func solanaSystemTransferMessage(sender, recipient, blockhash []byte, amount uint64) []byte {
	message := []byte{1, 0, 1, 3}
	message = append(message, sender...)
	message = append(message, recipient...)
	systemProgram, _ := base58.Decode(solSystemProgramID)
	message = append(message, systemProgram...)
	message = append(message, blockhash...)
	message = append(message, 1, 2, 2, 0, 1, 12)
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[:4], 2)
	binary.LittleEndian.PutUint64(data[4:], amount)
	return append(message, data...)
}
