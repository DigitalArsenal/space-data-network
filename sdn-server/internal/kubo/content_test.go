package kubo_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipfs/boxo/files"
	"github.com/ipfs/boxo/ipld/merkledag"
	"github.com/ipfs/kubo/core/coreapi"
	"github.com/ipfs/kubo/core/coreiface/options"

	"github.com/spacedatanetwork/sdn-server/internal/kubo"
)

// Held and Open read publication content from the repository alone: a
// chunked UnixFS file (a shard) and a raw block (an index) are held with
// their byte sizes and read back byte for byte, seeking included; a DAG with
// one block missing is not held, and nothing is fetched to make it so.
func TestHeldContentIsWholeAndLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	key, _ := newKey(t)
	n := startConfig(t, kubo.Config{RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: key, DHT: kubo.DHTOff})
	api, err := coreapi.NewCoreAPI(n.IPFS())
	if err != nil {
		t.Fatal(err)
	}
	shard := make([]byte, 300_000)
	if _, err := rand.Read(shard); err != nil {
		t.Fatal(err)
	}
	added, err := api.Unixfs().Add(ctx, files.NewBytesFile(shard), options.Unixfs.Chunker("size-65536"), options.Unixfs.Pin(false, ""))
	if err != nil {
		t.Fatal(err)
	}
	shardCID := added.RootCid().String()
	index := []byte("an index, one raw block")
	block, err := api.Block().Put(ctx, bytes.NewReader(index))
	if err != nil {
		t.Fatal(err)
	}
	indexCID := block.Path().RootCid().String()

	for _, c := range []struct {
		cid  string
		want []byte
	}{{shardCID, shard}, {indexCID, index}} {
		size, held := n.Held(c.cid)
		if !held || size != int64(len(c.want)) {
			t.Fatalf("Held(%s) = %d, %v; want %d, true", c.cid, size, held, len(c.want))
		}
		r, size, err := n.Open(c.cid)
		if err != nil || size != int64(len(c.want)) {
			t.Fatalf("Open(%s) = %d, %v", c.cid, size, err)
		}
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, c.want) {
			t.Fatalf("Open(%s) read %d bytes (%v), want the %d added", c.cid, len(got), err, len(c.want))
		}
		if _, err := r.Seek(int64(len(c.want)/2), io.SeekStart); err != nil {
			t.Fatal(err)
		}
		tail, _ := io.ReadAll(r)
		if !bytes.Equal(tail, c.want[len(c.want)/2:]) {
			t.Fatalf("Open(%s) after a seek read the wrong bytes", c.cid)
		}
		r.Close()
	}

	// One leaf of the shard goes: the shard is no longer held.
	root, err := n.IPFS().DAG.Get(ctx, added.RootCid())
	if err != nil {
		t.Fatal(err)
	}
	links := root.Links()
	if len(links) < 2 {
		t.Fatalf("the shard has %d links, want a chunked DAG", len(links))
	}
	if err := n.IPFS().Blockstore.DeleteBlock(ctx, links[1].Cid); err != nil {
		t.Fatal(err)
	}
	if _, held := n.Held(shardCID); held {
		t.Fatal("a shard with a missing block is reported held")
	}
	online, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if err := merkledag.FetchGraph(online, added.RootCid(), n.IPFS().DAG); err == nil {
		// Only a fetch over the network could complete it, and this node has
		// no peers: the online DAG must fail too, so Held did not hide a fetch.
		t.Fatal("the shard completed with no peer to fetch from")
	}
	if _, held := n.Held("not-a-cid"); held {
		t.Fatal("a malformed CID is reported held")
	}
}
