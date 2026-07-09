package storefront

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChainVerifier verifies a cryptocurrency transaction on a specific blockchain.
type ChainVerifier interface {
	// VerifyTransaction checks whether the transaction identified by TxHash
	// has been confirmed on chain with sufficient confirmations.
	VerifyTransaction(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error)

	// Chain returns the chain identifier (e.g., "ethereum", "solana", "bitcoin").
	Chain() string
}

// ChainConfig holds RPC endpoint and confirmation settings for one blockchain.
type ChainConfig struct {
	RPCURL                string
	RequiredConfirmations uint64
}

// --- JSON-RPC helpers ---

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcCall(ctx context.Context, client *http.Client, rpcURL, method string, params interface{}) (json.RawMessage, error) {
	body, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal rpc request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("build rpc request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rpc request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read rpc response: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal rpc response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

func parseHexUint64(hexStr string) (uint64, error) {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	hexStr = strings.TrimPrefix(hexStr, "0X")
	var val uint64
	_, err := fmt.Sscanf(hexStr, "%x", &val)
	return val, err
}

// --- Ethereum ---

// EthereumVerifier verifies Ethereum transactions via JSON-RPC
// (eth_getTransactionReceipt + eth_blockNumber).
type EthereumVerifier struct {
	rpcURL        string
	confirmations uint64
	client        *http.Client
}

// NewEthereumVerifier creates a verifier for Ethereum-compatible chains.
func NewEthereumVerifier(cfg ChainConfig) *EthereumVerifier {
	confs := cfg.RequiredConfirmations
	if confs == 0 {
		confs = 12
	}
	return &EthereumVerifier{
		rpcURL:        cfg.RPCURL,
		confirmations: confs,
		client:        &http.Client{Timeout: 30 * time.Second},
	}
}

func (v *EthereumVerifier) Chain() string { return "ethereum" }

func (v *EthereumVerifier) VerifyTransaction(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	if v.rpcURL == "" {
		return &CryptoPaymentResult{Verified: false, Error: "ethereum RPC URL not configured"}, nil
	}

	// eth_getTransactionReceipt
	receiptRaw, err := rpcCall(ctx, v.client, v.rpcURL, "eth_getTransactionReceipt", []interface{}{req.TxHash})
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("eth_getTransactionReceipt: %v", err)}, nil
	}
	if string(receiptRaw) == "null" {
		return &CryptoPaymentResult{Verified: false, Error: "transaction not found or not yet mined"}, nil
	}

	var receipt struct {
		Status      string `json:"status"`
		BlockNumber string `json:"blockNumber"`
	}
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil || receipt.BlockNumber == "" {
		return &CryptoPaymentResult{Verified: false, Error: "transaction not found or not yet mined"}, nil
	}
	if receipt.Status != "0x1" {
		return &CryptoPaymentResult{Verified: false, Error: "transaction reverted"}, nil
	}

	txBlock, err := parseHexUint64(receipt.BlockNumber)
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("invalid block number: %v", err)}, nil
	}

	// eth_blockNumber
	blockRaw, err := rpcCall(ctx, v.client, v.rpcURL, "eth_blockNumber", []interface{}{})
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("eth_blockNumber: %v", err)}, nil
	}
	var blockHex string
	if err := json.Unmarshal(blockRaw, &blockHex); err != nil {
		return &CryptoPaymentResult{Verified: false, Error: "invalid block number response"}, nil
	}
	currentBlock, err := parseHexUint64(blockHex)
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("invalid current block: %v", err)}, nil
	}

	if currentBlock < txBlock {
		return &CryptoPaymentResult{Verified: false, Error: "block number inconsistency"}, nil
	}
	confirmations := currentBlock - txBlock
	if confirmations < v.confirmations {
		return &CryptoPaymentResult{
			Verified:          false,
			ConfirmationBlock: txBlock,
			Error:             fmt.Sprintf("insufficient confirmations: %d/%d", confirmations, v.confirmations),
		}, nil
	}

	return &CryptoPaymentResult{Verified: true, ConfirmationBlock: txBlock}, nil
}

// --- Solana ---

// SolanaVerifier verifies Solana transactions via JSON-RPC (getTransaction).
type SolanaVerifier struct {
	rpcURL string
	client *http.Client
}

// NewSolanaVerifier creates a verifier for Solana.
func NewSolanaVerifier(cfg ChainConfig) *SolanaVerifier {
	return &SolanaVerifier{
		rpcURL: cfg.RPCURL,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (v *SolanaVerifier) Chain() string { return "solana" }

func (v *SolanaVerifier) VerifyTransaction(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	if v.rpcURL == "" {
		return &CryptoPaymentResult{Verified: false, Error: "solana RPC URL not configured"}, nil
	}

	params := []interface{}{
		req.TxHash,
		map[string]interface{}{
			"commitment":                     "confirmed",
			"maxSupportedTransactionVersion": 0,
		},
	}
	resultRaw, err := rpcCall(ctx, v.client, v.rpcURL, "getTransaction", params)
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("getTransaction: %v", err)}, nil
	}
	if string(resultRaw) == "null" {
		return &CryptoPaymentResult{Verified: false, Error: "transaction not found"}, nil
	}

	var tx struct {
		Slot uint64 `json:"slot"`
		Meta struct {
			Err interface{} `json:"err"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(resultRaw, &tx); err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("parse transaction: %v", err)}, nil
	}
	if tx.Meta.Err != nil {
		return &CryptoPaymentResult{Verified: false, Error: "transaction failed on chain"}, nil
	}

	return &CryptoPaymentResult{Verified: true, ConfirmationBlock: tx.Slot}, nil
}

// --- Bitcoin ---

// BitcoinVerifier verifies Bitcoin transactions via JSON-RPC (getrawtransaction).
// The RPC URL may include credentials: http://user:pass@host:8332
type BitcoinVerifier struct {
	rpcURL        string
	confirmations uint64
	client        *http.Client
}

// NewBitcoinVerifier creates a verifier for Bitcoin.
func NewBitcoinVerifier(cfg ChainConfig) *BitcoinVerifier {
	confs := cfg.RequiredConfirmations
	if confs == 0 {
		confs = 6
	}
	return &BitcoinVerifier{
		rpcURL:        cfg.RPCURL,
		confirmations: confs,
		client:        &http.Client{Timeout: 30 * time.Second},
	}
}

func (v *BitcoinVerifier) Chain() string { return "bitcoin" }

func (v *BitcoinVerifier) VerifyTransaction(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	if v.rpcURL == "" {
		return &CryptoPaymentResult{Verified: false, Error: "bitcoin RPC URL not configured"}, nil
	}

	resultRaw, err := rpcCall(ctx, v.client, v.rpcURL, "getrawtransaction", []interface{}{req.TxHash, true})
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("getrawtransaction: %v", err)}, nil
	}

	var tx struct {
		Confirmations uint64 `json:"confirmations"`
		BlockHash     string `json:"blockhash"`
	}
	if err := json.Unmarshal(resultRaw, &tx); err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("parse transaction: %v", err)}, nil
	}
	if tx.BlockHash == "" {
		return &CryptoPaymentResult{Verified: false, Error: "transaction not yet in a block"}, nil
	}
	if tx.Confirmations < v.confirmations {
		return &CryptoPaymentResult{
			Verified: false,
			Error:    fmt.Sprintf("insufficient confirmations: %d/%d", tx.Confirmations, v.confirmations),
		}, nil
	}

	return &CryptoPaymentResult{Verified: true, ConfirmationBlock: tx.Confirmations}, nil
}
