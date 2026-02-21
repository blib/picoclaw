package eval

// Baseline holds published retrieval scores for comparison.
type Baseline struct {
	Name    string     `json:"name"`
	System  string     `json:"system"` // e.g. "BM25 (Pyserini)", "contriever"
	Metrics MetricsSet `json:"metrics"`
}

// Comparison holds a strategy result versus a baseline.
type Comparison struct {
	StrategyName string     `json:"strategy"`
	BaselineName string     `json:"baseline"`
	System       string     `json:"system"`
	Delta        MetricsSet `json:"delta"`
	Metrics      MetricsSet `json:"metrics"`
	BaseMet      MetricsSet `json:"baseline_metrics"`
}

// PublishedBaselines returns hardcoded BEIR BM25 baselines from the
// official BEIR paper (Thakur et al., 2021). These serve as reference
// points — values are nDCG@10 from the published results.
// Recall@5, Recall@10, MRR@10 are approximated from public reproductions.
func PublishedBaselines() map[string]Baseline {
	return map[string]Baseline{
		"scifact": {
			Name:   "scifact",
			System: "BM25 (BEIR)",
			Metrics: MetricsSet{
				Recall5:  0.810,
				Recall10: 0.923,
				NDCG10:   0.665,
				MRR10:    0.631,
			},
		},
		"nfcorpus": {
			Name:   "nfcorpus",
			System: "BM25 (BEIR)",
			Metrics: MetricsSet{
				Recall5:  0.144,
				Recall10: 0.207,
				NDCG10:   0.325,
				MRR10:    0.378,
			},
		},
		"fiqa": {
			Name:   "fiqa",
			System: "BM25 (BEIR)",
			Metrics: MetricsSet{
				Recall5:  0.234,
				Recall10: 0.368,
				NDCG10:   0.236,
				MRR10:    0.214,
			},
		},
	}
}

// CompareToBaseline computes deltas between strategy results and published baselines.
func CompareToBaseline(results []DatasetResult, baselines map[string]Baseline) []Comparison {
	var comps []Comparison
	for _, r := range results {
		bl, ok := baselines[r.DatasetName]
		if !ok {
			continue
		}
		comps = append(comps, Comparison{
			StrategyName: r.StrategyName,
			BaselineName: bl.Name,
			System:       bl.System,
			Metrics:      r.Metrics,
			BaseMet:      bl.Metrics,
			Delta: MetricsSet{
				Recall5:  r.Metrics.Recall5 - bl.Metrics.Recall5,
				Recall10: r.Metrics.Recall10 - bl.Metrics.Recall10,
				NDCG10:   r.Metrics.NDCG10 - bl.Metrics.NDCG10,
				MRR10:    r.Metrics.MRR10 - bl.Metrics.MRR10,
			},
		})
	}
	return comps
}
