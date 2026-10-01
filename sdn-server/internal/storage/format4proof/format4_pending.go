package format4proof

import "errors"

// errFormat4Pending: the engine API (internal/storage/format4, contract §5.2)
// has not reached this branch, so format-4 stores cannot be digested or
// verified through it yet. A format-4 crash round reports this as a
// violation rather than passing silently.
var errFormat4Pending = errors.New("format4proof: the format4 package (contract §5.2) is not in this build")

// VerifyFormat4Store checks a closed format-4 store: REBUILD verify reports 0
// mismatches and every file passes integrity_check. It returns the problems.
func VerifyFormat4Store(store string) []string { return []string{errFormat4Pending.Error()} }

func digestFormat4(store string, schemas []string) (map[string]TypeDigest, error) {
	return nil, errFormat4Pending
}
