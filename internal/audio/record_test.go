package audio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordWavProducesFile(t *testing.T) {
	if os.Getenv("DEVIN_SPEECH_TEST_AUDIO") == "" {
		t.Skip("no audio source in test env")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	path, err := RecordWav(ctx, "", 0)
	if err != nil {
		t.Fatalf("RecordWav: %v", err)
	}
	defer os.Remove(path)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() < 100 {
		t.Fatalf("wav too small: %d bytes", st.Size())
	}
	f, _ := os.Open(path)
	defer f.Close()
	hdr := make([]byte, 4)
	f.Read(hdr)
	if string(hdr) != "RIFF" {
		t.Fatalf("not a wav: %q", hdr)
	}
}

func TestTmpWavPathUnique(t *testing.T) {
	dir := t.TempDir()
	p1 := wavPathIn(dir)
	p2 := wavPathIn(dir)
	if p1 == p2 {
		t.Fatal("paths not unique")
	}
	_ = filepath.Separator
}
