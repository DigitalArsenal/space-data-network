package api

import (
	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"testing"
)

// Publication fixtures bind the same pin policy as the daemon to their local Kubo server.
func newPublicationServiceWithPinPolicy(t *testing.T, store *storage.FlatSQLStore,
	publisher DatasetUpdatePublisher, signingKey []byte, providerPeerID, providerEPMCID, ipfsAPIURL, outputDir string,
) *ConcreteDatasetPublicationService {
	t.Helper()
	service := NewConcreteDatasetPublicationService(store, publisher, signingKey, providerPeerID, providerEPMCID, ipfsAPIURL, outputDir)
	policy, err := pinpolicy.New(ipfsAPIURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetPinPolicy(policy)
	return service
}
