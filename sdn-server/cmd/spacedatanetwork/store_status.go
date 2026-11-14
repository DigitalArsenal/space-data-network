package main

import (
	"fmt"
	"io"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// writeStoreStatus reads markers without opening or activating an engine.
func writeStoreStatus(out io.Writer, root string) error {
	m, err := marker.Read(root)
	if err != nil {
		return err
	}
	compatibility, err := update.ReadStoreFormat(root)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "store_backend_format=%d\nstore_stream_format=%d\nstore_layout=%d\nstore_required_layout=%d\n", compatibility.Format, m.Format, m.Layout, m.RequiredLayout)
	return err
}
