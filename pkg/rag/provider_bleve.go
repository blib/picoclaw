//go:build !no_bleve

package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/sipeed/picoclaw/pkg/config"
)

type bleveProvider struct {
	indexPath string
	infoPath  string
}

func newBleveProvider(_ string, _ config.RAGToolsConfig, indexRoot string) (IndexProvider, error) {
	return &bleveProvider{
		indexPath: filepath.Join(indexRoot, "state", "bleve"),
		infoPath:  filepath.Join(indexRoot, "state", "index_info.json"),
	}, nil
}

func (p *bleveProvider) Name() string {
	return "bleve"
}

func (p *bleveProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{Semantic: false}
}

func (p *bleveProvider) Build(_ context.Context, chunks []IndexedChunk, info IndexInfo) error {
	if err := os.MkdirAll(filepath.Dir(p.indexPath), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(p.indexPath); err != nil {
		return err
	}

	indexMapping := bleve.NewIndexMapping()
	docMapping := bleve.NewDocumentMapping()

	textStored := bleve.NewTextFieldMapping()
	textStored.Store = true
	textStored.Index = true

	numStored := bleve.NewNumericFieldMapping()
	numStored.Store = true
	numStored.Index = true

	keywordStored := bleve.NewTextFieldMapping()
	keywordStored.Store = true
	keywordStored.Index = true
	keywordStored.Analyzer = "keyword"

	docMapping.AddFieldMappingsAt("source_path", keywordStored)
	docMapping.AddFieldMappingsAt("chunk_ordinal", numStored)
	docMapping.AddFieldMappingsAt("heading_path", textStored)
	docMapping.AddFieldMappingsAt("start_char", numStored)
	docMapping.AddFieldMappingsAt("end_char", numStored)
	docMapping.AddFieldMappingsAt("document_version", keywordStored)
	docMapping.AddFieldMappingsAt("paragraph_id", keywordStored)
	docMapping.AddFieldMappingsAt("title", textStored)
	docMapping.AddFieldMappingsAt("date", keywordStored)
	docMapping.AddFieldMappingsAt("project", keywordStored)
	docMapping.AddFieldMappingsAt("confidentiality", keywordStored)
	docMapping.AddFieldMappingsAt("doc_type", keywordStored)
	docMapping.AddFieldMappingsAt("text", textStored)
	docMapping.AddFieldMappingsAt("snippet", textStored)
	docMapping.AddFieldMappingsAt("risk_score", numStored)
	docMapping.AddFieldMappingsAt("tags", textStored)
	docMapping.AddFieldMappingsAt("flags", textStored)
	indexMapping.DefaultMapping = docMapping

	indexType := "scorch"
	kvStore := "boltdb"

	idx, err := bleve.NewUsing(
		p.indexPath,
		indexMapping,
		indexType,
		kvStore,
		nil, // kvConfig
	)
	if err != nil {
		return err
	}
	defer idx.Close()

	batch := idx.NewBatch()
	for _, chunk := range chunks {
		docID := bleveDocID(chunk.SourcePath, chunk.ChunkOrdinal)
		doc := map[string]interface{}{
			"source_path":      chunk.SourcePath,
			"chunk_ordinal":    chunk.ChunkOrdinal,
			"heading_path":     chunk.ChunkLoc.HeadingPath,
			"start_char":       chunk.ChunkLoc.StartChar,
			"end_char":         chunk.ChunkLoc.EndChar,
			"document_version": chunk.DocumentVersion,
			"paragraph_id":     chunk.ParagraphID,
			"title":            chunk.Title,
			"date":             chunk.Date,
			"project":          chunk.Project,
			"tags":             chunk.Tags,
			"confidentiality":  chunk.Confidentiality,
			"doc_type":         chunk.DocType,
			"text":             chunk.Text,
			"snippet":          chunk.Snippet,
			"flags":            chunk.Flags,
			"risk_score":       chunk.RiskScore,
		}
		if err := batch.Index(docID, doc); err != nil {
			return err
		}
	}
	if err := idx.Batch(batch); err != nil {
		return err
	}

	infoBytes, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.infoPath, infoBytes, 0o644)
}

func (p *bleveProvider) Search(_ context.Context, query string, opts ProviderSearchOptions) (*ProviderSearchResult, error) {
	info, err := p.LoadIndexInfo(context.Background())
	if err != nil {
		return nil, err
	}

	idx, err := bleve.Open(p.indexPath)
	if err != nil {
		return nil, mapBleveOpenError(err)
	}
	defer idx.Close()

	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return &ProviderSearchResult{IndexInfo: *info, Hits: nil}, nil
	}

	q := bleve.NewQueryStringQuery(trimmed)
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}

	req := bleve.NewSearchRequestOptions(q, limit, 0, false)
	req.Fields = []string{"*"}

	res, err := idx.Search(req)
	if err != nil {
		return nil, err
	}

	hits := make([]ProviderHit, 0, len(res.Hits))
	for _, h := range res.Hits {
		chunk, err := bleveFieldsToChunk(h.Fields)
		if err != nil {
			continue
		}
		hits = append(hits, ProviderHit{
			Chunk:        chunk,
			LexicalScore: h.Score,
		})
	}

	return &ProviderSearchResult{
		IndexInfo: *info,
		Hits:      hits,
	}, nil
}

func (p *bleveProvider) FetchChunk(_ context.Context, sourcePath string, chunkOrdinal int) (*IndexedChunk, error) {
	idx, err := bleve.Open(p.indexPath)
	if err != nil {
		return nil, mapBleveOpenError(err)
	}
	defer idx.Close()

	req := bleve.NewSearchRequestOptions(bleve.NewDocIDQuery([]string{bleveDocID(sourcePath, chunkOrdinal)}), 1, 0, false)
	req.Fields = []string{"*"}

	res, err := idx.Search(req)
	if err != nil {
		return nil, err
	}
	if len(res.Hits) == 0 {
		return nil, os.ErrNotExist
	}

	chunk, err := bleveFieldsToChunk(res.Hits[0].Fields)
	if err != nil {
		return nil, err
	}
	return &chunk, nil
}

func (p *bleveProvider) LoadIndexInfo(_ context.Context) (*IndexInfo, error) {
	data, err := os.ReadFile(p.infoPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrIndexNotBuilt
		}
		return nil, err
	}

	var info IndexInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func bleveDocID(sourcePath string, chunkOrdinal int) string {
	return fmt.Sprintf("%s#%d", filepath.ToSlash(sourcePath), chunkOrdinal)
}

func bleveFieldsToChunk(fields map[string]interface{}) (IndexedChunk, error) {
	sourcePath := asString(fields["source_path"])
	ordinal := asInt(fields["chunk_ordinal"])
	if sourcePath == "" || ordinal <= 0 {
		return IndexedChunk{}, fmt.Errorf("invalid bleve hit fields")
	}

	chunk := IndexedChunk{
		SourcePath:      sourcePath,
		ChunkOrdinal:    ordinal,
		DocumentVersion: asString(fields["document_version"]),
		ParagraphID:     asString(fields["paragraph_id"]),
		Title:           asString(fields["title"]),
		Date:            asString(fields["date"]),
		Project:         asString(fields["project"]),
		Tags:            asStringSlice(fields["tags"]),
		Confidentiality: asString(fields["confidentiality"]),
		DocType:         asString(fields["doc_type"]),
		Text:            asString(fields["text"]),
		Snippet:         asString(fields["snippet"]),
		Flags:           asStringSlice(fields["flags"]),
		RiskScore:       asFloat(fields["risk_score"]),
		ChunkLoc: ChunkLoc{
			HeadingPath: asString(fields["heading_path"]),
			StartChar:   asInt(fields["start_char"]),
			EndChar:     asInt(fields["end_char"]),
		},
	}
	return chunk, nil
}

func mapBleveOpenError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "cannot open index") || strings.Contains(strings.ToLower(err.Error()), "no such file") {
		return ErrIndexNotBuilt
	}
	return err
}

func asString(v interface{}) string {
	switch typed := v.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func asInt(v interface{}) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(typed))
		return n
	default:
		return 0
	}
}

func asFloat(v interface{}) float64 {
	switch typed := v.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		f, _ := typed.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f
	default:
		return 0
	}
}

func asStringSlice(v interface{}) []string {
	switch typed := v.(type) {
	case []string:
		return typed
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, it := range typed {
			if s := asString(it); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}
