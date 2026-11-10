// Package exporter writes portable result/metric bundles before node cleanup.
package exporter

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/evidence"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	NodeID, Output, ControllerURL, AdminToken string
	RunID                                     string
	PrometheusURL, PrometheusToken            string
	Start, End                                time.Time
	Step                                      time.Duration
	Queries                                   []string
	RawSource                                 func(context.Context, []protocol.EvidenceRef, string) error
	HTTPClient                                *http.Client
}
type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size_bytes"`
}
type History struct {
	Query    string   `json:"query"`
	File     string   `json:"file"`
	Series   int      `json:"series"`
	Warnings []string `json:"warnings,omitempty"`
}
type Manifest struct {
	SelectedRun        string     `json:"selected_run_id,omitempty"`
	SchemaVersion      string     `json:"schema_version"`
	NodeID             string     `json:"node_id"`
	CreatedAt          time.Time  `json:"created_at"`
	DatabaseCapturedAt time.Time  `json:"database_captured_at"`
	MetricsCapturedAt  time.Time  `json:"metrics_captured_at"`
	ExporterVersion    string     `json:"exporter_version"`
	MetricsScope       string     `json:"metrics_scope"`
	RawIncluded        bool       `json:"raw_evidence_included"`
	RawFileCount       int        `json:"raw_file_count"`
	Start              *time.Time `json:"history_start,omitempty"`
	End                *time.Time `json:"history_end,omitempty"`
	StepSeconds        float64    `json:"history_step_seconds,omitempty"`
	History            []History  `json:"prometheus_queries,omitempty"`
	Warnings           []string   `json:"warnings"`
	Files              []File     `json:"files"`
}

func endpoint(base, path string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid source URL")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("source URL must be HTTP(S) without embedded credentials, query, or fragment")
	}
	return strings.TrimRight(base, "/") + path, nil
}

var errResponseTooLarge = errors.New("export response exceeds size limit")

func fetch(ctx context.Context, o Options, base, path, token, kind string, limit int64) ([]byte, error) {
	address, err := endpoint(base, path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		if kind == "admin" {
			request.Header.Set("X-ModelGauge-Admin-Token", token)
		} else {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if o.HTTPClient != nil {
		copy := *o.HTTPClient
		copy.CheckRedirect = client.CheckRedirect
		client = &copy
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("export source unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("export source returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errResponseTooLarge
	}
	return data, nil
}
func validate(o Options) error {
	if o.NodeID == "" || o.Output == "" || o.AdminToken == "" {
		return fmt.Errorf("node, output directory, and controller/admin connection are required")
	}
	if _, err := endpoint(o.ControllerURL, ""); err != nil {
		return err
	}
	if o.PrometheusURL == "" {
		if !o.Start.IsZero() || !o.End.IsZero() || len(o.Queries) > 0 {
			return fmt.Errorf("history times/queries require --prometheus-url")
		}
		return nil
	}
	if _, err := endpoint(o.PrometheusURL, ""); err != nil {
		return err
	}
	if o.Start.IsZero() || o.End.IsZero() || !o.End.After(o.Start) || o.Step < time.Second || o.End.Sub(o.Start)/o.Step > 11000 {
		return fmt.Errorf("history needs start < end, step >= 1s, and at most 11000 intervals")
	}
	if len(o.Queries) > 20 {
		return fmt.Errorf("at most 20 history queries are supported")
	}
	return nil
}
func write(root, name string, data []byte) error {
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	return closed
}
func writeJSON(root, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return write(root, name, append(data, '\n'))
}
func Write(ctx context.Context, o Options) (manifest Manifest, err error) {
	if err = validate(o); err != nil {
		return manifest, err
	}
	if _, e := os.Lstat(o.Output); !os.IsNotExist(e) {
		return manifest, fmt.Errorf("output must be a new directory; refusing to overwrite")
	}
	snapshotPath := "/v1/nodes/" + url.PathEscape(o.NodeID) + "/export"
	if o.RunID != "" {
		snapshotPath += "?run=" + url.QueryEscape(o.RunID)
	}
	snapshotBytes, err := fetch(ctx, o, o.ControllerURL, snapshotPath, o.AdminToken, "admin", 64<<20)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) && o.RunID == "" {
			return manifest, fmt.Errorf("%w; use --run RUN_ID to export one experiment", err)
		}
		return manifest, err
	}
	var snapshot controller.NodeExport
	if err = json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		return manifest, err
	}
	var node struct {
		ID string `json:"node_id"`
	}
	if err = json.Unmarshal(snapshot.Node, &node); err != nil {
		return manifest, err
	}
	if snapshot.SelectedRun != o.RunID {
		return manifest, fmt.Errorf("controller did not honor selected run; update controller before exporting")
	}
	if snapshot.SchemaVersion != "modelgauge-node-export/v1" || node.ID != o.NodeID || snapshot.CapturedAt.IsZero() {
		return manifest, fmt.Errorf("controller export schema/node identity mismatch")
	}
	metrics, err := fetch(ctx, o, o.ControllerURL, "/metrics", "", "", 16<<20)
	if err != nil {
		return manifest, err
	}
	if !strings.Contains(string(metrics), "# TYPE modelgauge_") {
		return manifest, fmt.Errorf("controller did not return ModelGauge metrics")
	}
	// Reserve a new directory exclusively. Any later failure intentionally leaves
	// INCOMPLETE and no finalized manifest/checksum pair; user data is never erased.
	if err = os.MkdirAll(filepath.Dir(o.Output), 0700); err != nil {
		return manifest, err
	}
	if err = os.Mkdir(o.Output, 0700); err != nil {
		return manifest, err
	}
	if err = write(o.Output, "INCOMPLETE", []byte("Export did not complete. Do not delete source evidence based on this directory.\n")); err != nil {
		return manifest, err
	}
	manifest = Manifest{SelectedRun: o.RunID, SchemaVersion: "modelgauge-export/v1", NodeID: o.NodeID, CreatedAt: time.Now().UTC(), DatabaseCapturedAt: snapshot.CapturedAt, MetricsCapturedAt: time.Now().UTC(), ExporterVersion: buildinfo.Version, MetricsScope: "controller-wide latest gauges; not historical samples", Warnings: []string{}, Files: []File{}}
	for _, section := range []struct {
		name  string
		value any
	}{
		{"results.json", snapshot.Results}, {"node.json", json.RawMessage(snapshot.Node)}, {"plans.json", snapshot.Plans}, {"runs.json", snapshot.Runs}, {"models.json", snapshot.Models}, {"cases.json", snapshot.Cases}, {"memory.json", snapshot.Memory}, {"host-snapshots.json", snapshot.Snapshots}, {"model-installs.json", snapshot.InstallJobs}, {"stored-recommendations.json", snapshot.Recommendations}, {"artifacts.json", snapshot.Artifacts},
	} {
		if err = writeJSON(o.Output, section.name, section.value); err != nil {
			return manifest, err
		}
	}
	if err = write(o.Output, "metrics.prom", metrics); err != nil {
		return manifest, err
	}
	var csvData strings.Builder
	if err = resultsCSV(&csvData, snapshot.Results); err != nil {
		return manifest, err
	}
	if err = write(o.Output, "results.csv", []byte(csvData.String())); err != nil {
		return manifest, err
	}
	var reports []protocol.Result
	for _, r := range snapshot.Results {
		reports = append(reports, protocol.Result{RunID: r.RunID, PlanID: r.PlanID, NodeID: r.NodeID, ExecutionMode: protocol.ExecutionMode(r.ExecutionMode), Status: protocol.RunStatus(r.Status), Benchmark: r.Benchmark})
	}
	reportFiles, err := benchmark.WriteReports(reports)
	if err != nil {
		return manifest, err
	}
	for name, data := range reportFiles {
		if err = write(o.Output, name, data); err != nil {
			return manifest, err
		}
	}
	for _, p := range snapshot.Plans {
		var plan struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(p, &plan)
		if plan.Status == "running" || plan.Status == "queued" || plan.Status == "leased" {
			manifest.Warnings = append(manifest.Warnings, "Active/queued plans are included as metadata; final results require a later export.")
			break
		}
	}
	manifest.Warnings = append(manifest.Warnings, "Database snapshot, controller-wide current metrics and optional history are separate observations, not one global atomic snapshot.")
	if o.RawSource != nil {
		raw := filepath.Join(o.Output, "raw")
		if err = os.Mkdir(raw, 0700); err != nil {
			return manifest, err
		}
		if len(snapshot.Artifacts) > 0 {
			if err = o.RawSource(ctx, snapshot.Artifacts, raw); err != nil {
				return manifest, fmt.Errorf("raw evidence export: %w", err)
			}
		}
		manifest.RawIncluded = len(snapshot.Artifacts) > 0
		manifest.RawFileCount = len(snapshot.Artifacts)
		if len(snapshot.Artifacts) == 0 {
			manifest.Warnings = append(manifest.Warnings, "No raw artifact references were recorded; no raw files were included.")
		}
	} else {
		manifest.Warnings = append(manifest.Warnings, "Raw node files were not included; artifacts.json contains references/checksums only.")
	}
	if o.PrometheusURL != "" {
		if err = history(ctx, o, &manifest); err != nil {
			return manifest, err
		}
	} else {
		manifest.Warnings = append(manifest.Warnings, "Prometheus history was not requested; metrics.prom contains current controller gauges only.")
	}
	if err = filepath.WalkDir(o.Output, func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() || path == filepath.Join(o.Output, "INCOMPLETE") {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular file in export")
		}
		file, e := os.Open(path)
		if e != nil {
			return e
		}
		hash := sha256.New()
		size, e := io.Copy(hash, file)
		file.Close()
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(o.Output, path)
		if e != nil {
			return e
		}
		manifest.Files = append(manifest.Files, File{Name: filepath.ToSlash(relative), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size})
		return nil
	}); err != nil {
		return manifest, err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Name < manifest.Files[j].Name })
	if err = writeJSON(o.Output, "manifest.json", manifest); err != nil {
		return manifest, err
	}
	var sums strings.Builder
	for _, file := range manifest.Files {
		fmt.Fprintf(&sums, "%s  %s\n", file.SHA256, file.Name)
	}
	data, e := os.ReadFile(filepath.Join(o.Output, "manifest.json"))
	if e != nil {
		return manifest, e
	}
	hash := sha256.Sum256(data)
	fmt.Fprintf(&sums, "%x  manifest.json\n", hash)
	if err = write(o.Output, "SHA256SUMS", []byte(sums.String())); err != nil {
		return manifest, err
	}
	if err = os.Remove(filepath.Join(o.Output, "INCOMPLETE")); err != nil {
		return manifest, err
	}
	return manifest, nil
}
func csvText(s string) string {
	trim := strings.TrimLeft(s, " \t\r\n")
	if len(trim) > 0 && strings.ContainsRune("=+-@", rune(trim[0])) {
		return "'" + s
	}
	return s
}
func resultsCSV(out io.Writer, results []controller.RunSummary) error {
	writer := csv.NewWriter(out)
	if err := writer.Write([]string{"run_id", "plan_id", "node_id", "model_id", "execution_mode", "status", "completed_at", "case_id", "threads", "prompt_tokens", "generation_tokens", "repetitions", "tokens_per_second", "stddev_tokens_per_second", "peak_rss_bytes", "safe_reservation_bytes", "recovered", "invalid_reason"}); err != nil {
		return err
	}
	for _, r := range results {
		cases := r.Cases
		if len(cases) == 0 {
			cases = []controller.StoredCase{{}}
		}
		for _, c := range cases {
			fields := []string{r.RunID, r.PlanID, r.NodeID, r.ModelID, r.ExecutionMode, r.Status, r.CompletedAt, c.CaseID}
			for i := range fields {
				fields[i] = csvText(fields[i])
			}
			values := []string{"", "", "", "", "", ""}
			if c.CaseID != "" {
				values = []string{strconv.Itoa(c.Threads), strconv.Itoa(c.PromptTokens), strconv.Itoa(c.GenerationTokens), strconv.Itoa(c.Repetitions), strconv.FormatFloat(c.TokensPerSecond, 'g', -1, 64), strconv.FormatFloat(c.TokensPerSecondStddev, 'g', -1, 64)}
			}
			fields = append(fields, values...)
			rss, reservation := "", ""
			if r.PeakRSSBytes > 0 {
				rss = strconv.FormatInt(r.PeakRSSBytes, 10)
			}
			if r.SafeReservationBytes > 0 {
				reservation = strconv.FormatInt(r.SafeReservationBytes, 10)
			}
			fields = append(fields, rss, reservation, strconv.FormatBool(r.Recovered), csvText(r.InvalidReason))
			if err := writer.Write(fields); err != nil {
				return err
			}
		}
	}
	writer.Flush()
	return writer.Error()
}
func history(ctx context.Context, o Options, m *Manifest) error {
	queries := o.Queries
	if len(queries) == 0 {
		selector := "{node=" + strconv.Quote(o.NodeID) + "}"
		queries = []string{"modelgauge_benchmark_tokens_per_second" + selector, "modelgauge_peak_rss_bytes" + selector, "node_memory_MemAvailable_bytes" + selector, "rate(node_cpu_seconds_total" + selector + "[1m])"}
	}
	start, end := o.Start.UTC(), o.End.UTC()
	m.Start = &start
	m.End = &end
	m.StepSeconds = o.Step.Seconds()
	for index, query := range queries {
		if strings.TrimSpace(query) == "" {
			return fmt.Errorf("history query must not be empty")
		}
		values := url.Values{"query": {query}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "step": {strconv.FormatFloat(o.Step.Seconds(), 'f', -1, 64)}}
		// Base URL has no query; query-range parameters are encoded separately.
		base, err := endpoint(o.PrometheusURL, "/api/v1/query_range")
		if err != nil {
			return err
		}
		parsed, _ := url.Parse(base)
		parsed.RawQuery = values.Encode()
		request, err := http.NewRequestWithContext(ctx, "GET", parsed.String(), nil)
		if err != nil {
			return err
		}
		if o.PrometheusToken != "" {
			request.Header.Set("Authorization", "Bearer "+o.PrometheusToken)
		}
		client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if o.HTTPClient != nil {
			copy := *o.HTTPClient
			copy.CheckRedirect = client.CheckRedirect
			client = &copy
		}
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("Prometheus history unavailable: %w", err)
		}
		data, e := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
		response.Body.Close()
		if e != nil {
			return e
		}
		if response.StatusCode != 200 || len(data) > 32<<20 {
			return fmt.Errorf("Prometheus history request failed or exceeded size limit")
		}
		var envelope struct {
			Status string `json:"status"`
			Data   struct {
				ResultType string            `json:"resultType"`
				Result     []json.RawMessage `json:"result"`
			} `json:"data"`
			Warnings []string `json:"warnings"`
		}
		if err = json.Unmarshal(data, &envelope); err != nil {
			return err
		}
		if envelope.Status != "success" || envelope.Data.ResultType != "matrix" {
			return fmt.Errorf("Prometheus query failed or returned an unexpected result type")
		}
		name := fmt.Sprintf("prometheus/query-%02d.json", index+1)
		if err = write(o.Output, name, data); err != nil {
			return err
		}
		m.History = append(m.History, History{Query: query, File: name, Series: len(envelope.Data.Result), Warnings: envelope.Warnings})
		if len(envelope.Data.Result) == 0 {
			m.Warnings = append(m.Warnings, fmt.Sprintf("History query %d returned no series; check retention, labels and exporter setup.", index+1))
		}
	}
	return nil
}

// LocalEvidence adapts an explicit copied node-results directory to export.
func LocalEvidence(root string) func(context.Context, []protocol.EvidenceRef, string) error {
	return func(ctx context.Context, refs []protocol.EvidenceRef, destination string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return evidence.CopyLocalContext(ctx, root, destination, refs)
	}
}
