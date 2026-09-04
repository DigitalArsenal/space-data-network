package api

// sourcesRouteDecls documents the sources workflow the dashboard drives —
// connectors, sync lanes, per-standard export, signed archives — and the
// operator probe routes. Every payload on these lanes is a Space Data
// Standards FlatBuffer frame (size-prefixed, application/vnd.sdn.flatbuffers.stream):
// $ICN connectors, $DSS sync state and actions, $QRP requests, $DPM archive
// manifests; the export lane streams the standard's own records.
func sourcesRouteDecls() []staticRouteDecl {
	streamHeaders := openAPIObj{
		"X-SDN-Record-Count":  openAPIObj{"description": "Number of frames in the stream.", "schema": openAPIObj{"type": "integer"}},
		"X-SDN-Schema":        openAPIObj{"description": "Schema file of the frames, e.g. DSS.fbs.", "schema": openAPIObj{"type": "string"}},
		"X-SDN-Stream-Format": openAPIObj{"description": "Frame framing, e.g. flatsql-size-prefixed-le-u32.", "schema": openAPIObj{"type": "string"}},
	}
	frames := func(schema, description string) openAPIObj {
		return openAPIObj{
			"description": description,
			"headers":     streamHeaders,
			"content": openAPIObj{
				ContentTypeFlatBufferStream: openAPIObj{
					"schema":      openAPIObj{"type": "string", "format": "binary"},
					"description": "Size-prefixed " + schema + " FlatBuffer frames.",
				},
			},
		}
	}
	frameBody := func(schema, description string) openAPIObj {
		return openAPIObj{
			"required": true,
			"content": openAPIObj{
				ContentTypeFlatBufferStream: openAPIObj{
					"schema":      openAPIObj{"type": "string", "format": "binary"},
					"description": "One size-prefixed " + schema + " frame: " + description,
				},
			},
		}
	}
	pathParam := func(name, description string) openAPIObj {
		return openAPIObj{"name": name, "in": "path", "required": true, "schema": openAPIObj{"type": "string"}, "description": description}
	}
	queryParam := func(name, description string) openAPIObj {
		return openAPIObj{"name": name, "in": "query", "required": false, "schema": openAPIObj{"type": "string"}, "description": description}
	}
	operator := openAPIObj{"description": "Operator session required."}
	errorFrame := openAPIObj{"description": "Error frame ($ERR) naming the refusal; Retry-After when the node asks for a retry."}

	const sourcesTag = "sources"
	const sourcesTagDescription = "Where records come from: connectors ($ICN), sync lanes ($DSS), export and signed archives ($DPM). FlatBuffer frames only."
	const probesTag = "probes"
	const probesTagDescription = "Liveness, readiness and metrics for load balancers and monitoring."

	return []staticRouteDecl{
		{
			path: "/api/v1/connectors", method: "GET",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "listConnectors",
				"summary":     "Connectors this node ingests from",
				"description": "Every configured connector as a $ICN record: origin, kind, status, last fetch and validators.",
				"responses":   openAPIObj{"200": frames("$ICN", "Connector records.")},
			},
		},
		{
			path: "/api/v1/connectors/{connectorId}", method: "GET",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "getConnector",
				"summary":     "One connector",
				"parameters":  []openAPIObj{pathParam("connectorId", "Connector identifier from the list.")},
				"responses":   openAPIObj{"200": frames("$ICN", "The connector record."), "404": errorFrame},
			},
		},
		{
			path: "/api/v1/connectors/{connectorId}/run", method: "POST",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "runConnector",
				"summary":     "Fetch now",
				"description": "Runs the connector once, honouring its politeness window; a run inside the window answers 429 with the next eligible instant.",
				"parameters":  []openAPIObj{pathParam("connectorId", "Connector identifier from the list.")},
				"responses":   openAPIObj{"202": frames("$ICN", "The connector record after the run was accepted."), "401": operator, "429": errorFrame},
			},
		},
		{
			path: "/api/v1/sync", method: "GET",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "listSyncLanes",
				"summary":     "Sync state of every lane",
				"description": "One $DSS record per (standard, provider, source) lane: subscription state, retention rule, pin policy, last sync and counts.",
				"parameters":  []openAPIObj{queryParam("schema", "Standard code, e.g. OMM."), queryParam("provider_id", "Provider identifier."), queryParam("source", "Source lane name."), queryParam("origin", "Origin identifier from $ICN.")},
				"responses":   openAPIObj{"200": frames("$DSS", "Sync lanes.")},
			},
		},
		{
			path: "/api/v1/sync", method: "POST",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "requestSyncAction",
				"summary":     "Subscribe, sync, pause or unsubscribe a lane",
				"description": "The $DSS frame names the lane and the requested state (and, for a subscription, its retention rule and pin policy); the node answers with the lane's $DSS after the action was accepted.",
				"requestBody": frameBody("$DSS", "the lane and the requested state."),
				"responses":   openAPIObj{"202": frames("$DSS", "The lane after the action."), "400": errorFrame, "401": operator},
			},
		},
		{
			path: "/api/v1/sync/{schema}/{providerId}/{sourceName}", method: "GET",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "getSyncLane",
				"summary":     "One lane's sync state",
				"parameters":  []openAPIObj{pathParam("schema", "Standard code, e.g. OMM."), pathParam("providerId", "Provider identifier."), pathParam("sourceName", "Source lane name.")},
				"responses":   openAPIObj{"200": frames("$DSS", "The lane."), "404": errorFrame},
			},
		},
		{
			path: "/api/v1/data/{code}/export", method: "GET",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "exportRecords",
				"summary":     "Download a standard's records",
				"description": "Streams the stored records of one standard as a size-prefixed FlatBuffer file (Content-Disposition names it), optionally one lane, one batch, or an epoch range. Refused with 503 while the record catalog is still loading, so an export is never incomplete.",
				"parameters": []openAPIObj{
					pathParam("code", "Standard code in lower case, e.g. omm."),
					queryParam("source", "Source lane name, or CODE@source."),
					queryParam("provider_id", "Provider identifier."),
					queryParam("batch_id", "Batch identifier."),
					queryParam("from", "Earliest epoch (RFC 3339 or Unix seconds)."),
					queryParam("to", "Latest epoch (RFC 3339 or Unix seconds)."),
				},
				"responses": openAPIObj{"200": frames("record", "The records, as stored."), "400": errorFrame, "401": operator, "503": errorFrame},
			},
		},
		{
			path: "/api/v1/archive", method: "POST",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "createArchive",
				"summary":     "Write a signed archive of a lane",
				"description": "The $QRP request names the standard, provider and source (and optionally an archive id). The node exports the selection, signs a $DPM manifest over the shard and index, pins the three assets and records them as permanent pins.",
				"requestBody": frameBody("$QRP", "the lane to archive."),
				"responses":   openAPIObj{"202": frames("$DPM", "The signed archive manifest."), "400": errorFrame, "401": operator, "503": errorFrame},
			},
		},
		{
			path: "/api/v1/archives", method: "GET",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "listArchives",
				"summary":     "Archives this node holds",
				"description": "Every archive manifest on the node's archive plane, newest first. The X-SDN-Archive-CIDs header names each listed manifest by its CID, in frame order: a signed manifest cannot carry its own hash, and the CID is how an archive is re-imported or fetched.",
				"parameters":  []openAPIObj{queryParam("schema", "Only archives of this standard.")},
				"responses":   openAPIObj{"200": frames("$DPM", "Archive manifests; X-SDN-Archive-CIDs lists their CIDs.")},
			},
		},
		{
			path: "/api/v1/archives/{manifestCid}/asset/{assetCid}", method: "GET",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "getArchiveAsset",
				"summary":     "One archive asset's bytes",
				"description": "The shard, index or manifest bytes of a held archive, verified against the manifest before they are served.",
				"parameters":  []openAPIObj{pathParam("manifestCid", "The archive's manifest CID."), pathParam("assetCid", "The asset CID from the manifest.")},
				"responses": openAPIObj{
					"200": openAPIObj{"description": "The asset bytes.", "content": openAPIObj{"application/octet-stream": openAPIObj{"schema": openAPIObj{"type": "string", "format": "binary"}}}},
					"404": errorFrame,
				},
			},
		},
		{
			path: "/api/v1/archive/import", method: "POST",
			tag: sourcesTag, tagDescription: sourcesTagDescription,
			operation: openAPIObj{
				"operationId": "importArchive",
				"summary":     "Re-import an archive",
				"description": "The $QRP request names the archive by manifest CID (or archive id). The manifest's provider signature and every asset's CID and SHA-256 are verified before a record lands; the answer is the lane's $DSS while the import runs.",
				"requestBody": frameBody("$QRP", "the archive to import."),
				"responses":   openAPIObj{"202": frames("$DSS", "The lane's sync state with the import in flight."), "400": errorFrame, "401": operator, "404": errorFrame},
			},
		},
		{
			path: "/health", method: "GET",
			tag: probesTag, tagDescription: probesTagDescription,
			operation: openAPIObj{
				"operationId": "getHealth",
				"summary":     "Liveness",
				"description": "Answers `ok` while the process serves requests. Also at /api/v1/health.",
				"responses":   openAPIObj{"200": openAPIObj{"description": "ok", "content": openAPIObj{"text/plain": openAPIObj{"schema": openAPIObj{"type": "string"}}}}},
			},
		},
		{
			path: "/ready", method: "GET",
			tag: probesTag, tagDescription: probesTagDescription,
			operation: openAPIObj{
				"operationId": "getReady",
				"summary":     "Readiness",
				"description": "Answers `ready` when the record store is linked, the engine answers within two seconds and the libp2p host is up; otherwise 503 with `not ready: <reason>`. Never waits on a busy store. Also at /api/v1/ready.",
				"responses": openAPIObj{
					"200": openAPIObj{"description": "ready", "content": openAPIObj{"text/plain": openAPIObj{"schema": openAPIObj{"type": "string"}}}},
					"503": openAPIObj{"description": "not ready: store not linked | store busy | engine not answering | libp2p host down", "content": openAPIObj{"text/plain": openAPIObj{"schema": openAPIObj{"type": "string"}}}},
				},
			},
		},
		{
			path: "/metrics", method: "GET",
			tag: probesTag, tagDescription: probesTagDescription,
			operation: openAPIObj{
				"operationId": "getMetrics",
				"summary":     "Prometheus metrics",
				"description": "Node instruments in the Prometheus text format. The numbers describe the host, so an operator session is required.",
				"responses": openAPIObj{
					"200": openAPIObj{"description": "Prometheus text exposition.", "content": openAPIObj{"text/plain": openAPIObj{"schema": openAPIObj{"type": "string"}}}},
					"401": operator,
				},
			},
		},
	}
}
