package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// writeStoreStatus reads markers without opening or activating an engine,
// and names the embedded standards the stream store leaves unregistered
// (storage.SealedRuleRefusals: read from the binary, not the store).
func writeStoreStatus(out io.Writer, root string) error {
	m, err := marker.Read(root)
	if err != nil {
		return err
	}
	compatibility, err := update.ReadStoreFormat(root)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(out, "store_backend_format=%d\nstore_stream_format=%d\nstore_layout=%d\nstore_required_layout=%d\n", compatibility.Format, m.Format, m.Layout, m.RequiredLayout); err != nil {
		return err
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	refused := storage.SealedRuleRefusals(validator)
	names := make([]string, 0, len(refused))
	for _, r := range refused {
		names = append(names, r.Schema)
	}
	list := strings.Join(names, ",")
	if list == "" {
		list = "none"
	}
	if _, err = fmt.Fprintf(out, "store_unregistered_standards=%s\n", list); err != nil {
		return err
	}
	for _, r := range refused {
		if _, err = fmt.Fprintf(out, "store_unregistered_standard=%s: %v\n", r.Schema, r.Err); err != nil {
			return err
		}
	}
	return nil
}
