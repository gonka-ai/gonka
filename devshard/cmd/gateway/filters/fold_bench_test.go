package filters

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkBodyFolderProductionStream(b *testing.B) {
	events := productionStream(64)
	streamBytes := 0
	for _, event := range events {
		streamBytes += len(event)
	}
	b.ReportAllocs()
	b.SetBytes(int64(streamBytes))
	for b.Loop() {
		folder := NewBodyFolder(LogprobIntent{})
		for _, event := range events {
			if _, err := folder.Write(event); err != nil {
				b.Fatal(err)
			}
		}
		if len(folder.Body()) == 0 {
			b.Fatal("empty body")
		}
	}
}

func BenchmarkBodyFolderPlainContentStream(b *testing.B) {
	events := plainContentStream(512)
	b.ReportAllocs()
	for b.Loop() {
		folder := NewBodyFolder(LogprobIntent{})
		for _, event := range events {
			if _, err := folder.Write(event); err != nil {
				b.Fatal(err)
			}
		}
		if len(folder.Body()) == 0 {
			b.Fatal("empty body")
		}
	}
}

func BenchmarkBodyFolderTestdataStreams(b *testing.B) {
	entries, err := os.ReadDir(filepath.Join("testdata", "sse"))
	if err != nil {
		b.Fatal(err)
	}
	for _, entry := range entries {
		stream, err := os.ReadFile(filepath.Join("testdata", "sse", entry.Name()))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(entry.Name(), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(stream)))
			for b.Loop() {
				folder := NewBodyFolder(LogprobIntent{})
				if _, err := folder.Write(stream); err != nil {
					b.Fatal(err)
				}
				_ = folder.Body()
			}
		})
	}
}
