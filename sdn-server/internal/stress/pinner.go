//go:build stress
// +build stress

package stress

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StreamingPinTracker tracks pinned CIDs using file-backed storage
// to avoid memory issues with large volumes (10GB = millions of CIDs).
// This implements a streaming approach similar to IPFS's --stream flag.
type StreamingPinTracker struct {
	indexFile  *os.File
	writer     *bufio.Writer
	cidCount   int64
	totalBytes int64
	mu         sync.RWMutex
}

// NewStreamingPinTracker creates a new file-backed pin tracker.
func NewStreamingPinTracker(indexPath string) (*StreamingPinTracker, error) {
	f, err := os.Create(indexPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create index file: %w", err)
	}

	return &StreamingPinTracker{
		indexFile: f,
		writer:    bufio.NewWriterSize(f, 64*1024), // 64KB buffer
	}, nil
}

// RecordPin records a pinned CID without holding it in memory.
// Each CID is written to the index file immediately.
func (t *StreamingPinTracker) RecordPin(cid string, size int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Write CID and size to file (one per line)
	if _, err := fmt.Fprintf(t.writer, "%s,%d\n", cid, size); err != nil {
		return fmt.Errorf("failed to write pin record: %w", err)
	}

	t.cidCount++
	t.totalBytes += size

	// Flush periodically to avoid losing data
	if t.cidCount%10000 == 0 {
		if err := t.writer.Flush(); err != nil {
			return fmt.Errorf("failed to flush: %w", err)
		}
	}

	return nil
}

// Flush ensures all buffered data is written to disk.
func (t *StreamingPinTracker) Flush() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writer.Flush()
}

// StreamCIDs returns a channel that streams all pinned CIDs.
// This allows iterating over millions of CIDs without loading them all into memory.
func (t *StreamingPinTracker) StreamCIDs(ctx context.Context) (<-chan string, error) {
	// Flush any buffered writes first
	t.mu.Lock()
	if err := t.writer.Flush(); err != nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("failed to flush before streaming: %w", err)
	}
	t.mu.Unlock()

	// Reopen file for reading
	f, err := os.Open(t.indexFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to open index for reading: %w", err)
	}

	ch := make(chan string, 1000)

	go func() {
		defer close(ch)
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			// Extract CID (before comma)
			if idx := strings.Index(line, ","); idx > 0 {
				cid := line[:idx]
				select {
				case ch <- cid:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// StreamRecords returns a channel that streams all pin records with their sizes.
func (t *StreamingPinTracker) StreamRecords(ctx context.Context) (<-chan GeneratedRecord, error) {
	// Flush any buffered writes first
	t.mu.Lock()
	if err := t.writer.Flush(); err != nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("failed to flush before streaming: %w", err)
	}
	t.mu.Unlock()

	// Reopen file for reading
	f, err := os.Open(t.indexFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to open index for reading: %w", err)
	}

	ch := make(chan GeneratedRecord, 1000)

	go func() {
		defer close(ch)
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.SplitN(line, ",", 2)
			if len(parts) == 2 {
				size, _ := strconv.ParseInt(parts[1], 10, 64)
				record := GeneratedRecord{
					CID:  parts[0],
					Size: size,
				}
				select {
				case ch <- record:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// Stats returns the current tracking statistics.
func (t *StreamingPinTracker) Stats() (count int64, bytes int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cidCount, t.totalBytes
}

// Close flushes and closes the tracker.
func (t *StreamingPinTracker) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.writer.Flush(); err != nil {
		return err
	}
	return t.indexFile.Close()
}

// IndexPath returns the path to the index file.
func (t *StreamingPinTracker) IndexPath() string {
	return t.indexFile.Name()
}

// StressPinner implements a mock pinner interface for stress testing.
// It uses the StreamingPinTracker for memory-efficient CID tracking.
type StressPinner struct {
	tracker *StreamingPinTracker
}

// NewStressPinner creates a new stress test pinner.
func NewStressPinner(indexPath string) (*StressPinner, error) {
	tracker, err := NewStreamingPinTracker(indexPath)
	if err != nil {
		return nil, err
	}
	return &StressPinner{tracker: tracker}, nil
}

// Pin records a CID as pinned (mock implementation for stress tests).
func (p *StressPinner) Pin(ctx context.Context, cid string, size int64, ttl time.Duration) error {
	return p.tracker.RecordPin(cid, size)
}

// Tracker returns the underlying StreamingPinTracker.
func (p *StressPinner) Tracker() *StreamingPinTracker {
	return p.tracker
}

// Close closes the pinner and its tracker.
func (p *StressPinner) Close() error {
	return p.tracker.Close()
}
