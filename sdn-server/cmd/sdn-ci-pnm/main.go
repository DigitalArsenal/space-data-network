package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const defaultManifestCID = "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"

func main() {
	fileID := flag.String("file-id", "", "PNM FILE_ID to embed")
	manifestCID := flag.String("cid", defaultManifestCID, "manifest CID to embed")
	fileName := flag.String("file-name", "sdn-live-dht-smoke.dpm", "PNM FILE_NAME to embed")
	address := flag.String("addr", "", "PNM MULTIFORMAT_ADDRESS to embed")
	publishedAt := flag.String("published-at", time.Now().UTC().Format(time.RFC3339), "PNM publish timestamp")
	flag.Parse()

	if strings.TrimSpace(*fileID) == "" {
		_, _ = fmt.Fprintln(os.Stderr, "--file-id is required")
		os.Exit(2)
	}
	addr := strings.TrimSpace(*address)
	if addr == "" {
		addr = "/ipfs/" + strings.TrimSpace(*manifestCID)
	}

	pnm := sds.NewPNMBuilder().
		WithFileID(strings.TrimSpace(*fileID)).
		WithCID(strings.TrimSpace(*manifestCID)).
		WithFileName(strings.TrimSpace(*fileName)).
		WithMultiformatAddress(addr).
		WithPublishTimestamp(strings.TrimSpace(*publishedAt)).
		WithSignature("").
		WithSignatureType("").
		Build()

	fmt.Println(base64.StdEncoding.EncodeToString(pnm))
}
