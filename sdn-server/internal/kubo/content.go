package kubo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ipfs/boxo/blockservice"
	offline "github.com/ipfs/boxo/exchange/offline"
	"github.com/ipfs/boxo/files"
	"github.com/ipfs/boxo/ipld/merkledag"
	unixfile "github.com/ipfs/boxo/ipld/unixfs/file"
	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
)

// contentWait bounds one local read of the repository: it never waits on the
// network, so a slow answer is a slow disk.
const contentWait = 30 * time.Second

// offlineDAG is the repository's DAG with no exchange: a block that is not
// local is an error, never a fetch.
func (n *Node) offlineDAG() ipld.DAGService {
	bs := n.ipfs.Blockstore
	return merkledag.NewDAGService(blockservice.New(bs, offline.Exchange(bs)))
}

// localFile opens the UnixFS file (or raw block) of c from the repository
// alone.
func (n *Node) localFile(ctx context.Context, c cid.Cid) (files.File, error) {
	dag := n.offlineDAG()
	root, err := dag.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	node, err := unixfile.NewUnixfsFile(ctx, dag, root)
	if err != nil {
		return nil, err
	}
	file, ok := node.(files.File)
	if !ok {
		node.Close()
		return nil, fmt.Errorf("%s is not a file", c)
	}
	return file, nil
}

// Held reports whether the repository holds every block of the DAG of
// cidText, without asking the network, and the byte size of its content (a
// UnixFS file, or a raw block's bytes).
func (n *Node) Held(cidText string) (int64, bool) {
	c, err := cid.Decode(cidText)
	if err != nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), contentWait)
	defer cancel()
	if err := merkledag.FetchGraph(ctx, c, n.offlineDAG()); err != nil {
		return 0, false
	}
	file, err := n.localFile(ctx, c)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	size, err := file.Size()
	if err != nil {
		return 0, false
	}
	return size, true
}

// Open reads the content of cidText from the repository alone. The returned
// reader seeks within the content.
func (n *Node) Open(cidText string) (io.ReadSeekCloser, int64, error) {
	c, err := cid.Decode(cidText)
	if err != nil {
		return nil, 0, err
	}
	file, err := n.localFile(context.Background(), c)
	if err != nil {
		return nil, 0, err
	}
	size, err := file.Size()
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	if size < 0 {
		file.Close()
		return nil, 0, errors.New("content of unknown size")
	}
	seeker, ok := file.(io.ReadSeekCloser)
	if !ok {
		file.Close()
		return nil, 0, fmt.Errorf("content of %s does not seek", c)
	}
	return seeker, size, nil
}
