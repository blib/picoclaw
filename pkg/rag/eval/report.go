package eval

import (
	"fmt"
	"html/template"
	"io"
	"strings"
	"time"
)

// Report holds all data needed to render the evaluation report.
type Report struct {
	Title       string
	GeneratedAt time.Time
	Results     []DatasetResult
	Comparisons []Comparison
	// Grouped by dataset for easier rendering.
	ByDataset map[string][]DatasetResult
}

// NewReport builds a report from evaluation results.
func NewReport(results []DatasetResult) *Report {
	baselines := PublishedBaselines()
	comps := CompareToBaseline(results, baselines)

	byDS := make(map[string][]DatasetResult)
	for _, r := range results {
		byDS[r.DatasetName] = append(byDS[r.DatasetName], r)
	}

	return &Report{
		Title:       "PicoClaw RAG Evaluation",
		GeneratedAt: time.Now(),
		Results:     results,
		Comparisons: comps,
		ByDataset:   byDS,
	}
}

// WriteHTML renders the report as a self-contained HTML page.
func (r *Report) WriteHTML(w io.Writer) error {
	return reportTmpl.Execute(w, r)
}

// funcMap provides template helpers.
var funcMap = template.FuncMap{
	"pct": func(v float64) string {
		return fmt.Sprintf("%.1f%%", v*100)
	},
	"f3": func(v float64) string {
		return fmt.Sprintf("%.3f", v)
	},
	"delta": func(v float64) string {
		sign := "+"
		if v < 0 {
			sign = ""
		}
		return fmt.Sprintf("%s%.3f", sign, v)
	},
	"deltaClass": func(v float64) string {
		if v > 0.01 {
			return "positive"
		}
		if v < -0.01 {
			return "negative"
		}
		return "neutral"
	},
	"durSec": func(d time.Duration) string {
		return fmt.Sprintf("%.1fs", d.Seconds())
	},
	"commaInt": func(n int) string {
		s := fmt.Sprintf("%d", n)
		if len(s) <= 3 {
			return s
		}
		var b strings.Builder
		for i, c := range s {
			if i > 0 && (len(s)-i)%3 == 0 {
				b.WriteByte(',')
			}
			b.WriteRune(c)
		}
		return b.String()
	},
	"bestTokens": func(s EmbedderStats) int {
		return s.BestTokenCount()
	},
	"barWidth": func(v float64) string {
		w := v * 400
		if w < 2 {
			w = 2
		}
		return fmt.Sprintf("%.0f", w)
	},
}

var reportTmpl = template.Must(template.New("report").Funcs(funcMap).Parse(reportHTML))

const reportHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>
  :root {
    --bg: #0d1117; --fg: #c9d1d9; --card: #161b22;
    --border: #30363d; --accent: #58a6ff; --green: #3fb950;
    --red: #f85149; --yellow: #d29922;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    background: var(--bg); color: var(--fg); line-height: 1.6; padding: 2rem; }
  h1 { color: var(--accent); margin-bottom: 0.5rem; }
  h2 { color: var(--fg); margin: 2rem 0 1rem; border-bottom: 1px solid var(--border); padding-bottom: 0.5rem; }
  h3 { color: var(--accent); margin: 1.5rem 0 0.75rem; }
  .meta { color: #8b949e; font-size: 0.875rem; margin-bottom: 2rem; }
  table { border-collapse: collapse; width: 100%; margin-bottom: 1.5rem; }
  th, td { padding: 0.5rem 0.75rem; text-align: left; border: 1px solid var(--border); }
  th { background: var(--card); font-weight: 600; font-size: 0.8125rem; text-transform: uppercase;
    letter-spacing: 0.05em; color: #8b949e; }
  td { font-family: 'SF Mono', 'Cascadia Code', monospace; font-size: 0.875rem; }
  tr:hover td { background: rgba(88,166,255,0.04); }
  .positive { color: var(--green); }
  .negative { color: var(--red); }
  .neutral { color: var(--yellow); }
  .card { background: var(--card); border: 1px solid var(--border); border-radius: 6px;
    padding: 1rem 1.25rem; margin-bottom: 1rem; }
  .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 0.75rem; margin-bottom: 1rem; }
  .stat { text-align: center; }
  .stat-value { font-size: 1.5rem; font-weight: 700; color: var(--accent); font-family: 'SF Mono', monospace; }
  .stat-label { font-size: 0.75rem; color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em; }
  svg { margin: 1rem 0; }
  .bar { rx: 2; }
</style>
</head>
<body>

<h1>{{.Title}}</h1>
<p class="meta">Generated {{.GeneratedAt.Format "2006-01-02 15:04:05 MST"}}</p>

<!-- Summary table -->
<h2>Summary</h2>
<table>
  <thead>
    <tr><th>Dataset</th><th>Strategy</th><th>Recall@5</th><th>Recall@10</th><th>nDCG@10</th><th>MRR@10</th><th>Tokens</th><th>Time</th></tr>
  </thead>
  <tbody>
  {{range .Results}}
    <tr>
      <td>{{.DatasetName}}</td>
      <td>{{.StrategyName}}</td>
      <td>{{f3 .Metrics.Recall5}}</td>
      <td>{{f3 .Metrics.Recall10}}</td>
      <td>{{f3 .Metrics.NDCG10}}</td>
      <td>{{f3 .Metrics.MRR10}}</td>
      <td>{{commaInt (bestTokens .Embedding)}}</td>
      <td>{{durSec .Duration}}</td>
    </tr>
  {{end}}
  </tbody>
</table>

<!-- Baseline comparison -->
{{if .Comparisons}}
<h2>vs. Published Baselines</h2>
<table>
  <thead>
    <tr><th>Dataset</th><th>Strategy</th><th>Baseline</th><th>&Delta; Recall@10</th><th>&Delta; nDCG@10</th><th>&Delta; MRR@10</th></tr>
  </thead>
  <tbody>
  {{range .Comparisons}}
    <tr>
      <td>{{.BaselineName}}</td>
      <td>{{.StrategyName}}</td>
      <td>{{.System}}</td>
      <td class="{{deltaClass .Delta.Recall10}}">{{delta .Delta.Recall10}}</td>
      <td class="{{deltaClass .Delta.NDCG10}}">{{delta .Delta.NDCG10}}</td>
      <td class="{{deltaClass .Delta.MRR10}}">{{delta .Delta.MRR10}}</td>
    </tr>
  {{end}}
  </tbody>
</table>
{{end}}

<!-- Embedding cost -->
<h2>Embedding Cost</h2>
<table>
  <thead>
    <tr><th>Dataset</th><th>Strategy</th><th>API Calls</th><th>Texts</th><th>Chars</th><th>Est. Tokens</th><th>API Tokens</th><th>Dims</th></tr>
  </thead>
  <tbody>
  {{range .Results}}
    <tr>
      <td>{{.DatasetName}}</td>
      <td>{{.StrategyName}}</td>
      <td>{{commaInt .Embedding.TotalCalls}}</td>
      <td>{{commaInt .Embedding.TotalTexts}}</td>
      <td>{{commaInt .Embedding.TotalChars}}</td>
      <td>{{commaInt .Embedding.EstimatedTokens}}</td>
      <td>{{commaInt .Embedding.APITokens}}</td>
      <td>{{.Embedding.Dims}}</td>
    </tr>
  {{end}}
  </tbody>
</table>

<!-- Per-dataset detail -->
{{range $ds, $runs := .ByDataset}}
<h2>{{$ds}}</h2>

<div class="card">
<h3>nDCG@10</h3>
{{range $runs}}
<div class="bar-row">
  <span class="bar-label">{{.StrategyName}}</span>
  <div class="bar-fill" style="width:{{barWidth .Metrics.NDCG10}}px"></div>
  <span class="bar-val">{{f3 .Metrics.NDCG10}}</span>
</div>
{{end}}
</div>

<div class="card">
<h3>Recall@10</h3>
{{range $runs}}
<div class="bar-row">
  <span class="bar-label">{{.StrategyName}}</span>
  <div class="bar-fill" style="width:{{barWidth .Metrics.Recall10}}px"></div>
  <span class="bar-val">{{f3 .Metrics.Recall10}}</span>
</div>
{{end}}
</div>

<div class="card">
{{range $runs}}
<div style="margin-bottom:0.75rem;">
  <strong>{{.StrategyName}}</strong>
  <div class="stats-grid">
    <div class="stat"><div class="stat-value">{{commaInt .IndexInfo.TotalDocuments}}</div><div class="stat-label">Documents</div></div>
    <div class="stat"><div class="stat-value">{{commaInt .IndexInfo.TotalChunks}}</div><div class="stat-label">Chunks</div></div>
    <div class="stat"><div class="stat-value">{{commaInt (bestTokens .Embedding)}}</div><div class="stat-label">Tokens</div></div>
    <div class="stat"><div class="stat-value">{{durSec .Duration}}</div><div class="stat-label">Time</div></div>
  </div>
</div>
{{end}}
</div>
{{end}}

</body>
</html>`
