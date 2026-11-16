package update

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// FeedSignalFile binds a stored signal to the index entry that announces it.
type FeedSignalFile struct {
	Path        string
	Entry       ProviderFeedUpdate
	FeedBaseURL string
}

func ReadTrustRootsFile(path string) (TrustedRoots, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseTrustRoots(raw, "update trust roots")
}

func feedComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`) && filepath.Base(s) == s
}

// FeedSignalFiles reads indexes without following directory symlinks.
func FeedSignalFiles(dir, channel, version string) ([]FeedSignalFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []FeedSignalFile
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "index.json" {
			return nil
		}
		raw, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		feed, err := ParseProviderFeed(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		base, err := ParseFeedBase(feed.FeedBaseURL)
		if err != nil {
			return err
		}
		for _, entry := range feed.Updates {
			if channel != "" && entry.Channel != channel || version != "" && entry.Version != version {
				continue
			}
			for _, c := range []string{entry.Target.Kind, entry.Channel, entry.Target.Platform, entry.Target.Arch, entry.Version} {
				if !feedComponent(c) {
					return fmt.Errorf("invalid feed path component %q", c)
				}
			}
			expected := filepath.Join(entry.Target.Kind, entry.Channel, entry.Target.Platform, entry.Target.Arch)
			if filepath.ToSlash(expected) != filepath.ToSlash(filepath.Dir(path)) {
				return fmt.Errorf("index target disagrees with path %s", path)
			}
			if err := entry.AssertUnderFeed(base); err != nil {
				return err
			}
			files = append(files, FeedSignalFile{Path: filepath.Join(expected, entry.Version, "signal.json"), Entry: entry, FeedBaseURL: feed.FeedBaseURL})
		}
		return nil
	})
	return files, err
}

// MatchFeedSignal compares every modeled field except the signature and timestamps.
func MatchFeedSignal(signal *Signal, file FeedSignalFile) error {
	expected := SignalFromFeedEntry(file.Entry, file.FeedBaseURL, time.Now(), false)
	actual := *signal
	actual.raw = nil
	actual.Signing, expected.Signing = SignalSigning{}, SignalSigning{}
	actual.PublishedAt, expected.PublishedAt = "", ""
	actual.ExpiresAt, expected.ExpiresAt = "", ""
	if !reflect.DeepEqual(&actual, expected) {
		return errors.New("signed signal disagrees with the served index entry")
	}
	return nil
}

func VerifyFeedSignal(raw []byte, roots TrustedRoots, file FeedSignalFile) (*Signal, error) {
	signal, err := ParseSignal(raw)
	if err != nil {
		return nil, err
	}
	if err := signal.Verify(SignalVerifyOptions{TrustedRoots: roots, FeedBaseURL: file.FeedBaseURL}); err != nil {
		return nil, err
	}
	if err := MatchFeedSignal(signal, file); err != nil {
		return nil, err
	}
	return signal, nil
}

// SignFeedSignals uses the receiver's statement and verifier for each index entry.
func SignFeedSignals(dir string, key ed25519.PrivateKey, roots TrustedRoots) error {
	pub := key.Public().(ed25519.PublicKey)
	id := RootKeyID(pub)
	trusted, ok := roots[id]
	if !ok {
		return errors.New("signal signing key is not an update root")
	}
	decoded, err := decodeTrustedPublicKey(trusted)
	if err != nil || !pub.Equal(decoded) {
		return errors.New("signal signing key does not match its update root")
	}
	files, err := FeedSignalFiles(dir, "", "")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("feed contains no entries")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, file := range files {
		signal := SignalFromFeedEntry(file.Entry, file.FeedBaseURL, now, false)
		signal.ExpiresAt = now.Add(SignalMaxTTL).Format(time.RFC3339)
		signal.Signing.KeyID, signal.Signing.PublicKey = id, base64.StdEncoding.EncodeToString(spki)
		unsigned, err := signal.Marshal()
		if err != nil {
			return err
		}
		statement, err := SignalStatement(unsigned)
		if err != nil {
			return err
		}
		signal.Signing.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, statement))
		raw, err := json.Marshal(signal)
		if err != nil {
			return err
		}
		if _, err := VerifyFeedSignal(raw, roots, file); err != nil {
			return err
		}
		if err := root.WriteFile(file.Path, raw, 0o644); err != nil {
			return err
		}
		written, err := root.ReadFile(file.Path)
		if err != nil {
			return err
		}
		if _, err := VerifyFeedSignal(written, roots, file); err != nil {
			return err
		}
	}
	return nil
}

// WithdrawFeedVersion removes index entries before moving their payloads outside the feed.
func WithdrawFeedVersion(dir, channel, version string) (string, error) {
	if !feedComponent(channel) || !feedComponent(version) {
		return "", errors.New("channel and version must be path components")
	}
	files, err := FeedSignalFiles(dir, channel, version)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", errors.New("version is absent or withdrawn")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	archive := absolute + "-withdrawn-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Mkdir(archive, 0o700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return archive, err
	}
	defer root.Close()
	indexes := map[string]bool{}
	folders := map[string]bool{}
	for _, file := range files {
		folders[filepath.Dir(file.Path)] = true
		indexes[filepath.Join(filepath.Dir(filepath.Dir(file.Path)), "index.json")] = true
	}
	for path := range indexes {
		raw, err := root.ReadFile(path)
		if err != nil {
			return archive, err
		}
		feed, err := ParseProviderFeed(raw)
		if err != nil {
			return archive, err
		}
		kept := make([]ProviderFeedUpdate, 0, len(feed.Updates))
		for _, entry := range feed.Updates {
			if entry.Channel != channel || entry.Version != version {
				kept = append(kept, entry)
			}
		}
		feed.Updates, feed.GeneratedAt = kept, time.Now().UTC().Format(time.RFC3339)
		raw, err = json.Marshal(feed)
		if err != nil {
			return archive, err
		}
		temp := path + ".withdraw-" + time.Now().UTC().Format("150405.000000000")
		if err := root.WriteFile(temp, raw, 0o644); err != nil {
			return archive, err
		}
		if err := root.Rename(temp, path); err != nil {
			return archive, err
		}
	}
	for folder := range folders {
		source := filepath.Join(absolute, folder)
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			return archive, err
		}
		if resolved != source {
			return archive, errors.New("refusing a symlinked release directory")
		}
		target := filepath.Join(archive, folder)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return archive, err
		}
		if err := os.Rename(source, target); err != nil {
			return archive, err
		}
	}
	return archive, nil
}
