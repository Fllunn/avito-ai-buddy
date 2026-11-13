package main

import "testing"

func TestThreshold(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts counts
		want   bool
	}{
		{"exactly eighty", counts{4, 5}, false},
		{"above eighty", counts{81, 100}, true},
		{"below eighty", counts{79, 100}, false},
		{"empty", counts{0, 0}, false},
		{"invalid", counts{6, 5}, false},
		{"large exact threshold", counts{800_000_000_000_000_000, 1_000_000_000_000_000_000}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.counts.passes(); got != tc.want {
				t.Fatalf("passes() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAggregateUsesLineCounts(t *testing.T) {
	got, err := aggregate(map[string]counts{"a": {9, 10}, "b": {1, 2}})
	if err != nil || got != (counts{10, 12}) {
		t.Fatalf("aggregate() = %+v, %v", got, err)
	}
	if _, err := aggregate(map[string]counts{"empty": {0, 0}}); err == nil {
		t.Fatal("empty module must not silently pass")
	}
}
