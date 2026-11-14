package hostops

// The node's durable page operation reuses PIV raw ports and StreamResult.
// Its request is QRP and its stream is a QRP page followed by record frames.
func init() {
	Operations["storage.index_page"] = Operation{Capability: "storage_query", Result: "StreamResult",
		RawIn: []string{"request"}, RawOut: []string{"stream"}}
}
