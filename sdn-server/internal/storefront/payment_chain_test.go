package storefront

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRPCMockServer serves canned JSON-RPC "result" payloads keyed by method
// name. Any method not present in responses fails the test loudly instead of
// silently returning something the verifier might misinterpret.
func newRPCMockServer(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		result, ok := responses[req.Method]
		if !ok {
			t.Errorf("unexpected RPC method: %s", req.Method)
			http.Error(w, "unexpected method", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// hexAddr builds a deterministic, correctly-sized (20-byte/40-hex-char) EVM
// address by repeating a single byte, so tests never depend on hand-counted
// hex strings.
func hexAddr(b byte) string {
	return "0x" + strings.Repeat(fmt.Sprintf("%02x", b), 20)
}

// topicFromAddr left-zero-pads a 20-byte address into a 32-byte log topic,
// as EVM logs encode indexed `address` event parameters.
func topicFromAddr(addr string) string {
	a := strings.TrimPrefix(addr, "0x")
	return "0x" + strings.Repeat("0", 64-len(a)) + a
}

func TestEthereumVerifierNativeTransferPopulatesFullResult(t *testing.T) {
	recipient := hexAddr(0xaa)
	sender := hexAddr(0xbb)

	srv := newRPCMockServer(t, map[string]string{
		"eth_getTransactionReceipt": fmt.Sprintf(`{"status":"0x1","blockNumber":"0x64","from":%q,"to":%q,"logs":[]}`, sender, recipient),
		"eth_blockNumber":           `"0x78"`, // 0x78-0x64 = 20 confirmations
		"eth_getTransactionByHash":  fmt.Sprintf(`{"from":%q,"to":%q,"value":"0xde0b6b3a7640000"}`, sender, recipient),
	})

	v := NewEthereumVerifier(ChainConfig{RPCURL: srv.URL, RequiredConfirmations: 12})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{TxHash: "0xdeadbeef"})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("expected verified, got error: %s", result.Error)
	}
	if result.Chain != "ethereum" {
		t.Errorf("Chain = %q, want ethereum", result.Chain)
	}
	if !result.NativeAsset || result.Asset != "eth" {
		t.Errorf("expected native eth asset, got NativeAsset=%v Asset=%q", result.NativeAsset, result.Asset)
	}
	if result.AssetContract != "" {
		t.Errorf("AssetContract = %q, want empty for native transfer", result.AssetContract)
	}
	if !strings.EqualFold(result.RecipientAddress, recipient) {
		t.Errorf("RecipientAddress = %q, want %q", result.RecipientAddress, recipient)
	}
	if !strings.EqualFold(result.SenderAddress, sender) {
		t.Errorf("SenderAddress = %q, want %q", result.SenderAddress, sender)
	}
	if result.Amount != 1000000000000000000 {
		t.Errorf("Amount = %d, want 1e18", result.Amount)
	}
	if result.ConfirmationBlock != 100 || result.CurrentBlock != 120 || result.Confirmations != 20 {
		t.Errorf("block accounting wrong: %+v", result)
	}
}

func TestEthereumVerifierERC20TransferPopulatesFullResult(t *testing.T) {
	recipient := hexAddr(0xaa)
	sender := hexAddr(0xbb)
	contract := hexAddr(0xcc)

	logsJSON := fmt.Sprintf(`[{"address":%q,"topics":[%q,%q,%q],"data":"0x3e8"}]`,
		contract, erc20TransferTopic, topicFromAddr(sender), topicFromAddr(recipient))

	srv := newRPCMockServer(t, map[string]string{
		"eth_getTransactionReceipt": fmt.Sprintf(`{"status":"0x1","blockNumber":"0x64","from":%q,"to":%q,"logs":%s}`, sender, contract, logsJSON),
		"eth_blockNumber":           `"0x78"`,
		"eth_getTransactionByHash":  fmt.Sprintf(`{"from":%q,"to":%q,"value":"0x0"}`, sender, contract),
	})

	v := NewEthereumVerifier(ChainConfig{RPCURL: srv.URL, RequiredConfirmations: 12})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{
		TxHash:        "0xdeadbeef",
		AssetContract: contract,
	})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("expected verified, got error: %s", result.Error)
	}
	if result.NativeAsset {
		t.Error("expected NativeAsset=false for a token transfer")
	}
	if !strings.EqualFold(result.AssetContract, contract) {
		t.Errorf("AssetContract = %q, want %q", result.AssetContract, contract)
	}
	if !strings.EqualFold(result.RecipientAddress, recipient) {
		t.Errorf("RecipientAddress = %q, want %q", result.RecipientAddress, recipient)
	}
	if !strings.EqualFold(result.SenderAddress, sender) {
		t.Errorf("SenderAddress = %q, want %q", result.SenderAddress, sender)
	}
	if result.Amount != 1000 {
		t.Errorf("Amount = %d, want 1000", result.Amount)
	}
}

func TestEthereumVerifierRejectsMissingTokenTransfer(t *testing.T) {
	recipient := hexAddr(0xaa)
	sender := hexAddr(0xbb)
	contract := hexAddr(0xcc)
	otherContract := hexAddr(0xdd)

	// A Transfer log exists, but for a DIFFERENT contract than the one
	// claimed — the verifier must not substitute it or fall back to native.
	logsJSON := fmt.Sprintf(`[{"address":%q,"topics":[%q,%q,%q],"data":"0x3e8"}]`,
		otherContract, erc20TransferTopic, topicFromAddr(sender), topicFromAddr(recipient))

	srv := newRPCMockServer(t, map[string]string{
		"eth_getTransactionReceipt": fmt.Sprintf(`{"status":"0x1","blockNumber":"0x64","from":%q,"to":%q,"logs":%s}`, sender, otherContract, logsJSON),
		"eth_blockNumber":           `"0x78"`,
		"eth_getTransactionByHash":  fmt.Sprintf(`{"from":%q,"to":%q,"value":"0x0"}`, sender, otherContract),
	})

	v := NewEthereumVerifier(ChainConfig{RPCURL: srv.URL, RequiredConfirmations: 12})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{
		TxHash:        "0xdeadbeef",
		AssetContract: contract,
	})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if result.Verified {
		t.Fatal("expected verification to fail for a non-matching token contract")
	}
	if !strings.Contains(result.Error, "no matching token transfer") {
		t.Errorf("error = %q, want no-matching-transfer rejection", result.Error)
	}
}

func TestEthereumVerifierRejectsInsufficientConfirmations(t *testing.T) {
	recipient := hexAddr(0xaa)
	sender := hexAddr(0xbb)

	srv := newRPCMockServer(t, map[string]string{
		"eth_getTransactionReceipt": fmt.Sprintf(`{"status":"0x1","blockNumber":"0x64","from":%q,"to":%q,"logs":[]}`, sender, recipient),
		"eth_blockNumber":           `"0x65"`, // only 1 confirmation
	})

	v := NewEthereumVerifier(ChainConfig{RPCURL: srv.URL, RequiredConfirmations: 12})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{TxHash: "0xdeadbeef"})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if result.Verified {
		t.Fatal("expected verification to fail for insufficient confirmations")
	}
	if !strings.Contains(result.Error, "insufficient confirmations") {
		t.Errorf("error = %q, want insufficient confirmations", result.Error)
	}
}

func TestEthereumVerifierRejectsRevertedTransaction(t *testing.T) {
	srv := newRPCMockServer(t, map[string]string{
		"eth_getTransactionReceipt": `{"status":"0x0","blockNumber":"0x64","from":"0x0","to":"0x0","logs":[]}`,
	})

	v := NewEthereumVerifier(ChainConfig{RPCURL: srv.URL})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{TxHash: "0xdeadbeef"})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if result.Verified {
		t.Fatal("expected verification to fail for a reverted transaction")
	}
	if !strings.Contains(result.Error, "reverted") {
		t.Errorf("error = %q, want reverted rejection", result.Error)
	}
}

func TestSolanaVerifierNativeTransferPopulatesFullResult(t *testing.T) {
	source := "SourcePubkey11111111111111111111111111111"
	destination := "DestPubkey111111111111111111111111111111"

	txResult := fmt.Sprintf(`{
		"slot": 555,
		"meta": {"err": null},
		"transaction": {
			"message": {
				"instructions": [
					{
						"programId": %q,
						"parsed": {"type":"transfer","info":{"source":%q,"destination":%q,"lamports":2000000000}}
					}
				]
			}
		}
	}`, solSystemProgramID, source, destination)

	srv := newRPCMockServer(t, map[string]string{
		"getTransaction": txResult,
	})

	v := NewSolanaVerifier(ChainConfig{RPCURL: srv.URL})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{TxHash: "sometxsig"})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("expected verified, got error: %s", result.Error)
	}
	if result.Chain != "solana" || !result.NativeAsset || result.Asset != "sol" {
		t.Errorf("unexpected chain/asset fields: %+v", result)
	}
	if result.RecipientAddress != destination {
		t.Errorf("RecipientAddress = %q, want %q", result.RecipientAddress, destination)
	}
	if result.SenderAddress != source {
		t.Errorf("SenderAddress = %q, want %q", result.SenderAddress, source)
	}
	if result.Amount != 2000000000 {
		t.Errorf("Amount = %d, want 2000000000", result.Amount)
	}
	if result.ConfirmationBlock != 555 {
		t.Errorf("ConfirmationBlock (slot) = %d, want 555", result.ConfirmationBlock)
	}
}

func TestSolanaVerifierSPLTransferPopulatesFullResult(t *testing.T) {
	mint := "MintAddress1111111111111111111111111111111"
	recipientOwner := "RecipientOwner11111111111111111111111111"
	senderOwner := "SenderOwner111111111111111111111111111111"

	txResult := fmt.Sprintf(`{
		"slot": 777,
		"meta": {
			"err": null,
			"preTokenBalances": [
				{"accountIndex": 1, "mint": %q, "owner": %q, "uiTokenAmount": {"amount": "5000"}},
				{"accountIndex": 2, "mint": %q, "owner": %q, "uiTokenAmount": {"amount": "1000"}}
			],
			"postTokenBalances": [
				{"accountIndex": 1, "mint": %q, "owner": %q, "uiTokenAmount": {"amount": "3500"}},
				{"accountIndex": 2, "mint": %q, "owner": %q, "uiTokenAmount": {"amount": "2500"}}
			]
		},
		"transaction": {"message": {"instructions": []}}
	}`, mint, senderOwner, mint, recipientOwner, mint, senderOwner, mint, recipientOwner)

	srv := newRPCMockServer(t, map[string]string{
		"getTransaction": txResult,
	})

	v := NewSolanaVerifier(ChainConfig{RPCURL: srv.URL})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{
		TxHash:        "sometxsig",
		AssetContract: mint,
	})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("expected verified, got error: %s", result.Error)
	}
	if result.NativeAsset {
		t.Error("expected NativeAsset=false for an SPL transfer")
	}
	if result.AssetContract != mint {
		t.Errorf("AssetContract = %q, want %q", result.AssetContract, mint)
	}
	if result.RecipientAddress != recipientOwner {
		t.Errorf("RecipientAddress = %q, want %q", result.RecipientAddress, recipientOwner)
	}
	if result.SenderAddress != senderOwner {
		t.Errorf("SenderAddress = %q, want %q", result.SenderAddress, senderOwner)
	}
	// sender: 5000 -> 3500 (delta -1500); recipient: 1000 -> 2500 (delta +1500).
	if result.Amount != 1500 {
		t.Errorf("Amount = %d, want 1500", result.Amount)
	}
}

func TestBitcoinVerifierFindsPaidOutput(t *testing.T) {
	recipient := "bc1qrecipientaddressexample000000000000000"
	changeAddr := "bc1qchangeaddressexample00000000000000000"

	txResult := fmt.Sprintf(`{
		"confirmations": 10,
		"blockhash": "0000000000000000000blockhash",
		"vout": [
			{"value": 0.001, "scriptPubKey": {"address": %q}},
			{"value": 0.5, "scriptPubKey": {"address": %q}}
		]
	}`, changeAddr, recipient)

	srv := newRPCMockServer(t, map[string]string{
		"getrawtransaction": txResult,
	})

	v := NewBitcoinVerifier(ChainConfig{RPCURL: srv.URL, RequiredConfirmations: 6})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{
		TxHash:           "sometxid",
		RecipientAddress: recipient,
	})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("expected verified, got error: %s", result.Error)
	}
	if !result.NativeAsset || result.Asset != "btc" {
		t.Errorf("unexpected asset fields: %+v", result)
	}
	if result.RecipientAddress != recipient {
		t.Errorf("RecipientAddress = %q, want %q", result.RecipientAddress, recipient)
	}
	// 0.5 BTC == 50,000,000 satoshis
	if result.Amount != 50000000 {
		t.Errorf("Amount = %d, want 50000000", result.Amount)
	}
}

func TestBitcoinVerifierRejectsWhenRecipientOutputMissing(t *testing.T) {
	changeAddr := "bc1qchangeaddressexample00000000000000000"

	txResult := fmt.Sprintf(`{
		"confirmations": 10,
		"blockhash": "0000000000000000000blockhash",
		"vout": [
			{"value": 0.5, "scriptPubKey": {"address": %q}}
		]
	}`, changeAddr)

	srv := newRPCMockServer(t, map[string]string{
		"getrawtransaction": txResult,
	})

	v := NewBitcoinVerifier(ChainConfig{RPCURL: srv.URL, RequiredConfirmations: 6})
	result, err := v.VerifyTransaction(context.Background(), &CryptoPaymentRequest{
		TxHash:           "sometxid",
		RecipientAddress: "bc1qdifferentrecipientaddress0000000000000",
	})
	if err != nil {
		t.Fatalf("VerifyTransaction returned error: %v", err)
	}
	if result.Verified {
		t.Fatal("expected verification to fail when the claimed recipient has no paid output")
	}
	if !strings.Contains(result.Error, "not found in transaction outputs") {
		t.Errorf("error = %q, want recipient-not-found rejection", result.Error)
	}
}
