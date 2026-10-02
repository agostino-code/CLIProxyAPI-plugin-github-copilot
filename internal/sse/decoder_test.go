package sse

import (
	"bytes"
	"reflect"
	"testing"
)

func TestDecoderFramesSplitChunks(t *testing.T) {
	t.Parallel()

	var decoder Decoder
	if got := decoder.Feed([]byte("event: one\ndata: {\"a\":")); got != nil {
		t.Fatalf("unexpected incomplete frames: %#v", got)
	}
	got := decoder.Feed([]byte("1}\n\nevent: two\r\ndata: {}\r\n\r\ntrailing"))
	want := [][]byte{
		[]byte("event: one\ndata: {\"a\":1}\n\n"),
		[]byte("event: two\r\ndata: {}\r\n\r\n"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = %#v, want %#v", got, want)
	}
	if trailing := string(decoder.Flush()); trailing != "trailing" {
		t.Fatalf("trailing data = %q", trailing)
	}
}

func FuzzDecoder(f *testing.F) {
	f.Add([]byte("data: a\r\n\r\ndata: b\n\n"), uint8(3))
	f.Fuzz(func(t *testing.T, data []byte, width uint8) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		step := int(width) + 1
		d := &Decoder{}
		var got []byte
		for i := 0; i < len(data); i += step {
			end := min(i+step, len(data))
			for _, frame := range d.Feed(data[i:end]) {
				got = append(got, frame...)
			}
		}
		got = append(got, d.Flush()...)
		// Whitespace-only suffixes are intentionally dropped by Flush.
		if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(data)) {
			t.Fatal("decoder changed payload bytes")
		}
	})
}
