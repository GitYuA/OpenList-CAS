package _189pc

import (
	"testing"
	"time"
)

func TestTimeUnmarshalNarrowNoBreakSpace(t *testing.T) {
	var got Time
	if err := got.UnmarshalJSON([]byte(`"Jan 2, 2026 11:04:05 AM"`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Time(got).IsZero() {
		t.Fatal("expected parsed time, got zero time")
	}
}