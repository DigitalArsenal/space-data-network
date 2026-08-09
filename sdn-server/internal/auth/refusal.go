package auth

import (
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/gateway"
)

// writeAuthRefusal is the ONLY way the auth wall answers 401/403.
//
// Every refusal in middleware.go goes through here so that "a refusal is
// readable by the browser it refuses" is a property of the WALL rather than of
// whichever line happened to be updated. The decoration itself, and the reasons
// it is deliberately narrower than the public-surface one, live in
// internal/gateway/cors.go — the package that already owns the single predicate
// serving the wall, CORS, CSRF and the OpenAPI stamp.
//
// TestEveryAuthWallRefusalGoesThroughWriteAuthRefusal pins that no bare
// writeJSON refusal creeps back into middleware.go.
func writeAuthRefusal(w http.ResponseWriter, r *http.Request, status int, body errorResponse) {
	gateway.DecorateRefusal(w, r)
	writeJSON(w, status, body)
}
