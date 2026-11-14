package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Each historical batch uses one 48-byte checkpoint frame rather than a file.
// An eight-byte checksum detects incomplete durable appends.
func (s *FlatSQLStore) appendPublicationReceipt(id [32]byte, seq uint64) error {
	if s.publicationReceipts == nil {
		s.publicationReceipts = map[[32]byte]uint64{}
	}
	if s.publicationReceipts[id] != 0 {
		return nil
	}
	var frame [48]byte
	copy(frame[:32], id[:])
	binary.LittleEndian.PutUint64(frame[32:40], seq)
	sum := sha256.Sum256(frame[:40])
	copy(frame[40:], sum[:8])
	f, err := os.OpenFile(filepath.Join(s.publicationDir(), "receipts.checkpoint"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(frame[:])
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	s.publicationMu.Lock()
	s.publicationReceipts[id] = seq
	s.publicationMu.Unlock()
	return publicationSyncDir(s.publicationDir())
}
func (s *FlatSQLStore) savePublicationReceipt(e publicationEntry) error {
	return s.appendPublicationReceipt(sha256.Sum256([]byte(e.Schema+"\x00"+e.Tags.ProviderID+"\x00"+e.Tags.SourceName+"\x00"+e.Tags.BatchID)), e.Sequence)
}
func (s *FlatSQLStore) recoverPublicationReceipts() error {
	path := filepath.Join(s.publicationDir(), "receipts.checkpoint")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	receipts := map[[32]byte]uint64{}
	valid := 0
	for valid+48 <= len(raw) {
		frame := raw[valid : valid+48]
		sum := sha256.Sum256(frame[:40])
		if !bytes.Equal(frame[40:], sum[:8]) {
			return errors.New("publication receipt checkpoint checksum mismatch")
		}
		var id [32]byte
		copy(id[:], frame[:32])
		seq := binary.LittleEndian.Uint64(frame[32:40])
		receipts[id] = max(receipts[id], seq)
		s.publicationHighWater = max(s.publicationHighWater, seq)
		valid += 48
	}
	if valid != len(raw) {
		if err = os.Truncate(path, int64(valid)); err != nil {
			return err
		}
	}
	s.publicationMu.Lock()
	s.publicationReceipts = receipts
	s.publicationMu.Unlock()
	dirs, err := os.ReadDir(s.publicationDir())
	if err != nil {
		return err
	}
	for _, d := range dirs {
		p := filepath.Join(s.publicationDir(), d.Name())
		if strings.HasPrefix(d.Name(), "receipt-") {
			idBytes, err := hex.DecodeString(strings.TrimPrefix(d.Name(), "receipt-"))
			if err != nil || len(idBytes) != 32 {
				continue
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			seq, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
			if err != nil {
				return err
			}
			var id [32]byte
			copy(id[:], idBytes)
			if err = s.appendPublicationReceipt(id, seq); err != nil {
				return err
			}
			if err = os.Remove(p); err != nil {
				return err
			}
		}
		if strings.HasPrefix(d.Name(), ".write-") {
			if err = os.Remove(p); err != nil {
				return err
			}
		}
		if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			temps, _ := filepath.Glob(filepath.Join(p, ".write-*"))
			for _, temp := range temps {
				if err = os.Remove(temp); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
