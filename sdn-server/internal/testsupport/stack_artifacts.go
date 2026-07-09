package testsupport

import (
	"os"
	"path/filepath"
)

func appendStackPackageArtifactCandidates(candidates []string, anchorDir string, suffixes [][]string) []string {
	stackRoot, ok := findNearestStackRoot(anchorDir)
	if !ok {
		return candidates
	}
	for _, suffix := range suffixes {
		if len(suffix) == 0 {
			continue
		}
		candidates = append(candidates,
			filepath.Join(append([]string{stackRoot, "repos", "main-packages"}, suffix...)...),
			filepath.Join(append([]string{stackRoot, "repos", "ancillary-packages"}, suffix...)...),
		)
	}
	return candidates
}

func findNearestStackRoot(start string) (string, bool) {
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "docs", "repository-catalog.md")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "repos", "main-packages")); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}
