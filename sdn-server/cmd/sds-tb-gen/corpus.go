package main

// corpus.go: the seed corpus (TBC2, written by flatsql's
// `flatsql_ps_test --test=tb_corpus_export`) and the clone mutation the flatsql
// engine tier uses (flatsql cpp/test/ps/tb_corpus.cpp mutateClone): clone c
// of a record shifts its epoch strings and epoch doubles by 61 s per clone,
// adds c mod 97 to its other integers and scales its other doubles by at most
// 1e-6. A record with an epoch keeps its identity (the same object later);
// one without adds 100003 per clone to its identity integers. Clone 0 is the
// seed. The patch sites come from the record's production schema.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

const (
	patchInt32 = iota
	patchInt64
	patchF64
	patchF32
	patchEpochStr
	patchEpochF64
	patchBytes8 = 0xff
)

type patch struct {
	off      uint32
	kind     uint8
	length   uint8
	identity bool
}

type seed struct {
	fid                                   string
	fb                                    []byte
	peer, provider, source, batch, supKey string
	patches                               []patch
}

type corpus struct {
	byFID map[string][]*seed
}

func loadCorpus(path string) (*corpus, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var hdr [16]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	if string(hdr[:4]) != "TBC2" {
		return nil, errors.New("not a TBC2 corpus (flatsql_ps_test --test=tb_corpus_export writes one)")
	}
	n := binary.LittleEndian.Uint64(hdr[8:])
	c := &corpus{byFID: map[string][]*seed{}}
	u16 := func() (uint16, error) {
		var b [2]byte
		_, err := io.ReadFull(r, b[:])
		return binary.LittleEndian.Uint16(b[:]), err
	}
	str := func() (string, error) {
		l, err := u16()
		if err != nil {
			return "", err
		}
		b := make([]byte, l)
		_, err = io.ReadFull(r, b)
		return string(b), err
	}
	for i := uint64(0); i < n; i++ {
		var h [8]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		s := &seed{fid: string(h[:4])}
		s.fb = make([]byte, binary.LittleEndian.Uint32(h[4:]))
		if _, err := io.ReadFull(r, s.fb); err != nil {
			return nil, err
		}
		for _, p := range []*string{&s.peer, &s.provider, &s.source, &s.batch, &s.supKey} {
			if *p, err = str(); err != nil {
				return nil, err
			}
		}
		np, err := u16()
		if err != nil {
			return nil, err
		}
		for j := 0; j < int(np); j++ {
			var b [8]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return nil, err
			}
			s.patches = append(s.patches, patch{off: binary.LittleEndian.Uint32(b[:]), kind: b[4], length: b[5], identity: b[6] != 0})
		}
		c.byFID[s.fid] = append(c.byFID[s.fid], s)
	}
	return c, nil
}

func mix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// shiftISO moves the "YYYY-MM-DDTHH:MM:SS" at p by sec seconds, in place
// (years 1000..9999, the separator kept).
func shiftISO(p []byte, sec int64) {
	if len(p) < 19 {
		return
	}
	sep := p[10]
	b := make([]byte, 19)
	copy(b, p[:19])
	b[10] = 'T'
	t, err := time.Parse("2006-01-02T15:04:05", string(b))
	if err != nil {
		return
	}
	t = t.Add(time.Duration(sec) * time.Second)
	if t.Year() < 1000 || t.Year() > 9999 {
		return
	}
	out := t.Format("2006-01-02T15:04:05")
	copy(p, out)
	p[10] = sep
}

// clone returns the bare FlatBuffer of generated record k of the seeds:
// seed k mod n, clone k / n. keepIdentity: on a record without an epoch the
// identity integers take clone (c - 1)'s values (a supersede of that clone).
func clone(seeds []*seed, k uint64, keepIdentity bool) (*seed, []byte) {
	s := seeds[k%uint64(len(seeds))]
	c := k / uint64(len(seeds))
	out := append([]byte(nil), s.fb...)
	if c == 0 {
		return s, out
	}
	// A record with an epoch is the same object at a later epoch: its identity
	// stays (object cardinality stays the corpus's). Without an epoch the
	// identity shifts, or keeps clone c-1's (a supersede of it).
	hasEpoch := false
	for _, p := range s.patches {
		if p.kind == patchEpochStr || p.kind == patchEpochF64 {
			hasEpoch = true
		}
	}
	idc := c
	switch {
	case hasEpoch:
		idc = 0
	case keepIdentity:
		idc = c - 1
	}
	le := binary.LittleEndian
	for _, p := range s.patches {
		at := out[p.off:]
		switch p.kind {
		case patchInt32:
			v := le.Uint32(at)
			if p.identity {
				v += uint32(idc * 100003)
			} else {
				v += uint32(c % 97)
			}
			le.PutUint32(at, v)
		case patchInt64:
			v := le.Uint64(at)
			if p.identity {
				v += idc * 100003
			} else {
				v += c % 97
			}
			le.PutUint64(at, v)
		case patchF64:
			v := math.Float64frombits(le.Uint64(at))
			v *= 1.0 + float64(int64(mix64(c)%2001)-1000)*1e-9
			le.PutUint64(at, math.Float64bits(v))
		case patchF32:
			v := math.Float32frombits(le.Uint32(at))
			v *= float32(1.0 + float64(int64(mix64(c)%2001)-1000)*1e-6)
			le.PutUint32(at, math.Float32bits(v))
		case patchEpochStr:
			shiftISO(at[:p.length], int64(c)*61)
		case patchEpochF64:
			v := math.Float64frombits(le.Uint64(at))
			v += float64(c) * 61.0
			le.PutUint64(at, math.Float64bits(v))
		case patchBytes8:
			le.PutUint64(at, c)
		}
	}
	return s, out
}
