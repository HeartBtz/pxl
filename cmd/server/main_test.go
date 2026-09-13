package main

import (
	"math"
	"testing"
)

func TestUploadRequestLimit(t *testing.T) {
	if got, want := uploadRequestLimit(50<<20, 10), int64(501<<20); got != want {
		t.Fatalf("uploadRequestLimit() = %d, want %d", got, want)
	}
	if got := uploadRequestLimit(math.MaxInt64, 2); got != math.MaxInt64 {
		t.Fatalf("overflow was not clamped: %d", got)
	}
}
