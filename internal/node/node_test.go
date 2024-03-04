package node

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/stretchr/testify/require"
)

var testNode *Node
var ctx context.Context
var err error

// TestMain is a special function to setup before any tests run and to shutdown resources after all tests have completed.
func TestMain(m *testing.M) {
	// Any setup required before the tests run.
	ctx = context.TODO()

	testNode, err = NewNode(ctx)
	if err != nil {
		panic(err) // panic here is fine because it's before we run any tests
	}

	// Run all the tests.
	code := m.Run()

	// Any teardown required after the tests run.
	// For example, if you need to stop a testNode:
	// testNode.Stop()

	// Exit with the return code from the test run.
	os.Exit(code)
}

func TestNewNode(t *testing.T) {
	require.NoError(t, err, "NewNode should not return an error")
	require.NotNil(t, testNode, "NewNode should return a non-nil node instance")
	require.NotNil(t, testNode.KeyStore, "NewNode should initialize a KeyStore")
	require.Equal(t, 16, testNode.EntropyBytes, "NewNode should set default EntropyBytes to 16")
}

func TestStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := testNode.Start(ctx)
	defer testNode.Stop() // Ensure we clean up resources after test

	require.NoError(t, err, "Start should not return an error")
	require.NotNil(t, testNode.Host, "Start should initialize the Host")
	require.Implements(t, (*host.Host)(nil), testNode.Host, "Host should implement the host.Host interface")
	require.NotNil(t, testNode.DHT, "Start should initialize the DHT")
}
