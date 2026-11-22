package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ipfs/boxo/ipns"
)

// The node's embedded Kubo routes over libp2p only (no delegated routers or
// publishers), and an online Kubo answers name/publish only after its DHT put.
// On a small host with a thin routing table that put can outlast any deadline,
// and the name then never moves (2026-11-19 to 11-21 on host-02). Kubo signs
// the record and stores it locally BEFORE the DHT put, so the node reads that
// signed record back with an offline read and hands it to Routing V1 HTTP
// publishers itself (PUT /routing/v1/ipns/<name>), which put it into the DHT.
// The record is signed by Kubo's key and verified by every reader; the
// publisher only transports it.

// IPNSRecordContentType is the Routing V1 media type of a signed IPNS record.
const IPNSRecordContentType = "application/vnd.ipfs.ipns-record"

// maxIPNSRecordBytes bounds a record read from Kubo (the spec caps records at 10 KiB).
const maxIPNSRecordBytes = 10 * 1024

// LocalIPNSRecord is a signed IPNS record as Kubo stores it, with the fields
// the node checks before handing it on.
type LocalIPNSRecord struct {
	Raw      []byte
	Value    string
	Sequence uint64
	Validity time.Time
}

// KuboKeyIPNSName returns the IPNS name (k51... form) of a Kubo key.
func KuboKeyIPNSName(ctx context.Context, ipfsAPIURL, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("ipns key is required")
	}
	// Kubo's text form of key/list -l: one "<IPNS name> <key name>" line per key.
	body, err := postKuboRPC(ctx, ipfsAPIURL, "/api/v0/key/list", url.Values{"l": {"true"}, "ipns-base": {"base36"}, "encoding": {"text"}}, 1<<20)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == key {
			if _, err := ipns.NameFromString(fields[0]); err != nil {
				return "", fmt.Errorf("Kubo key %q has an IPNS name %q that does not parse: %w", key, fields[0], err)
			}
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("Kubo has no key %q", key)
}

// ReadLocalIPNSRecord reads the signed record Kubo holds for name from its own
// datastore (name/get with offline=true: no network lookup), and checks it
// against the name. ok is false when Kubo holds no record for the name.
func ReadLocalIPNSRecord(ctx context.Context, ipfsAPIURL, name string) (LocalIPNSRecord, bool, error) {
	parsed, err := ipns.NameFromString(strings.TrimSpace(name))
	if err != nil {
		return LocalIPNSRecord{}, false, fmt.Errorf("ipns name: %w", err)
	}
	raw, err := postKuboRPC(ctx, ipfsAPIURL, "/api/v0/name/get", url.Values{"arg": {parsed.String()}, "offline": {"true"}}, maxIPNSRecordBytes)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return LocalIPNSRecord{}, false, nil
		}
		return LocalIPNSRecord{}, false, err
	}
	rec, err := ipns.UnmarshalRecord(raw)
	if err != nil {
		return LocalIPNSRecord{}, false, fmt.Errorf("local IPNS record for %s: %w", name, err)
	}
	if err := ipns.ValidateWithName(rec, parsed); err != nil {
		return LocalIPNSRecord{}, false, fmt.Errorf("local IPNS record for %s does not verify: %w", name, err)
	}
	value, err := rec.Value()
	if err != nil {
		return LocalIPNSRecord{}, false, fmt.Errorf("local IPNS record value: %w", err)
	}
	seq, err := rec.Sequence()
	if err != nil {
		return LocalIPNSRecord{}, false, fmt.Errorf("local IPNS record sequence: %w", err)
	}
	validity, err := rec.Validity()
	if err != nil {
		return LocalIPNSRecord{}, false, fmt.Errorf("local IPNS record validity: %w", err)
	}
	return LocalIPNSRecord{Raw: raw, Value: value.String(), Sequence: seq, Validity: validity}, true, nil
}

// PutIPNSRecordToPublisher sends a signed record to one Routing V1 HTTP
// publisher (base URL such as https://delegated-ipfs.dev).
func PutIPNSRecordToPublisher(ctx context.Context, publisherURL, name string, record []byte) error {
	base := strings.TrimRight(strings.TrimSpace(publisherURL), "/")
	if base == "" {
		return fmt.Errorf("delegated publisher url is required")
	}
	endpoint, err := url.JoinPath(base, "/routing/v1/ipns/", strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("build delegated publisher URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(record))
	if err != nil {
		return fmt.Errorf("create delegated publish request: %w", err)
	}
	req.Header.Set("Content-Type", IPNSRecordContentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("put IPNS record to %s: %w", base, err)
	}
	defer resp.Body.Close()
	body, _, _ := readBoundedKuboBody(resp.Body, maxKuboCommandErrorBodyBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("put IPNS record to %s: status %d: %s", base, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// postKuboRPC posts one Kubo RPC command and returns its body, bounded by limit.
func postKuboRPC(ctx context.Context, ipfsAPIURL, command string, query url.Values, limit int64) ([]byte, error) {
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return nil, fmt.Errorf("ipfs api url is required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), command)
	if err != nil {
		return nil, fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse IPFS URL: %w", err)
	}
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create IPFS %s request: %w", command, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post IPFS %s: %w", command, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _, _ := readBoundedKuboBody(resp.Body, maxKuboCommandErrorBodyBytes)
		return nil, fmt.Errorf("IPFS %s failed with status %d: %s", command, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read IPFS %s response: %w", command, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("IPFS %s response exceeds %d bytes", command, limit)
	}
	return body, nil
}
