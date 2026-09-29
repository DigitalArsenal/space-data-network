package format2

import (
	"encoding/binary"
	"fmt"
)

// WriterLayout is the engine's FlatsqlPsLayout (flatsql_ps.h): where a ring
// descriptor keeps each field, the slab pool, and the writer doorbells.
type WriterLayout struct {
	Version, NWriters, RingDescSize                              uint32
	OffTail, OffHead, OffAckedRseq, OffAckGen, OffMapGen         uint32
	OffWantPage, OffProdBusy, OffReclaim, OffProdWaiting         uint32
	OffState, OffOwnerWord, OffHandoffTo                         uint32
	OffRejectHead, OffRejectTail, OffRejects, OffPages           uint32
	OffCap, OffMaxEntry, OffNSlots, OffSlabBytes, OffNextRseq    uint32
	OffMappedPages, PoolBase, SlabBytes, EntryHeaderSize         uint32
	WriterSeq, WriterSleeping                                    []uint32
}

// ParseWriterLayout decodes a flatsql_ps_layout block.
func ParseWriterLayout(b []byte) (WriterLayout, error) {
	if len(b) < 624 {
		return WriterLayout{}, fmt.Errorf("format2: writer layout is %d bytes, want 624", len(b))
	}
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(b[4*i:]) }
	l := WriterLayout{
		Version: u(0), NWriters: u(1), RingDescSize: u(2),
		OffTail: u(3), OffHead: u(4), OffAckedRseq: u(5), OffAckGen: u(6), OffMapGen: u(7),
		OffWantPage: u(8), OffProdBusy: u(9), OffReclaim: u(10), OffProdWaiting: u(11),
		OffState: u(12), OffOwnerWord: u(13), OffHandoffTo: u(14),
		OffRejectHead: u(15), OffRejectTail: u(16), OffRejects: u(17), OffPages: u(18),
		OffCap: u(19), OffMaxEntry: u(20), OffNSlots: u(21), OffSlabBytes: u(22), OffNextRseq: u(23),
		OffMappedPages: u(24), PoolBase: u(25), SlabBytes: u(26), EntryHeaderSize: u(27),
	}
	if l.Version != 1 {
		return l, fmt.Errorf("format2: writer layout version %d, want 1", l.Version)
	}
	if l.NWriters < 1 || l.NWriters > 64 || l.SlabBytes == 0 || l.EntryHeaderSize != entryHeaderBytes {
		return l, fmt.Errorf("format2: writer layout out of range (writers %d, slab %d, entry header %d)",
			l.NWriters, l.SlabBytes, l.EntryHeaderSize)
	}
	for i := 0; i < int(l.NWriters); i++ {
		l.WriterSeq = append(l.WriterSeq, u(28+i))
		l.WriterSleeping = append(l.WriterSleeping, u(92+i))
	}
	return l, nil
}

// ReaderLayout is the engine's FlatsqlPsReaderLayout: the mailbox.
type ReaderLayout struct {
	Version, NLanes, NSlots, SlotBase, SlotStride, HeaderSize, ReqBytes, RingBytes uint32
	OffState, OffCancel, OffOutSeq, OffSpaceSeq, OffFlags, OffLane, OffReqID       uint32
	OffSQLLen, OffParamsLen, OffReqCap, OffRingCap                                  uint32
	OffMaxRowsExamined, OffMaxBytesRead, OffMaxResultRows, OffMaxResultBytes        uint32
	OffRingHead, OffRingTail, OffStatus, OffErrLen, OffRowsOut, OffRowsExamined    uint32
	OffBytesRead, OffIndexEntries, OffFenceReads, OffSubmitNs, OffStartNs, OffEndNs uint32
	OffErr                                                                          uint32
	QueueCells, QueueMask, QueueEnq, QueueDeq, StopWord                            uint32
	LaneDoorbell, LaneState, LaneAnnounce                                           []uint32
}

// ParseReaderLayout decodes a flatsql_ps_reader_layout block.
func ParseReaderLayout(b []byte) (ReaderLayout, error) {
	if len(b) < 932 {
		return ReaderLayout{}, fmt.Errorf("format2: reader layout is %d bytes, want 932", len(b))
	}
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(b[4*i:]) }
	l := ReaderLayout{
		Version: u(0), NLanes: u(1), NSlots: u(2), SlotBase: u(3), SlotStride: u(4), HeaderSize: u(5),
		ReqBytes: u(6), RingBytes: u(7),
		OffState: u(8), OffCancel: u(9), OffOutSeq: u(10), OffSpaceSeq: u(11), OffFlags: u(12), OffLane: u(13),
		OffReqID: u(14), OffSQLLen: u(15), OffParamsLen: u(16), OffReqCap: u(17), OffRingCap: u(18),
		OffMaxRowsExamined: u(19), OffMaxBytesRead: u(20), OffMaxResultRows: u(21), OffMaxResultBytes: u(22),
		OffRingHead: u(23), OffRingTail: u(24), OffStatus: u(25), OffErrLen: u(26), OffRowsOut: u(27),
		OffRowsExamined: u(28), OffBytesRead: u(29), OffIndexEntries: u(30), OffFenceReads: u(31),
		OffSubmitNs: u(32), OffStartNs: u(33), OffEndNs: u(34), OffErr: u(35),
		QueueCells: u(36), QueueMask: u(37), QueueEnq: u(38), QueueDeq: u(39), StopWord: u(40),
	}
	if l.Version != 1 {
		return l, fmt.Errorf("format2: reader layout version %d, want 1", l.Version)
	}
	if l.NLanes < 1 || l.NLanes > 64 || l.NSlots == 0 || l.SlotStride < l.HeaderSize+l.ReqBytes+l.RingBytes {
		return l, fmt.Errorf("format2: reader layout out of range (lanes %d, slots %d)", l.NLanes, l.NSlots)
	}
	for i := 0; i < int(l.NLanes); i++ {
		l.LaneDoorbell = append(l.LaneDoorbell, u(41+i))
		l.LaneState = append(l.LaneState, u(105+i))
		l.LaneAnnounce = append(l.LaneAnnounce, u(169+i))
	}
	return l, nil
}
