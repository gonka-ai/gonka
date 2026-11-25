package spool

import (
	"io"
	"runtime"
	"testing"
)

// One aggregated reply: a few hundred kilobytes arriving as SSE-event-sized chunks.
const (
	benchChunkBytes = 384
	benchBodyBytes  = 256 << 10
	benchChunkCount = benchBodyBytes / benchChunkBytes
	benchWriteBuf   = 64 << 10
)

func benchChunk() []byte {
	chunk := make([]byte, benchChunkBytes)
	for i := range chunk {
		chunk[i] = byte('a' + i%26)
	}
	return chunk
}

func benchDir(tb testing.TB) *Dir {
	tb.Helper()
	dir, err := Open(Config{
		Path:         tb.TempDir(),
		Prefix:       "bench-",
		MaxFiles:     64,
		MaxFileBytes: 8 << 20,
		WriteBuffer:  benchWriteBuf,
	})
	if err != nil {
		tb.Fatalf("Open() = %v", err)
	}
	return dir
}

func BenchmarkBufferWriteMemory(b *testing.B) {
	chunk := benchChunk()

	b.ReportAllocs()
	b.SetBytes(benchChunkCount * benchChunkBytes)
	for b.Loop() {
		buffer := NewBuffer(BufferConfig{Budget: NewBudget(2<<20, 2<<20)})
		for range benchChunkCount {
			if _, err := buffer.Write(chunk); err != nil {
				b.Fatalf("Write() = %v", err)
			}
		}
		if err := buffer.Close(); err != nil {
			b.Fatalf("Close() = %v", err)
		}
	}
}

// The gateway re-reads the spill status after every write, so that is the shape of one SSE event.
func BenchmarkBufferWriteMemoryWithStatusReads(b *testing.B) {
	chunk := benchChunk()

	b.ReportAllocs()
	b.SetBytes(benchChunkCount * benchChunkBytes)
	for b.Loop() {
		buffer := NewBuffer(BufferConfig{Budget: NewBudget(2<<20, 2<<20)})
		for range benchChunkCount {
			if _, err := buffer.Write(chunk); err != nil {
				b.Fatalf("Write() = %v", err)
			}
			_, _, _ = buffer.DiskLimit(), buffer.SpillDisabled(), buffer.HoldsDegradedSlot()
		}
		if err := buffer.Close(); err != nil {
			b.Fatalf("Close() = %v", err)
		}
	}
}

func BenchmarkBufferWriteSpilled(b *testing.B) {
	dir := benchDir(b)
	chunk := benchChunk()

	b.ReportAllocs()
	b.SetBytes(benchChunkCount * benchChunkBytes)
	for b.Loop() {
		buffer := NewBuffer(BufferConfig{Dir: dir, Budget: NewBudget(64<<10, 8<<20)})
		for range benchChunkCount {
			if _, err := buffer.Write(chunk); err != nil {
				b.Fatalf("Write() = %v", err)
			}
		}
		if err := buffer.Close(); err != nil {
			b.Fatalf("Close() = %v", err)
		}
	}
}

// Spilling exists to give the RAM back, so what a spilled buffer still holds is the number that matters.
func BenchmarkBufferSpilledResident(b *testing.B) {
	const liveBuffers = 16
	dir := benchDir(b)
	chunk := benchChunk()
	resident := float64(0)

	for b.Loop() {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		live := make([]*Buffer, liveBuffers)
		for index := range live {
			live[index] = NewBuffer(BufferConfig{Dir: dir, Budget: NewBudget(64<<10, 8<<20)})
			for range benchChunkCount {
				if _, err := live[index].Write(chunk); err != nil {
					b.Fatalf("Write() = %v", err)
				}
			}
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		resident = (float64(after.HeapAlloc) - float64(before.HeapAlloc)) / liveBuffers

		for _, buffer := range live {
			if err := buffer.Close(); err != nil {
				b.Fatalf("Close() = %v", err)
			}
		}
	}
	b.ReportMetric(resident, "resident-B/buffer")
}

func BenchmarkBufferBytesSpilled(b *testing.B) {
	dir := benchDir(b)
	chunk := benchChunk()
	buffer := NewBuffer(BufferConfig{Dir: dir, Budget: NewBudget(64<<10, 8<<20)})
	for range benchChunkCount {
		if _, err := buffer.Write(chunk); err != nil {
			b.Fatalf("Write() = %v", err)
		}
	}
	b.Cleanup(func() { _ = buffer.Close() })

	b.ReportAllocs()
	b.SetBytes(benchChunkCount * benchChunkBytes)
	for b.Loop() {
		body, err := buffer.Bytes()
		if err != nil {
			b.Fatalf("Bytes() = %v", err)
		}
		if len(body) != benchChunkCount*benchChunkBytes {
			b.Fatalf("Bytes() length = %d, want %d", len(body), benchChunkCount*benchChunkBytes)
		}
	}
}

// The logprob store writes one NDJSON line per entry, so File.Write is a small-write path.
func BenchmarkFileWriteLines(b *testing.B) {
	dir := benchDir(b)
	line := benchChunk()

	b.ReportAllocs()
	b.SetBytes(benchChunkCount * benchChunkBytes)
	for b.Loop() {
		file, err := dir.Create()
		if err != nil {
			b.Fatalf("Create() = %v", err)
		}
		for range benchChunkCount {
			if _, err := file.Write(line); err != nil {
				b.Fatalf("Write() = %v", err)
			}
		}
		if err := file.Close(); err != nil {
			b.Fatalf("Close() = %v", err)
		}
	}
}

func BenchmarkFileReadBack(b *testing.B) {
	dir := benchDir(b)
	file, err := dir.Create()
	if err != nil {
		b.Fatalf("Create() = %v", err)
	}
	b.Cleanup(func() { _ = file.Close() })
	chunk := benchChunk()
	for range benchChunkCount {
		if _, err := file.Write(chunk); err != nil {
			b.Fatalf("Write() = %v", err)
		}
	}

	b.ReportAllocs()
	b.SetBytes(benchChunkCount * benchChunkBytes)
	for b.Loop() {
		reader, err := file.Reader()
		if err != nil {
			b.Fatalf("Reader() = %v", err)
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			b.Fatalf("Copy() = %v", err)
		}
	}
}

func BenchmarkIndexAppend(b *testing.B) {
	dir := benchDir(b)
	index, err := dir.CreateIndex()
	if err != nil {
		b.Fatalf("CreateIndex() = %v", err)
	}
	b.Cleanup(func() { _ = index.Close() })
	offsets := make([]int64, 8)
	for i := range offsets {
		offsets[i] = int64(i) * 4096
	}

	b.ReportAllocs()
	for b.Loop() {
		if err := index.Append(offsets); err != nil {
			b.Fatalf("Append() = %v", err)
		}
	}
}

func BenchmarkDirCreate(b *testing.B) {
	dir := benchDir(b)

	b.ReportAllocs()
	for b.Loop() {
		file, err := dir.Create()
		if err != nil {
			b.Fatalf("Create() = %v", err)
		}
		if err := file.Close(); err != nil {
			b.Fatalf("Close() = %v", err)
		}
	}
}
