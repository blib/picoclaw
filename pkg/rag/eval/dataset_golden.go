package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// GoldenDataset loads a custom evaluation dataset from a YAML file
// with queries, relevance judgments, and a directory of markdown docs.
type GoldenDataset struct {
	name    string
	file    string
	corpus  []Document
	queries []Query
	qrels   Qrels
	loaded  bool
}

// goldenFile is the YAML schema for golden evaluation files.
type goldenFile struct {
	Dataset   string        `yaml:"dataset"`
	CorpusDir string        `yaml:"corpus_dir"` // relative to YAML file location
	Queries   []goldenQuery `yaml:"queries"`
}

type goldenQuery struct {
	ID        string         `yaml:"id"`
	Text      string         `yaml:"text"`
	Relevance map[string]int `yaml:"relevance"` // doc filename → grade
}

// NewGoldenDataset creates a dataset loader from a YAML golden file.
func NewGoldenDataset(yamlPath string) *GoldenDataset {
	return &GoldenDataset{
		file: yamlPath,
	}
}

func (d *GoldenDataset) Name() string {
	if d.name != "" {
		return d.name
	}
	base := filepath.Base(d.file)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func (d *GoldenDataset) Prepare(_ interface{}, _ string) error {
	if d.loaded {
		return nil
	}

	data, err := os.ReadFile(d.file)
	if err != nil {
		return fmt.Errorf("read golden file: %w", err)
	}

	var gf goldenFile
	if err := yaml.Unmarshal(data, &gf); err != nil {
		return fmt.Errorf("parse golden file: %w", err)
	}

	if gf.Dataset != "" {
		d.name = gf.Dataset
	}

	// Load corpus from directory.
	corpusDir := gf.CorpusDir
	if !filepath.IsAbs(corpusDir) {
		corpusDir = filepath.Join(filepath.Dir(d.file), corpusDir)
	}

	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return fmt.Errorf("read corpus dir %s: %w", corpusDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(corpusDir, entry.Name()))
		if err != nil {
			continue
		}
		d.corpus = append(d.corpus, Document{
			ID:   entry.Name(),
			Text: string(content),
		})
	}

	// Build queries and qrels.
	d.qrels = make(Qrels)
	for _, q := range gf.Queries {
		d.queries = append(d.queries, Query{ID: q.ID, Text: q.Text})
		if len(q.Relevance) > 0 {
			d.qrels[q.ID] = q.Relevance
		}
	}

	d.loaded = true
	return nil
}

func (d *GoldenDataset) Corpus() []Document        { return d.corpus }
func (d *GoldenDataset) Queries() []Query           { return d.queries }
func (d *GoldenDataset) RelevanceJudgments() Qrels  { return d.qrels }
