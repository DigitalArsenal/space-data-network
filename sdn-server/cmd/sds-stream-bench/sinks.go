package main

// The sinks the same synthesised stream is driven into. They exist so the
// store's number has a FLOOR to be compared against: a throughput figure with
// no baseline cannot distinguish "the store is slow" from "the disk is slow",
// and the owner's requirement is wire speed MINUS BACK PRESSURE, which is a
// ratio and not an absolute.
//
//	discardSink — synthesis only. The ceiling no sink can beat.
//	fileSink    — the same records appended to a plain file, either buffered
//	              (wire speed) or fsynced at the store's own commit cadence
//	              (the durable floor).
//	storeSink   — storage.FlatSQLStore, the real on-disk record store.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// sink consumes one schema's batch of freshly synthesised records.
type sink interface {
	write(schema string, records [][]byte) error
	close() error
}

type discardSink struct{}

func (discardSink) write(string, [][]byte) error { return nil }
func (discardSink) close() error                 { return nil }

// fileSink appends each record behind its own u32 length — the framing every
// SDN stream reader already strips — into a plain file on the same filesystem
// the store is writing. It is deliberately the most OPTIMISTIC floor
// available: no index row, no dedupe lookup, no per-producer table, no engine
// mirror. Anything the store gives up against it is the price of being a
// queryable record store rather than a log.
//
// TWO FLOORS, NOT ONE, because "wire speed minus back pressure latency" names
// both halves and they are an order of magnitude apart:
//
//	syncEvery=false — buffered sequential append, one fsync at close. This is
//	  WIRE SPEED: what the disk moves when nothing waits for it.
//	syncEvery=true  — fsync per batch, the store's own durability cadence (one
//	  control transaction per storeBatchChunk). The gap between the two IS the
//	  back-pressure latency, isolated from anything the store does.
type fileSink struct {
	f         *os.File
	w         *bufio.Writer
	header    [4]byte
	syncEvery bool
}

func newFileSink(path string, syncEvery bool) (*fileSink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &fileSink{f: f, w: bufio.NewWriterSize(f, 1<<20), syncEvery: syncEvery}, nil
}

func (s *fileSink) write(_ string, records [][]byte) error {
	for _, rec := range records {
		binary.LittleEndian.PutUint32(s.header[:], uint32(len(rec)))
		if _, err := s.w.Write(s.header[:]); err != nil {
			return err
		}
		if _, err := s.w.Write(rec); err != nil {
			return err
		}
	}
	if !s.syncEvery {
		return nil
	}
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.f.Sync()
}

// close flushes and fsyncs, so even the unsynced pass pays for durability
// once: a wire-speed number that left the last gigabyte in the page cache
// would not be a disk measurement at all.
func (s *fileSink) close() error {
	if err := s.w.Flush(); err != nil {
		s.f.Close()
		return err
	}
	if err := s.f.Sync(); err != nil {
		s.f.Close()
		return err
	}
	return s.f.Close()
}

// storeSink drives the real write path.
type storeSink struct {
	store    *storage.FlatSQLStore
	producer string
	single   bool // -batch 1: the per-record Store() path instead of StoreBatch
}

func (s *storeSink) write(schema string, records [][]byte) error {
	if s.single {
		for _, rec := range records {
			if _, err := s.store.Store(schema, rec, s.producer, nil); err != nil {
				return fmt.Errorf("store %s record: %w", schema, err)
			}
		}
		return nil
	}
	inserted, err := s.store.StoreBatch(schema, records, s.producer, nil)
	if err != nil {
		return fmt.Errorf("store %s batch: %w", schema, err)
	}
	// A short insert means records collided on their CID and took the dedupe
	// path instead of the write path — the measurement would then be of
	// something other than what it claims. Fail rather than report it. Store()
	// above cannot be checked the same way (it answers the CID either way),
	// which is why uniqueness is also asserted at synthesis time.
	if inserted != len(records) {
		return fmt.Errorf("store %s batch inserted %d of %d records: synthesised records are not unique", schema, inserted, len(records))
	}
	return nil
}

func (s *storeSink) close() error { return nil }
