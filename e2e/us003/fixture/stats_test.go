package stats

import "testing"

func TestMean(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{4}, 4},
		{[]float64{1, 2, 3, 4}, 2.5},
	} {
		if got := Mean(tc.in); got != tc.want {
			t.Errorf("Mean(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestMedian(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{7}, 7},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{10, 2, 38, 23, 38, 23}, 23},
	} {
		in := append([]float64(nil), tc.in...)
		if got := Median(tc.in); got != tc.want {
			t.Errorf("Median(%v) = %v, want %v", in, got, tc.want)
		}
		for i := range in {
			if tc.in[i] != in[i] {
				t.Fatalf("Median modified its input: %v, was %v", tc.in, in)
			}
		}
	}
}
