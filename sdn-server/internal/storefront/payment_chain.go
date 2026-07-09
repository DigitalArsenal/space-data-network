package storefront

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ChainVerifier verifies a cryptocurrency transaction on a specific blockchain.
type ChainVerifier interface {
	// VerifyTransaction checks whether the transaction identified by TxHash
	// has been confirmed on chain with sufficient confirmations, and reports
	// the transaction's actual on-chain recipient, sender, amount, and asset
	// identity. Callers (validateVerifiedCryptoPayment) compare every one of
	// these fields against the buyer's signed payment intent and fail closed
	// if the verifier could not determine one of them — a verifier must never
	// leave an identity field blank just because confirming the tx hash was
	// easier than fully decoding it.
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

// parseHexUint64 parses a "0x"-prefixed (or bare) hex string into a uint64.
// It is used both for compact values (block numbers) and 32-byte
// left-zero-padded ABI-encoded values (ERC-20 Transfer log data): leading
// zeros do not affect the result, and a value that does not fit in 64 bits
// returns an error instead of silently truncating/wrapping — callers must
// treat that as a verification failure, never as "close enough".
func parseHexUint64(hexStr string) (uint64, error) {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	hexStr = strings.TrimPrefix(hexStr, "0X")
	if hexStr == "" {
		return 0, nil
	}
	var val uint64
	_, err := fmt.Sscanf(hexStr, "%x", &val)
	return val, err
}

// addressFromTopic extracts a 20-byte EVM address from a 32-byte
// left-zero-padded log topic (as used for indexed `address` event
// parameters, e.g. ERC-20 Transfer's `from`/`to`).
func addressFromTopic(topic string) string {
	topic = strings.TrimPrefix(topic, "0x")
	topic = strings.TrimPrefix(topic, "0X")
	if len(topic) < 40 {
		return ""
	}
	return "0x" + strings.ToLower(topic[len(topic)-40:])
}

// --- Ethereum ---

// erc20TransferTopic is keccak256("Transfer(address,address,uint256)"), the
// topic0 every ERC-20 Transfer log carries.
const erc20TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// EthereumVerifier verifies Ethereum transactions via JSON-RPC
// (eth_getTransactionReceipt + eth_blockNumber + eth_getTransactionByHash).
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

type ethLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type ethTransactionReceipt struct {
	Status      string   `json:"status"`
	BlockNumber string   `json:"blockNumber"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Logs        []ethLog `json:"logs"`
}

type ethTransaction struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Value string `json:"value"`
}

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

	var receipt ethTransactionReceipt
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

	// eth_getTransactionByHash — needed for the native ETH value, which the
	// receipt does not carry.
	txRaw, err := rpcCall(ctx, v.client, v.rpcURL, "eth_getTransactionByHash", []interface{}{req.TxHash})
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("eth_getTransactionByHash: %v", err)}, nil
	}
	if string(txRaw) == "null" {
		return &CryptoPaymentResult{Verified: false, Error: "transaction not found"}, nil
	}
	var tx ethTransaction
	if err := json.Unmarshal(txRaw, &tx); err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("parse transaction: %v", err)}, nil
	}

	result := &CryptoPaymentResult{
		Verified:          true,
		Chain:             "ethereum",
		ConfirmationBlock: txBlock,
		CurrentBlock:      currentBlock,
		Confirmations:     confirmations,
	}
	if sender := strings.TrimSpace(tx.From); sender != "" {
		result.SenderAddress = strings.ToLower(sender)
	}

	// The caller's claimed asset contract (if any) selects which on-chain
	// evidence we trust: a token payment must show up as a matching ERC-20
	// Transfer log; we do not fall back to treating an unrelated stray log as
	// the payment, and we do not fall back to the native value either, since
	// that would let a caller claiming a token contract get "verified" off
	// of an incidental/zero-value native transfer. Whatever this function
	// returns is re-checked against the buyer's signed intent by
	// validateVerifiedCryptoPayment, so a caller cannot gain anything by
	// lying here — it can only steer verification toward on-chain evidence
	// that either matches the intent (and is legitimately paid) or doesn't
	// (and is rejected).
	wantContract := normalizePaymentContract(req.AssetContract, "ethereum")
	if wantContract != "" {
		transfer, ok := findERC20Transfer(receipt.Logs, wantContract)
		if !ok {
			return &CryptoPaymentResult{Verified: false, Error: "no matching token transfer found in transaction logs"}, nil
		}
		amount, err := parseHexUint64(transfer.data)
		if err != nil {
			return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("invalid token transfer amount: %v", err)}, nil
		}
		result.NativeAsset = false
		result.AssetContract = transfer.contract
		result.RecipientAddress = transfer.to
		if transfer.from != "" {
			result.SenderAddress = transfer.from
		}
		result.Amount = amount
		return result, nil
	}

	result.NativeAsset = true
	result.Asset = "eth"
	if to := strings.TrimSpace(tx.To); to != "" {
		result.RecipientAddress = strings.ToLower(to)
	}
	amount, err := parseHexUint64(tx.Value)
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("invalid transaction value: %v", err)}, nil
	}
	result.Amount = amount

	return result, nil
}

type erc20Transfer struct {
	contract string
	from     string
	to       string
	data     string
}

// findERC20Transfer scans the receipt's logs for a Transfer(address,address,uint256)
// event emitted by wantContract (already normalized). It returns the first match.
func findERC20Transfer(logs []ethLog, wantContract string) (erc20Transfer, bool) {
	for _, l := range logs {
		if len(l.Topics) < 3 {
			continue
		}
		if !strings.EqualFold(l.Topics[0], erc20TransferTopic) {
			continue
		}
		contract := normalizePaymentContract(l.Address, "ethereum")
		if contract == "" || contract != wantContract {
			continue
		}
		to := addressFromTopic(l.Topics[2])
		if to == "" {
			continue
		}
		return erc20Transfer{
			contract: contract,
			from:     addressFromTopic(l.Topics[1]),
			to:       to,
			data:     l.Data,
		}, true
	}
	return erc20Transfer{}, false
}

// --- Solana ---

const solSystemProgramID = "11111111111111111111111111111111"

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

type solTokenBalance struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UiTokenAmount struct {
		Amount string `json:"amount"`
	} `json:"uiTokenAmount"`
}

type solMeta struct {
	Err               interface{}        `json:"err"`
	PreTokenBalances  []solTokenBalance  `json:"preTokenBalances"`
	PostTokenBalances []solTokenBalance  `json:"postTokenBalances"`
}

type solParsedInstructionInfo struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Lamports    uint64 `json:"lamports"`
}

type solParsedInstruction struct {
	Type string                    `json:"type"`
	Info solParsedInstructionInfo `json:"info"`
}

type solInstruction struct {
	ProgramID string                 `json:"programId"`
	Parsed    *solParsedInstruction `json:"parsed"`
}

type solMessage struct {
	Instructions []solInstruction `json:"instructions"`
}

type solTransactionEnvelope struct {
	Message solMessage `json:"message"`
}

type solTransactionResult struct {
	Slot        uint64                   `json:"slot"`
	Meta        *solMeta                 `json:"meta"`
	Transaction *solTransactionEnvelope `json:"transaction"`
}

func (v *SolanaVerifier) VerifyTransaction(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	if v.rpcURL == "" {
		return &CryptoPaymentResult{Verified: false, Error: "solana RPC URL not configured"}, nil
	}

	params := []interface{}{
		req.TxHash,
		map[string]interface{}{
			"commitment":                     "confirmed",
			"maxSupportedTransactionVersion": 0,
			"encoding":                       "jsonParsed",
		},
	}
	resultRaw, err := rpcCall(ctx, v.client, v.rpcURL, "getTransaction", params)
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("getTransaction: %v", err)}, nil
	}
	if string(resultRaw) == "null" {
		return &CryptoPaymentResult{Verified: false, Error: "transaction not found"}, nil
	}

	var tx solTransactionResult
	if err := json.Unmarshal(resultRaw, &tx); err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("parse transaction: %v", err)}, nil
	}
	if tx.Meta == nil {
		return &CryptoPaymentResult{Verified: false, Error: "transaction metadata missing"}, nil
	}
	if tx.Meta.Err != nil {
		return &CryptoPaymentResult{Verified: false, Error: "transaction failed on chain"}, nil
	}

	result := &CryptoPaymentResult{
		Verified:          true,
		Chain:             "solana",
		ConfirmationBlock: tx.Slot,
	}

	// Same non-fallback discipline as the Ethereum verifier: a claimed mint
	// selects the SPL balance-delta evidence we trust; we never substitute
	// native-SOL movement for a claimed token payment.
	wantMint := strings.TrimSpace(req.AssetContract)
	if wantMint != "" {
		recipientOwner, senderOwner, amount, ok := findSPLTransfer(tx.Meta, wantMint)
		if !ok {
			return &CryptoPaymentResult{Verified: false, Error: "no matching token transfer found in transaction"}, nil
		}
		result.NativeAsset = false
		result.AssetContract = wantMint
		result.RecipientAddress = recipientOwner
		result.SenderAddress = senderOwner
		result.Amount = amount
		return result, nil
	}

	if tx.Transaction == nil {
		return &CryptoPaymentResult{Verified: false, Error: "transaction message missing"}, nil
	}
	source, destination, lamports, ok := findSystemTransfer(tx.Transaction.Message.Instructions)
	if !ok {
		return &CryptoPaymentResult{Verified: false, Error: "no native SOL transfer instruction found"}, nil
	}
	result.NativeAsset = true
	result.Asset = "sol"
	result.RecipientAddress = destination
	result.SenderAddress = source
	result.Amount = lamports
	return result, nil
}

// findSystemTransfer looks for a parsed System Program transfer instruction
// and returns its source, destination, and lamport amount.
func findSystemTransfer(instructions []solInstruction) (source, destination string, lamports uint64, ok bool) {
	for _, instr := range instructions {
		if instr.ProgramID != solSystemProgramID || instr.Parsed == nil {
			continue
		}
		if instr.Parsed.Type != "transfer" && instr.Parsed.Type != "transferWithSeed" {
			continue
		}
		if instr.Parsed.Info.Destination == "" {
			continue
		}
		return instr.Parsed.Info.Source, instr.Parsed.Info.Destination, instr.Parsed.Info.Lamports, true
	}
	return "", "", 0, false
}

// findSPLTransfer correlates pre/post SPL token balances (keyed by account
// index) for wantMint and returns the owner whose balance increased (the
// recipient), the owner whose balance decreased (the sender, if any), and
// the amount received. This balance-delta approach works regardless of
// which instruction variant (transfer, transferChecked, ...) moved the
// tokens.
func findSPLTransfer(meta *solMeta, wantMint string) (recipientOwner string, senderOwner string, amount uint64, ok bool) {
	type balance struct {
		pre, post int64
		owner     string
	}
	byIndex := make(map[int]*balance)
	for _, b := range meta.PreTokenBalances {
		if b.Mint != wantMint {
			continue
		}
		amt, _ := strconv.ParseInt(b.UiTokenAmount.Amount, 10, 64)
		byIndex[b.AccountIndex] = &balance{pre: amt, owner: b.Owner}
	}
	for _, b := range meta.PostTokenBalances {
		if b.Mint != wantMint {
			continue
		}
		amt, _ := strconv.ParseInt(b.UiTokenAmount.Amount, 10, 64)
		entry, exists := byIndex[b.AccountIndex]
		if !exists {
			entry = &balance{}
			byIndex[b.AccountIndex] = entry
		}
		entry.post = amt
		if entry.owner == "" {
			entry.owner = b.Owner
		}
	}

	var recipient, sender *balance
	for _, b := range byIndex {
		delta := b.post - b.pre
		if delta > 0 && (recipient == nil || delta > (recipient.post-recipient.pre)) {
			recipient = b
		}
		if delta < 0 && sender == nil {
			sender = b
		}
	}
	if recipient == nil || recipient.owner == "" {
		return "", "", 0, false
	}
	if sender != nil {
		senderOwner = sender.owner
	}
	return recipient.owner, senderOwner, uint64(recipient.post - recipient.pre), true
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

type btcScriptPubKey struct {
	Address   string   `json:"address"`
	Addresses []string `json:"addresses"`
}

type btcVout struct {
	Value        float64         `json:"value"`
	ScriptPubKey btcScriptPubKey `json:"scriptPubKey"`
}

type btcRawTransaction struct {
	Confirmations uint64    `json:"confirmations"`
	BlockHash     string    `json:"blockhash"`
	Vout          []btcVout `json:"vout"`
}

func (v *BitcoinVerifier) VerifyTransaction(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	if v.rpcURL == "" {
		return &CryptoPaymentResult{Verified: false, Error: "bitcoin RPC URL not configured"}, nil
	}

	resultRaw, err := rpcCall(ctx, v.client, v.rpcURL, "getrawtransaction", []interface{}{req.TxHash, true})
	if err != nil {
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("getrawtransaction: %v", err)}, nil
	}

	var tx btcRawTransaction
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

	// Bitcoin has no accounts and no non-BTC "asset contract" concept in this
	// codebase's payment model, so there is nothing to check beyond which
	// output (if any) actually pays the claimed recipient address. We
	// deliberately do not just take vout[0]: a transaction can have a
	// change output ahead of (or instead of) the payment output, so we scan
	// for the specific address and read its value, ignoring the rest.
	wantRecipient := strings.TrimSpace(req.RecipientAddress)
	if wantRecipient == "" {
		return &CryptoPaymentResult{Verified: false, Error: "recipient_address required to locate the paid output"}, nil
	}

	for _, out := range tx.Vout {
		addr := strings.TrimSpace(out.ScriptPubKey.Address)
		if addr == "" && len(out.ScriptPubKey.Addresses) > 0 {
			addr = strings.TrimSpace(out.ScriptPubKey.Addresses[0])
		}
		if addr == "" || addr != wantRecipient {
			continue
		}
		return &CryptoPaymentResult{
			Verified: true,
			Chain:    "bitcoin",
			// Bitcoin Core's getrawtransaction reports confirmation count,
			// not a block height; ConfirmationBlock has carried this value
			// for bitcoin since the original implementation.
			ConfirmationBlock: tx.Confirmations,
			NativeAsset:       true,
			Asset:             "btc",
			RecipientAddress:  addr,
			Amount:            btcToSatoshis(out.Value),
		}, nil
	}

	return &CryptoPaymentResult{Verified: false, Error: "recipient address not found in transaction outputs"}, nil
}

func btcToSatoshis(btc float64) uint64 {
	if btc < 0 {
		return 0
	}
	return uint64(math.Round(btc * 1e8))
}
