package format4proof

import (
	"encoding/json"
	"fmt"
	"os"
)

// Benchset is benchset.json (the design step's frozen operation set): the
// reads R01–R24, the writes W01–W10, the mixed M01–M02 and the growth plan.
type Benchset struct {
	Version  int                     `json:"version"`
	Fixtures map[string]BenchFixture `json:"fixtures"`
	Reads    []BenchOp               `json:"reads"`
	Writes   []BenchOp               `json:"writes"`
	Mixed    []BenchOp               `json:"mixed"`
	Growth   json.RawMessage         `json:"growth"`
}

// BenchFixture is a fixture's size (the bytes-per-record denominators).
type BenchFixture struct {
	UniqueRecords int64 `json:"unique_records"`
	RecordCopies  int64 `json:"record_copies"`
}

// BenchOp is one operation; Params differs per operation and is decoded by
// the shape builders.
type BenchOp struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Fn     string          `json:"fn"`
	Params json.RawMessage `json:"params"`
	After  string          `json:"after,omitempty"`
}

// LoadBenchset reads a benchset file.
func LoadBenchset(path string) (*Benchset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bs Benchset
	if err := json.Unmarshal(b, &bs); err != nil {
		return nil, fmt.Errorf("format4proof: benchset %s: %w", path, err)
	}
	if len(bs.Reads) == 0 {
		return nil, fmt.Errorf("format4proof: benchset %s has no reads", path)
	}
	return &bs, nil
}

// Read returns the read with the given id.
func (b *Benchset) Read(id string) (BenchOp, bool) {
	for _, r := range b.Reads {
		if r.ID == id {
			return r, true
		}
	}
	return BenchOp{}, false
}

// Write returns the write with the given id.
func (b *Benchset) Write(id string) (BenchOp, bool) {
	for _, r := range b.Writes {
		if r.ID == id {
			return r, true
		}
	}
	return BenchOp{}, false
}

// GrowthStep is one step of the benchset's growth plan.
type GrowthStep struct {
	ID         string `json:"id"`
	Records    int64  `json:"records"`
	Partitions int    `json:"partitions"`
	Payload    string `json:"payload,omitempty"`
}

// GrowthSteps decodes the growth plan's steps.
func (b *Benchset) GrowthSteps() ([]GrowthStep, error) {
	var g struct {
		Steps []GrowthStep `json:"steps"`
	}
	if len(b.Growth) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(b.Growth, &g); err != nil {
		return nil, err
	}
	return g.Steps, nil
}
