package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Kubo's Bitswap broadcast control (Internal.Bitswap.BroadcastControl, on by
// default since 0.37) only broadcasts wants to peers that previously served
// blocks, to LAN peers, and to peers registered with the peering service. A
// provider that is merely connected, and has never served this node a block,
// is never asked for a CID and the fetch times out. Loopback and public
// addresses are not "LAN". Registering the selected provider with the peering
// service for the duration of one fetch is the narrow, deterministic remedy
// measured on 2026-09-10 (13/13 native files, one provider at a time); it is
// removed again afterwards so no permanent Peering.Peers entry accumulates
// (swarm/peering/add persists to the Kubo config).

const providerPeeringConnectTimeout = 10 * time.Second

// PeerIPFSProvider registers providerAddr (a multiaddr ending in /p2p/<peer>)
// with the Kubo peering service and dials it. The returned release removes the
// peering entry and closes the connection when this call opened it. Release is
// safe to call once; it uses its own bounded context so it also runs after the
// caller's context is done.
func PeerIPFSProvider(ctx context.Context, ipfsAPIURL, providerAddr string) (release func(), err error) {
	providerAddr = strings.TrimSpace(providerAddr)
	peerID, err := peerIDFromMultiaddr(providerAddr)
	if err != nil {
		return nil, err
	}
	connectedBefore, err := kuboPeerConnected(ctx, ipfsAPIURL, peerID)
	if err != nil {
		return nil, err
	}
	if _, err := kuboRPC(ctx, ipfsAPIURL, "swarm/peering/add", map[string]string{"arg": providerAddr}); err != nil {
		return nil, fmt.Errorf("peer provider %s: %w", peerID, err)
	}
	cleanup := func() {
		bg, cancel := context.WithTimeout(context.Background(), providerPeeringConnectTimeout)
		defer cancel()
		_, _ = kuboRPC(bg, ipfsAPIURL, "swarm/peering/rm", map[string]string{"arg": peerID})
		if !connectedBefore {
			_, _ = kuboRPC(bg, ipfsAPIURL, "swarm/disconnect", map[string]string{"arg": providerAddr})
		}
	}
	if _, err := kuboRPC(ctx, ipfsAPIURL, "swarm/connect", map[string]string{"arg": providerAddr, "timeout": providerPeeringConnectTimeout.String()}); err != nil {
		cleanup()
		return nil, fmt.Errorf("connect provider %s: %w", peerID, err)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		cleanup()
	}, nil
}

// FetchIPFSBlockByCIDVia fetches like FetchIPFSBlockByCID after peering with
// and dialing providerAddr for the duration of the fetch.
func FetchIPFSBlockByCIDVia(ctx context.Context, ipfsAPIURL, cidValue, providerAddr string) ([]byte, error) {
	release, err := PeerIPFSProvider(ctx, ipfsAPIURL, providerAddr)
	if err != nil {
		return nil, err
	}
	defer release()
	return FetchIPFSBlockByCID(ctx, ipfsAPIURL, cidValue)
}

// peerIDFromMultiaddr returns the trailing /p2p/<id> (or legacy /ipfs/<id>)
// component of a multiaddr.
func peerIDFromMultiaddr(addr string) (string, error) {
	parts := strings.Split(strings.TrimSpace(addr), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		if (parts[i] == "p2p" || parts[i] == "ipfs") && parts[i+1] != "" && (i+1 == len(parts)-1) {
			return parts[i+1], nil
		}
	}
	return "", fmt.Errorf("provider address %q does not end in /p2p/<peer>", addr)
}

// kuboPeerConnected reports whether peerID appears in swarm/peers.
func kuboPeerConnected(ctx context.Context, ipfsAPIURL, peerID string) (bool, error) {
	body, err := kuboRPC(ctx, ipfsAPIURL, "swarm/peers", nil)
	if err != nil {
		return false, fmt.Errorf("list swarm peers: %w", err)
	}
	var peers struct {
		Peers []struct {
			Peer string `json:"Peer"`
		} `json:"Peers"`
	}
	if err := json.Unmarshal(body, &peers); err != nil {
		return false, fmt.Errorf("decode swarm peers: %w", err)
	}
	for _, p := range peers.Peers {
		if p.Peer == peerID {
			return true, nil
		}
	}
	return false, nil
}

// kuboRPC posts one Kubo RPC command and returns its body; non-2xx is an error.
func kuboRPC(ctx context.Context, ipfsAPIURL, operation string, args map[string]string) ([]byte, error) {
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return nil, errors.New("ipfs api url is required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/"+operation)
	if err != nil {
		return nil, fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	for key, value := range args {
		query.Set(key, value)
	}
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create IPFS request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post IPFS %s: %w", operation, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read IPFS %s response: %w", operation, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("IPFS %s failed with status %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
