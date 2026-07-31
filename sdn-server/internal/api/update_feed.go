package api

// The SDN update feed — GET /updates/<kind>/<channel>/<platform>/<arch>/…
//
// OWNER RULING 2026-07-30 (graph/tasks/sdn-signed-updater.md): the update
// server RUNS ON sdn.spaceaware.io. There is no updates.spacedatanetwork.org,
// no second hostname, no second certificate — that hostname's DNS/HTTPS
// blocker is what parked this work since 2026-05 and it is MOOT.
//
// WHY THE DAEMON SERVES IT AND NOT A WEB SERVER. host-01 has no Caddy and no
// nginx: the daemon itself owns :443 (verified live 2026-07-30 — the only
// listener on *:443 is the spacedatanetwork process). A static feed therefore
// either lives inside this binary's routing or it does not exist.
//
// WHY THE ROUTE IS REGISTERED IN THIS PACKAGE. The sibling static trees
// (/wallet-wasm/, /wallet-ui/) mount in cmd/spacedatanetwork/main.go, which was
// under another task's claim when this landed; contending a claimed write scope
// is a graph-protocol violation, not a merge inconvenience. RegisterRoutes is
// the api package's own registration seam, already called by main.go, so the
// route lands with no edit to a file this task does not own.
//
// WHAT THIS SURFACE IS AND IS NOT. It is a read-only byte pump over a directory
// an operator populated out of band. It is NOT the trust boundary: every byte
// it serves is verified by the client against a signed manifest before anything
// is installed (internal/update.Verify, desktop/src/sdn-updater/manifest.js) and
// both verifiers fail closed. A compromised feed directory can therefore stop
// updates or serve stale ones; it cannot install anything. That is exactly why
// serving it from the daemon is acceptable while SIGNING from the daemon needed
// a Seal Council ruling.

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// UpdateFeedRoute is the subtree this surface occupies. The trailing slash
// makes it a subtree match; a request for the bare path is not served.
//
// IT IS DELIBERATELY NOT UNDER /api/. On a require_auth node — which host-01
// is — serveAdminMuxRequest (cmd/spacedatanetwork/main.go) default-denies every
// path beginning with /api/ unless it appears in isPublicReadAPIPath's literal
// allow-list. An update feed under /api/ therefore 401s to the entire fleet
// until a list in main.go names it, and main.go's route/classifier block is
// under another task's claim. It is also simply the wrong shape: the feed must
// be readable by a client that has no session and never will have one.
//
// A top-level static subtree is the pattern this daemon already uses for
// exactly this class of surface (/wallet-wasm/, /wallet-ui/): outside /api/,
// served straight off the admin mux, public by construction rather than by
// allow-list entry.
const UpdateFeedRoute = "/updates/"

// updateFeedDirEnv points at the directory holding the generated feed tree
// (deployment/release/build-sdn-update-feed.js output).
//
// It is an ENVIRONMENT variable rather than a config-file field on purpose:
// config schema changes ripple into every deployed config and into files under
// other agents' claims, while this is a per-host deploy detail — the same class
// of knob as LD_LIBRARY_PATH in the unit file. Unset = the route does not
// mount, which is the correct default for every node that is not the publisher.
const updateFeedDirEnv = "SDN_UPDATE_FEED_DIR"

// updateFeedExts allow-lists what the feed may serve: manifests and index
// documents (.json) and the inert update carrier (.wasm). Everything else 404s.
//
// The allow-list is not paranoia about these two types; it is about every other
// type. An operator drops a generated tree here, and a stray .html, .sh or
// .service file in that tree would otherwise become fetchable from this node's
// own origin. Serving only what the update protocol names keeps a
// directory-population mistake from becoming a content-injection surface.
var updateFeedExts = map[string]string{
	".json": "application/json; charset=utf-8",
	".wasm": "application/wasm",
}

// updateFeedMaxDepth bounds how deep under the feed root a request may reach.
// The generated tree is exactly <kind>/<channel>/<platform>/<arch>/<version>/
// <file> — six segments. Anything deeper is not part of a feed and is refused
// rather than walked.
const updateFeedMaxDepth = 6

// registerUpdateFeedRoutes mounts the feed when this host is configured to
// publish one, and says so either way: a silent no-mount here looks exactly
// like a network fault to the fleet, and "the update lane is down" is not a
// diagnosis anyone should have to make twice.
func (h *CoreAPIHandler) registerUpdateFeedRoutes(mux *http.ServeMux) {
	dir := strings.TrimSpace(os.Getenv(updateFeedDirEnv))
	if dir == "" {
		return
	}
	resolved, err := filepath.Abs(dir)
	if err != nil {
		log.Errorf("Update feed NOT served at %s: %s=%q could not be resolved: %v", UpdateFeedRoute, updateFeedDirEnv, dir, err)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		log.Errorf("Update feed NOT served at %s: %s=%q is not a readable directory. The fleet will see no available updates.", UpdateFeedRoute, updateFeedDirEnv, resolved)
		return
	}

	mux.HandleFunc(UpdateFeedRoute, h.withRL(makeUpdateFeedHandler(resolved)))
	log.Infof("Update feed served at %s from %s (public GET/HEAD, .json and .wasm only). Clients verify every payload against its signed manifest before installing.", UpdateFeedRoute, resolved)
}

// makeUpdateFeedHandler serves the feed tree read-only.
//
// The root is symlink-resolved ONCE here rather than per request, because the
// containment check below compares it against a symlink-resolved candidate
// path. Comparing a resolved child to an unresolved parent fails for any root
// reached through a symlink — /var -> /private/var on macOS, and, more to the
// point in production, a feed directory placed on a mounted volume and reached
// through a convenience symlink. That mismatch does not fail open; it fails
// CLOSED, 404ing an otherwise healthy feed, which is the kind of outage that
// gets diagnosed as "the update server is broken" rather than as a path bug.
func makeUpdateFeedHandler(root string) http.HandlerFunc {
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "the update feed is read-only")
			return
		}

		rel, ok := updateFeedRelPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		contentType, allowed := updateFeedExts[strings.ToLower(path.Ext(rel))]
		if !allowed {
			http.NotFound(w, r)
			return
		}

		full := filepath.Join(root, filepath.FromSlash(rel))
		// Belt to the parser's braces: even though updateFeedRelPath has
		// already rejected traversal, confirm the resolved path is still inside
		// the root before opening it. A symlink inside the tree is the case the
		// string check alone cannot see.
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
			log.Warnf("Update feed refused %q: it resolves outside the feed root", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		file, err := os.Open(resolved)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()

		info, err := file.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// The feed is immutable per version: a manifest or carrier at a given
		// version path never changes content. index.json does, so it is the one
		// document that must not be cached hard.
		if path.Base(rel) == "index.json" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	}
}

// updateFeedRelPath turns a request path into a clean relative path under the
// feed root, or reports that it is not servable.
//
// path.Clean collapses any "..", and the explicit rejection afterwards catches
// the case where cleaning ESCAPED the subtree rather than merely normalizing
// inside it.
func updateFeedRelPath(urlPath string) (string, bool) {
	if !strings.HasPrefix(urlPath, UpdateFeedRoute) {
		return "", false
	}
	rel := strings.TrimPrefix(urlPath, UpdateFeedRoute)
	if rel == "" {
		return "", false
	}
	// A NUL or a backslash has no place in a feed path and is a decoding
	// artifact or an attack, never a real generated filename.
	if strings.ContainsAny(rel, "\x00\\") {
		return "", false
	}

	// REFUSE ".." rather than normalizing it away. path.Clean on an absolute
	// path CLAMPS a leading "../" at the root instead of escaping, so a
	// traversal attempt would be silently rewritten into a legitimate-looking
	// request for a different file inside the feed root — served with a 200 and
	// no trace that the client asked to leave. Refusing outright means the only
	// paths this surface ever serves are the ones the generator actually laid
	// down, and a probe reads as a probe in the logs.
	for _, segment := range strings.Split(rel, "/") {
		if segment == ".." {
			return "", false
		}
	}

	cleaned := path.Clean("/" + rel)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", false
	}
	segments := strings.Split(cleaned, "/")
	if len(segments) > updateFeedMaxDepth {
		return "", false
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") {
			return "", false
		}
	}
	return cleaned, true
}
