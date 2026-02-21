package eval

import "context"

// Document represents a single corpus document from an evaluation dataset.
type Document struct {
	ID    string `json:"_id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Query represents a single evaluation query with its text.
type Query struct {
	ID   string `json:"_id"`
	Text string `json:"text"`
}

// Qrels maps query IDs to document relevance judgments.
// Inner map: docID → relevance grade (0 = not relevant, 1 = relevant, 2 = highly relevant).
type Qrels map[string]map[string]int

// Dataset is the abstraction over evaluation datasets. Implementations
// handle downloading, parsing, and converting their native formats.
type Dataset interface {
	// Name returns a short identifier for the dataset (e.g. "scifact").
	Name() string

	// Prepare downloads and/or parses the dataset into cacheDir.
	// Idempotent: skips download if already cached.
	Prepare(ctx context.Context, cacheDir string) error

	// Corpus returns all documents in the dataset.
	Corpus() []Document

	// Queries returns all evaluation queries.
	Queries() []Query

	// RelevanceJudgments returns the ground-truth relevance labels.
	RelevanceJudgments() Qrels
}

// DatasetRegistry tracks available datasets by name.
var DatasetRegistry = map[string]func() Dataset{}

// RegisterDataset adds a named dataset constructor to the registry.
func RegisterDataset(name string, factory func() Dataset) {
	DatasetRegistry[name] = factory
}

// AvailableDatasets returns sorted names of registered datasets.
func AvailableDatasets() []string {
	names := make([]string, 0, len(DatasetRegistry))
	for name := range DatasetRegistry {
		names = append(names, name)
	}
	return names
}
