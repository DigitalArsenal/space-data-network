package main

import "github.com/spacedatanetwork/sdn-server/internal/versioninfo"

// servingAPI names the serving routes this build answers. A fleet harness
// refuses a mixed fleet by comparing build_sha256 across nodes instead of
// discovering an older binary through an HTTP 404 on a newer route
// (node-transfer tests, 2026-09-10: Vimpel had no /api/v1/data/remote).
var servingAPI = []string{
	"catalog",
	"catalog.index_state",
	"data.query",
	"data.search",
	"data.remote",
	"publish.batch",
}

// recordForm is the canonical stored record form every stream reader hands
// to consumers: a bare finished FlatBuffer, file identifier at byte 4.
const recordForm = "bare"

// executableSHA256 is the running executable's sha256, reported as
// build_sha256. One implementation for every surface that reports it
// (versioninfo.BuildSHA256): /api/node/info here, /api/v1/id in internal/api.
var executableSHA256 = versioninfo.BuildSHA256
