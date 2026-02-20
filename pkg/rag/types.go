package rag

import "time"

type SearchMode string

const (
	ModeKeywordOnly  SearchMode = "keyword-only"
	ModeSemanticOnly SearchMode = "semantic-only"
	ModeHybrid       SearchMode = "hybrid"
)

type SearchFilters struct {
	Tags                 []string `json:"tags,omitempty"`
	TagMode              string   `json:"tag_mode,omitempty"`
	Project              []string `json:"project,omitempty"`
	DocType              []string `json:"doc_type,omitempty"`
	DateFrom             string   `json:"date_from,omitempty"`
	DateTo               string   `json:"date_to,omitempty"`
	ConfidentialityAllow []string `json:"confidentiality_allow,omitempty"`
	AllowRestricted      bool     `json:"allow_restricted,omitempty"`
}

type SearchRequest struct {
	Query     string        `json:"query"`
	ProfileID string        `json:"profile_id,omitempty"`
	Mode      SearchMode    `json:"mode,omitempty"`
	TopK      int           `json:"top_k,omitempty"`
	Filters   SearchFilters `json:"filters,omitempty"`
}

type ChunkRef struct {
	SourcePath   string `json:"source_path"`
	ChunkOrdinal int    `json:"chunk_ordinal"`
}

type ChunkLoc struct {
	HeadingPath string `json:"heading_path"`
	StartByte   int    `json:"start_byte"`
	EndByte     int    `json:"end_byte"`
}

type ScoreBreakdown struct {
	BM25Norm      float64 `json:"bm25_norm,omitempty"`
	CosineNorm    float64 `json:"cosine_norm,omitempty"`
	FreshnessNorm float64 `json:"freshness_norm,omitempty"`
	MetadataBoost float64 `json:"metadata_boost,omitempty"`
	FinalScore    float64 `json:"final_score"`
}

type EvidenceItemFull struct {
	SourcePath      string         `json:"source_path"`
	ChunkRef        ChunkRef       `json:"chunk_ref"`
	ChunkLoc        ChunkLoc       `json:"chunk_loc"`
	DocumentVersion string         `json:"document_version"`
	Title           string         `json:"title,omitempty"`
	Date            string         `json:"date,omitempty"`
	Snippet         string         `json:"snippet"`
	Score           float64        `json:"score"`
	ScoreBreakdown  ScoreBreakdown `json:"score_breakdown,omitempty"`
	Flags           []string       `json:"flags,omitempty"`
}

type Coverage struct {
	UniqueSources int       `json:"unique_sources"`
	TimeSpan      *TimeSpan `json:"time_span,omitempty"`
}

type TimeSpan struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type IndexInfo struct {
	IndexVersion     string   `json:"index_version"`
	IndexState       string   `json:"index_state"`
	IndexProvider    string   `json:"index_provider,omitempty"`
	BuiltAt          string   `json:"built_at"`
	EmbeddingModelID string   `json:"embedding_model_id,omitempty"`
	ChunkingHash     string   `json:"chunking_hash,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	TotalDocuments   int      `json:"total_documents"`
	TotalChunks      int      `json:"total_chunks"`
}

type EvidencePackFull struct {
	Query     string             `json:"query"`
	ProfileID string             `json:"profile_id"`
	IndexInfo IndexInfo          `json:"index_info"`
	Items     []EvidenceItemFull `json:"items"`
	Coverage  Coverage           `json:"coverage"`
	Notes     []string           `json:"notes,omitempty"`
}

type EvidenceItemLLM struct {
	Ref     string  `json:"ref"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

type EvidencePackLLM struct {
	Query     string            `json:"query"`
	ProfileID string            `json:"profile_id"`
	Sources   map[string]string `json:"sources"`
	Items     []EvidenceItemLLM `json:"items"`
	Notes     []string          `json:"notes,omitempty"`
}

type SearchResult struct {
	Full *EvidencePackFull `json:"full,omitempty"`
	LLM  *EvidencePackLLM  `json:"llm,omitempty"`
}

type IndexedChunk struct {
	SourcePath      string   `json:"source_path"`
	ChunkOrdinal    int      `json:"chunk_ordinal"`
	ChunkLoc        ChunkLoc `json:"chunk_loc"`
	DocumentVersion string   `json:"document_version"`
	ParagraphID     string   `json:"paragraph_id"`
	Title           string   `json:"title,omitempty"`
	Date            string   `json:"date,omitempty"`
	Project         string   `json:"project,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	Confidentiality string   `json:"confidentiality,omitempty"`
	DocType         string   `json:"doc_type,omitempty"`
	Text            string   `json:"text"`
	Snippet         string   `json:"snippet"`
	Flags           []string `json:"flags,omitempty"`
	RiskScore       float64  `json:"risk_score,omitempty"`
}

type IndexStore struct {
	Info   IndexInfo      `json:"info"`
	Chunks []IndexedChunk `json:"chunks"`
}

type ChunkResult struct {
	SourcePath   string   `json:"source_path"`
	ChunkOrdinal int      `json:"chunk_ordinal"`
	ChunkLoc     ChunkLoc `json:"chunk_loc"`
	Text         string   `json:"text"`
	Snippet      string   `json:"snippet"`
}

type docMeta struct {
	Title           string
	Date            string
	EffectiveDate   string
	Project         string
	Tags            []string
	Source          string
	Confidentiality string
}

type FixedProfile struct {
	ID                  string
	DefaultMode         SearchMode
	BM25TopN            int
	SemanticTopN        int
	WeightBM25          float64
	WeightCosine        float64
	WeightFreshness     float64
	WeightMetadataBoost float64
	PerSourceCap        int
	PreferNotesPolicy   bool
}

func parseISODate(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{"2006-01-02", time.RFC3339}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
