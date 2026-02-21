package eval

import (
	"math"
	"testing"
)

func TestRecallAtK(t *testing.T) {
	relevant := map[string]int{"a": 1, "b": 1, "c": 1}

	tests := []struct {
		name   string
		ranked []string
		k      int
		want   float64
	}{
		{"all found", []string{"a", "b", "c"}, 3, 1.0},
		{"partial", []string{"a", "x", "y"}, 3, 1.0 / 3},
		{"k smaller", []string{"a", "b", "c"}, 2, 2.0 / 3},
		{"none found", []string{"x", "y", "z"}, 3, 0.0},
		{"empty ranked", []string{}, 3, 0.0},
		{"k larger than ranked", []string{"a"}, 10, 1.0 / 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RecallAtK(tt.ranked, relevant, tt.k)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("RecallAtK = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestRecallAtK_EmptyRelevant(t *testing.T) {
	got := RecallAtK([]string{"a"}, map[string]int{}, 5)
	if got != 0 {
		t.Errorf("expected 0 for empty relevant, got %f", got)
	}
}

func TestRecallAtK_ZeroRelevance(t *testing.T) {
	got := RecallAtK([]string{"a"}, map[string]int{"a": 0}, 5)
	if got != 0 {
		t.Errorf("expected 0 when all relevance=0, got %f", got)
	}
}

func TestNDCGAtK_BinaryRelevance(t *testing.T) {
	relevant := map[string]int{"a": 1, "b": 1}
	got := NDCGAtK([]string{"a", "b", "x"}, relevant, 3)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("perfect ranking nDCG = %f, want 1.0", got)
	}
	got = NDCGAtK([]string{"x", "y", "z"}, relevant, 3)
	if got != 0 {
		t.Errorf("no relevant nDCG = %f, want 0.0", got)
	}
}

func TestNDCGAtK_GradedRelevance(t *testing.T) {
	relevant := map[string]int{"a": 2, "b": 1}
	ideal := NDCGAtK([]string{"a", "b"}, relevant, 2)
	if math.Abs(ideal-1.0) > 1e-9 {
		t.Errorf("ideal order nDCG = %f, want 1.0", ideal)
	}
	reversed := NDCGAtK([]string{"b", "a"}, relevant, 2)
	if reversed >= 1.0 || reversed <= 0 {
		t.Errorf("reversed nDCG = %f, want 0 < x < 1", reversed)
	}
}

func TestMRRAtK(t *testing.T) {
	relevant := map[string]int{"a": 1, "b": 1}
	tests := []struct {
		name   string
		ranked []string
		k      int
		want   float64
	}{
		{"first position", []string{"a", "x"}, 10, 1.0},
		{"second position", []string{"x", "a"}, 10, 0.5},
		{"third position", []string{"x", "y", "b"}, 10, 1.0 / 3},
		{"not found in k", []string{"x", "y", "a"}, 2, 0.0},
		{"empty", []string{}, 10, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MRRAtK(tt.ranked, relevant, tt.k)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("MRRAtK = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestComputeAll(t *testing.T) {
	relevant := map[string]int{"a": 1, "b": 1}
	ranked := []string{"a", "x", "b", "y", "z", "w", "v", "u", "t", "s"}
	m := ComputeAll(ranked, relevant)
	if m.Recall5 != 1.0 {
		t.Errorf("Recall5 = %f, want 1.0", m.Recall5)
	}
	if m.MRR10 != 1.0 {
		t.Errorf("MRR10 = %f, want 1.0", m.MRR10)
	}
}

func TestAverageMetrics(t *testing.T) {
	sets := []MetricsSet{
		{Recall5: 0.5, Recall10: 1.0, NDCG10: 0.8, MRR10: 1.0},
		{Recall5: 0.3, Recall10: 0.6, NDCG10: 0.4, MRR10: 0.5},
	}
	avg := AverageMetrics(sets)
	if math.Abs(avg.Recall5-0.4) > 1e-9 {
		t.Errorf("Recall5 = %f, want 0.4", avg.Recall5)
	}
	if math.Abs(avg.MRR10-0.75) > 1e-9 {
		t.Errorf("MRR10 = %f, want 0.75", avg.MRR10)
	}
}

func TestAverageMetrics_Empty(t *testing.T) {
	avg := AverageMetrics(nil)
	if avg != (MetricsSet{}) {
		t.Errorf("expected zero MetricsSet for nil input, got %+v", avg)
	}
}
