//go:build stress
// +build stress

package stress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const (
	// DefaultTargetSize is 10GB
	DefaultTargetSize = 10 * 1024 * 1024 * 1024
	// BatchSize is the number of records per batch
	BatchSize = 10000
	// WorkerCount is the number of parallel generators
	WorkerCount = 8
)

// GeneratedRecord holds a generated FlatBuffer with its computed CID.
type GeneratedRecord struct {
	Data []byte
	CID  string
	Size int64
}

// BatchResult holds a batch of generated FlatBuffers.
type BatchResult struct {
	Records []GeneratedRecord
	Size    int64
	Err     error
}

// Generator creates FlatBuffer records in parallel batches.
type Generator struct {
	totalSize   int64
	recordCount int64
}

// NewGenerator creates a new batch generator.
func NewGenerator() *Generator {
	return &Generator{}
}

// GenerateBatches yields batches until target size is reached.
// Results are sent to the returned channel. The channel is closed when done.
func (g *Generator) GenerateBatches(ctx context.Context, targetBytes int64) <-chan BatchResult {
	results := make(chan BatchResult, WorkerCount)

	go func() {
		defer close(results)

		var wg sync.WaitGroup
		sem := make(chan struct{}, WorkerCount)
		var batchNum int64

		for atomic.LoadInt64(&g.totalSize) < targetBytes {
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}

			currentBatch := atomic.AddInt64(&batchNum, 1) - 1
			wg.Add(1)

			go func(bn int64) {
				defer wg.Done()
				defer func() { <-sem }()

				batch := g.generateBatch(bn)

				select {
				case results <- batch:
				case <-ctx.Done():
					return
				}
			}(currentBatch)

			// Check if we've generated enough
			if atomic.LoadInt64(&g.totalSize) >= targetBytes {
				break
			}
		}

		wg.Wait()
	}()

	return results
}

func (g *Generator) generateBatch(batchNum int64) BatchResult {
	records := make([]GeneratedRecord, 0, BatchSize)
	var batchSize int64

	// Create a new builder for this goroutine (builders are not thread-safe)
	builder := sds.NewOMMBuilder()

	for i := 0; i < BatchSize; i++ {
		noradID := uint32(batchNum*BatchSize + int64(i) + 1)

		data := builder.
			WithObjectName(fmt.Sprintf("STRESS-SAT-%d", noradID)).
			WithObjectID(fmt.Sprintf("2024-%05dA", noradID%100000)).
			WithNoradCatID(noradID).
			WithMeanMotion(14.5 + float64(i%1000)/10000).
			WithEccentricity(0.0001 + float64(i%100)/1000000).
			WithInclination(float64(30 + i%60)).
			WithRaOfAscNode(float64(i % 360)).
			WithArgOfPericenter(float64((i * 7) % 360)).
			WithMeanAnomaly(float64((i * 13) % 360)).
			Build()

		cid := computeCID(data)
		records = append(records, GeneratedRecord{
			Data: data,
			CID:  cid,
			Size: int64(len(data)),
		})
		batchSize += int64(len(data))
	}

	// Update totals atomically
	atomic.AddInt64(&g.totalSize, batchSize)
	atomic.AddInt64(&g.recordCount, int64(len(records)))

	return BatchResult{Records: records, Size: batchSize}
}

// Stats returns the current generation statistics.
func (g *Generator) Stats() (totalSize, recordCount int64) {
	return atomic.LoadInt64(&g.totalSize), atomic.LoadInt64(&g.recordCount)
}

// computeCID computes the CID (content identifier) for data using SHA-256.
// This matches the implementation in storage/flatsql.go.
func computeCID(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
