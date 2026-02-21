package eval

import (
	"math"
	"sort"
)

// MetricsSet holds standard IR evaluation metrics for a single query or
// averaged across a dataset.
type MetricsSet struct {
	Recall5  float64 `json:"recall@5"`
	Recall10 float64 `json:"recall@10"`
	NDCG10   float64 `json:"ndcg@10"`
	MRR10    float64 `json:"mrr@10"`
}

// RecallAtK computes the fraction of relevant documents found in the top-k
// ranked results. Relevance values > 0 are treated as relevant.
func RecallAtK(ranked []string, relevant map[string]int, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	totalRelevant := 0
	for _, rel := range relevant {
		if rel > 0 {
			totalRelevant++
		}
	}
	if totalRelevant == 0 {
		return 0
	}
	limit := k
	if limit > len(ranked) {
		limit = len(ranked)
	}
	found := 0
	for i := 0; i < limit; i++ {
		if rel, ok := relevant[ranked[i]]; ok && rel > 0 {
			found++
		}
	}
	return float64(found) / float64(totalRelevant)
}

// NDCGAtK computes Normalized Discounted Cumulative Gain at position k.
// Supports graded relevance (0, 1, 2, ...). Compatible with BEIR/trec_eval.
func NDCGAtK(ranked []string, relevant map[string]int, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	limit := k
	if limit > len(ranked) {
		limit = len(ranked)
	}

	// DCG
	dcg := 0.0
	for i := 0; i < limit; i++ {
		rel := 0
		if r, ok := relevant[ranked[i]]; ok {
			rel = r
		}
		if rel > 0 {
			dcg += (math.Pow(2, float64(rel)) - 1) / math.Log2(float64(i+2))
		}
	}

	// Ideal DCG: sort all relevance values descending
	idealRels := make([]int, 0, len(relevant))
	for _, r := range relevant {
		if r > 0 {
			idealRels = append(idealRels, r)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(idealRels)))
	idealLimit := k
	if idealLimit > len(idealRels) {
		idealLimit = len(idealRels)
	}
	idcg := 0.0
	for i := 0; i < idealLimit; i++ {
		idcg += (math.Pow(2, float64(idealRels[i])) - 1) / math.Log2(float64(i+2))
	}

	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// MRRAtK computes Mean Reciprocal Rank at position k.
// Returns the reciprocal of the rank of the first relevant document.
func MRRAtK(ranked []string, relevant map[string]int, k int) float64 {
	limit := k
	if limit > len(ranked) {
		limit = len(ranked)
	}
	for i := 0; i < limit; i++ {
		if rel, ok := relevant[ranked[i]]; ok && rel > 0 {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// ComputeAll calculates the standard MetricsSet for a single query.
func ComputeAll(ranked []string, relevant map[string]int) MetricsSet {
	return MetricsSet{
		Recall5:  RecallAtK(ranked, relevant, 5),
		Recall10: RecallAtK(ranked, relevant, 10),
		NDCG10:   NDCGAtK(ranked, relevant, 10),
		MRR10:    MRRAtK(ranked, relevant, 10),
	}
}

// AverageMetrics computes the mean of a slice of per-query MetricsSet values.
func AverageMetrics(sets []MetricsSet) MetricsSet {
	if len(sets) == 0 {
		return MetricsSet{}
	}
	var sum MetricsSet
	for _, m := range sets {
		sum.Recall5 += m.Recall5
		sum.Recall10 += m.Recall10
		sum.NDCG10 += m.NDCG10
		sum.MRR10 += m.MRR10
	}
	n := float64(len(sets))
	return MetricsSet{
		Recall5:  sum.Recall5 / n,
		Recall10: sum.Recall10 / n,
		NDCG10:   sum.NDCG10 / n,
		MRR10:    sum.MRR10 / n,
	}
}
