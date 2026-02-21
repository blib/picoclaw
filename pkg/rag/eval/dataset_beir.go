package eval

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const beirBaseURL = "https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets"

// BEIRDataset loads a standard BEIR benchmark dataset from the official
// distribution (zip containing corpus.jsonl, queries.jsonl, qrels/test.tsv).
type BEIRDataset struct {
	name    string
	corpus  []Document
	queries []Query
	qrels   Qrels
	loaded  bool
}

// NewBEIRDataset creates a BEIR dataset loader for the given dataset name.
// Supported names: scifact, nfcorpus, fiqa, trec-covid, scidocs, etc.
func NewBEIRDataset(name string) *BEIRDataset {
	return &BEIRDataset{name: name}
}

func (d *BEIRDataset) Name() string { return d.name }

func (d *BEIRDataset) Prepare(ctx context.Context, cacheDir string) error {
	dataDir := filepath.Join(cacheDir, d.name)

	// Check if already extracted.
	corpusPath := filepath.Join(dataDir, "corpus.jsonl")
	if _, err := os.Stat(corpusPath); err == nil {
		return d.load(dataDir)
	}

	// Download zip.
	zipPath := filepath.Join(cacheDir, d.name+".zip")
	if _, err := os.Stat(zipPath); err != nil {
		url := fmt.Sprintf("%s/%s.zip", beirBaseURL, d.name)
		if err := downloadFile(ctx, url, zipPath); err != nil {
			return fmt.Errorf("download %s: %w", d.name, err)
		}
	}

	// Extract.
	if err := extractZip(zipPath, cacheDir); err != nil {
		return fmt.Errorf("extract %s: %w", d.name, err)
	}

	return d.load(dataDir)
}

func (d *BEIRDataset) Corpus() []Document        { return d.corpus }
func (d *BEIRDataset) Queries() []Query           { return d.queries }
func (d *BEIRDataset) RelevanceJudgments() Qrels  { return d.qrels }

func (d *BEIRDataset) load(dataDir string) error {
	if d.loaded {
		return nil
	}

	corpus, err := loadBEIRCorpus(filepath.Join(dataDir, "corpus.jsonl"))
	if err != nil {
		return err
	}
	d.corpus = corpus

	queries, err := loadBEIRQueries(filepath.Join(dataDir, "queries.jsonl"))
	if err != nil {
		return err
	}
	d.queries = queries

	qrels, err := loadBEIRQrels(filepath.Join(dataDir, "qrels", "test.tsv"))
	if err != nil {
		return err
	}
	d.qrels = qrels

	d.loaded = true
	return nil
}

func loadBEIRCorpus(path string) ([]Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var docs []Document
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var doc Document
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			continue // skip malformed lines
		}
		if doc.ID == "" {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, scanner.Err()
}

func loadBEIRQueries(path string) ([]Query, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var queries []Query
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var q Query
		if err := json.Unmarshal(scanner.Bytes(), &q); err != nil {
			continue
		}
		if q.ID == "" {
			continue
		}
		queries = append(queries, q)
	}
	return queries, scanner.Err()
}

func loadBEIRQrels(path string) (Qrels, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	qrels := make(Qrels)
	scanner := bufio.NewScanner(f)
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			first = false
			// skip header if present
			if strings.HasPrefix(line, "query-id") || strings.HasPrefix(line, "query_id") {
				continue
			}
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		queryID := parts[0]
		docID := parts[1]
		rel, err := strconv.Atoi(parts[2])
		if err != nil {
			continue
		}
		if qrels[queryID] == nil {
			qrels[queryID] = make(map[string]int)
		}
		qrels[queryID][docID] = rel
	}
	return qrels, scanner.Err()
}

func downloadFile(ctx context.Context, url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	return os.Rename(tmp, dest)
}

func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)

		// Prevent zip slip.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		src, err := f.Open()
		if err != nil {
			return err
		}

		dst, err := os.Create(target)
		if err != nil {
			src.Close()
			return err
		}

		_, copyErr := io.Copy(dst, src)
		src.Close()
		dst.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// DocToMarkdown converts a BEIR Document into markdown with frontmatter
// for ingestion by the picoclaw RAG pipeline.
func DocToMarkdown(doc Document) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %s\n", yamlEscape(doc.Title)))
	sb.WriteString("confidentiality: internal\n")
	sb.WriteString("---\n\n")
	if doc.Title != "" {
		sb.WriteString("# ")
		sb.WriteString(doc.Title)
		sb.WriteString("\n\n")
	}
	sb.WriteString(doc.Text)
	sb.WriteString("\n")
	return sb.String()
}

func yamlEscape(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`") || strings.HasPrefix(s, "- ") {
		escaped := strings.ReplaceAll(s, `"`, `\"`)
		return `"` + escaped + `"`
	}
	return s
}

func init() {
	for _, name := range []string{"scifact", "nfcorpus", "fiqa"} {
		n := name // capture
		RegisterDataset(n, func() Dataset { return NewBEIRDataset(n) })
	}
}
