package gateway

import "regexp"

// dataRoute mirrors DATA_ROUTE in the stack's graph/checks/json-boundary.mjs
// exactly, less its optional "METHOD " prefix. Change both together.
var dataRoute = regexp.MustCompile(`^(?:/api/v1/(?:data|channels|catalog|search|log|query|apps|sync|archives?|claims|connectors|nodes|export|pubsub|storefront|flows/deploy|conjunction|demo|modules/customer|plugins/upload|admin/(?:publish|dataset-updates|directory))\b|/ws$|/\.well-known/sdn/|/api/module-delivery/|/api/apps\b|/api/storefront\b|/api/node/(?:info|epm)|/api/peers/(?:sdn|graph)|/api/directory/|/api/v1/address-book)`)

// IsDataRoute reports whether path is on the data plane, whose every answer,
// errors included, is SDS FlatBuffers.
func IsDataRoute(path string) bool { return dataRoute.MatchString(path) }
