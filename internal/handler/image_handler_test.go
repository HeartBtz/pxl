package handler

import "testing"

func TestParseSingleRange(t *testing.T) {
	tests := []struct {
		name           string
		header         string
		size           int64
		offset, length int64
		partial        bool
		wantErr        bool
	}{
		{name: "full", size: 100, offset: 0, length: 100},
		{name: "bounded", header: "bytes=10-19", size: 100, offset: 10, length: 10, partial: true},
		{name: "open ended", header: "bytes=90-", size: 100, offset: 90, length: 10, partial: true},
		{name: "suffix", header: "bytes=-25", size: 100, offset: 75, length: 25, partial: true},
		{name: "suffix clamped", header: "bytes=-200", size: 100, offset: 0, length: 100, partial: true},
		{name: "end clamped", header: "bytes=90-200", size: 100, offset: 90, length: 10, partial: true},
		{name: "past end", header: "bytes=100-", size: 100, wantErr: true},
		{name: "backwards", header: "bytes=20-10", size: 100, wantErr: true},
		{name: "multi range", header: "bytes=0-1,4-5", size: 100, wantErr: true},
		{name: "wrong unit", header: "items=0-1", size: 100, wantErr: true},
		{name: "empty image range", header: "bytes=0-", size: 0, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, length, partial, err := parseSingleRange(tt.header, tt.size)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSingleRange() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (offset != tt.offset || length != tt.length || partial != tt.partial) {
				t.Fatalf("parseSingleRange() = (%d,%d,%v), want (%d,%d,%v)", offset, length, partial, tt.offset, tt.length, tt.partial)
			}
		})
	}
}

func TestETagMatches(t *testing.T) {
	const etag = `"abc"`
	for _, header := range []string{`"abc"`, `W/"abc"`, `"other", "abc"`, `*`} {
		if !etagMatches(header, etag) {
			t.Errorf("etagMatches(%q) = false", header)
		}
	}
	if etagMatches(`"other"`, etag) {
		t.Error("unexpected ETag match")
	}
}
