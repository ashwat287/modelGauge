package exporter

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func snapshotFixture() controller.NodeExport {
	return controller.NodeExport{SchemaVersion: "modelgauge-node-export/v1", CapturedAt: time.Now().UTC(), Node: json.RawMessage(`{"node_id":"node-1"}`), Results: []controller.RunSummary{{RunID: "run-1", NodeID: "node-1", ModelID: "=SUM(1,2)", Status: "completed", PeakRSSBytes: 100, SafeReservationBytes: 125, Cases: []controller.StoredCase{{CaseID: "prompt-32-t2", Threads: 2, PromptTokens: 32, Repetitions: 5, TokensPerSecond: 25, TokensPerSecondStddev: .1}}}, {RunID: "run-failed", Status: "failed", InvalidReason: "stopped"}}}
}

func TestServingExportIncludesDetailedCSV(t *testing.T) {
	snapshot := snapshotFixture()
	snapshot.Results[0].Benchmark = &protocol.BenchmarkReport{Kind: "serving", Data: json.RawMessage(`{"schema_version":"modelgauge-serving/v1","model_id":"=unsafe-name","endpoint":"http://localhost:8080","cases":[{"id":"load","scenario":"load","concurrency":1,"successful":1,"ttft_ms":{"count":1,"mean":10,"p95":10}}]}`)}
	server := fixtureServer(t, snapshot, false)
	defer server.Close()
	root := filepath.Join(t.TempDir(), "export")
	m, err := Write(context.Background(), Options{NodeID: "node-1", Output: root, ControllerURL: server.URL, AdminToken: "secret-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "serving-results.csv"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(rows) != 2 || !strings.Contains(string(data), "'=unsafe-name") || !strings.Contains(string(data), "ttft_ms_p95") {
		t.Fatal(string(data), err)
	}
	found := false
	for _, f := range m.Files {
		if f.Name == "serving-results.csv" {
			found = true
		}
	}
	if !found {
		t.Fatal("serving CSV missing from checksum manifest")
	}
}

func TestExportRetainsReportAfterBenchmarkRemoval(t *testing.T) {
	snapshot := snapshotFixture()
	snapshot.Results[0].Benchmark = &protocol.BenchmarkReport{Kind: "removed-benchmark", Data: json.RawMessage(`{"schema_version":"removed/v1","value":7}`)}
	server := fixtureServer(t, snapshot, false)
	defer server.Close()
	root := filepath.Join(t.TempDir(), "export")
	if _, err := Write(context.Background(), Options{NodeID: "node-1", Output: root, ControllerURL: server.URL, AdminToken: "secret-test-token"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "results.json"))
	if err != nil || !strings.Contains(string(data), `"removed-benchmark"`) || !strings.Contains(string(data), `"value": 7`) {
		t.Fatal("historical payload was lost", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(root, "removed-benchmark-results.csv")); !os.IsNotExist(err) {
		t.Fatal("export invoked a removed formatter", err)
	}
}
func fixtureServer(t *testing.T, snapshot controller.NodeExport, promError bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/nodes/node-1/export":
			if r.Header.Get("X-ModelGauge-Admin-Token") != "secret-test-token" {
				t.Error("missing admin authentication")
			}
			_ = json.NewEncoder(w).Encode(snapshot)
		case "/metrics":
			if r.Header.Get("X-ModelGauge-Admin-Token") != "" {
				t.Error("unnecessary credentials on public metrics")
			}
			_, _ = fmt.Fprint(w, "# TYPE modelgauge_controller_up gauge\nmodelgauge_controller_up 1\n")
		case "/api/v1/query_range":
			if r.Header.Get("X-ModelGauge-Admin-Token") != "" {
				t.Error("controller token leaked to Prometheus")
			}
			if r.Header.Get("Authorization") != "Bearer prometheus-secret" {
				t.Error("missing distinct Prometheus credential")
			}
			if r.URL.Query().Get("start") == "" || r.URL.Query().Get("step") != "30" || r.URL.Query().Get("query") == "" {
				t.Error("invalid range request")
			}
			if promError {
				_, _ = fmt.Fprint(w, `{"status":"error","error":"bad query"}`)
			} else {
				_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]},"warnings":["test warning"]}`)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}
func TestBundleCSVHistoryAndChecksums(t *testing.T) {
	server := fixtureServer(t, snapshotFixture(), false)
	defer server.Close()
	output := filepath.Join(t.TempDir(), "export")
	opts := Options{NodeID: "node-1", Output: output, ControllerURL: server.URL, AdminToken: "secret-test-token", PrometheusURL: server.URL, PrometheusToken: "prometheus-secret", Start: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC), Step: 30 * time.Second}
	manifest, err := Write(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.History) != 4 || len(manifest.Warnings) < 3 || manifest.MetricsScope == "" {
		t.Fatal("export scope/empty history was not recorded")
	}
	if _, err = os.Stat(filepath.Join(output, "INCOMPLETE")); !os.IsNotExist(err) {
		t.Fatal("completed bundle marked incomplete")
	}
	data, _ := os.ReadFile(filepath.Join(output, "results.csv"))
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1][3] != "'=SUM(1,2)" || rows[2][7] != "" {
		t.Fatal("CSV lost failure record or allowed formula injection")
	}
	sums, _ := os.ReadFile(filepath.Join(output, "SHA256SUMS"))
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		data, e := os.ReadFile(filepath.Join(output, parts[1]))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != parts[0] {
			t.Fatal("checksum mismatch")
		}
	}
	all, _ := json.Marshal(manifest)
	if strings.Contains(string(all), "secret-test-token") || strings.Contains(string(all), "prometheus-secret") {
		t.Fatal("credentials persisted in manifest")
	}
	if _, err = Write(context.Background(), opts); err == nil {
		t.Fatal("existing output overwritten")
	}
}
func TestRawBundleAndFailureMarkers(t *testing.T) {
	data := []byte(`{"samples_ts":[25]}`)
	r := protocol.EvidenceRef{RunID: "run-1", Name: "case.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	snapshot := snapshotFixture()
	snapshot.Artifacts = []protocol.EvidenceRef{r}
	server := fixtureServer(t, snapshot, false)
	defer server.Close()
	source := t.TempDir()
	_ = os.Mkdir(filepath.Join(source, r.RunID), 0700)
	path := filepath.Join(source, r.RunID, r.Name)
	_ = os.WriteFile(path, data, 0600)
	opts := Options{NodeID: "node-1", Output: filepath.Join(t.TempDir(), "raw"), ControllerURL: server.URL, AdminToken: "secret-test-token", RawSource: LocalEvidence(source)}
	m, err := Write(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !m.RawIncluded || m.RawFileCount != 1 {
		t.Fatal("raw completeness incorrect")
	}
	_ = os.WriteFile(path, []byte("corrupted"), 0600)
	opts.Output = filepath.Join(t.TempDir(), "failed")
	if _, err = Write(context.Background(), opts); err == nil {
		t.Fatal("corrupt raw evidence accepted")
	}
	if _, err = os.Stat(filepath.Join(opts.Output, "INCOMPLETE")); err != nil {
		t.Fatal("partial failure not marked")
	}
	if _, err = os.Stat(filepath.Join(opts.Output, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("failed export looks finalized")
	}
}
func TestFailedHistoryAndInvalidRange(t *testing.T) {
	server := fixtureServer(t, snapshotFixture(), true)
	defer server.Close()
	o := Options{NodeID: "node-1", Output: filepath.Join(t.TempDir(), "failure"), ControllerURL: server.URL, AdminToken: "secret-test-token", PrometheusURL: server.URL, PrometheusToken: "prometheus-secret", Start: time.Now(), End: time.Now().Add(time.Hour), Step: 30 * time.Second}
	if _, err := Write(context.Background(), o); err == nil {
		t.Fatal("failed Prometheus query accepted")
	}
	o.End = o.Start.Add(365 * 24 * time.Hour)
	if err := validate(o); err == nil {
		t.Fatal("excessive range accepted")
	}
	o.PrometheusURL = ""
	if err := validate(o); err == nil {
		t.Fatal("history flags silently ignored")
	}
}

func TestSelectedRunExportRequiresControllerAcknowledgement(t *testing.T) {
	snapshot := snapshotFixture()
	requested := "run-1"
	for _, honor := range []bool{true, false} {
		snapshot.SelectedRun = ""
		if honor {
			snapshot.SelectedRun = requested
		}
		var selected string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/nodes/node-1/export" {
				selected = r.URL.Query().Get("run")
				_ = json.NewEncoder(w).Encode(snapshot)
				return
			}
			fmt.Fprint(w, "# TYPE modelgauge_controller_up gauge\nmodelgauge_controller_up 1\n")
		}))
		root := filepath.Join(t.TempDir(), "export")
		manifest, err := Write(context.Background(), Options{NodeID: "node-1", RunID: requested, Output: root, ControllerURL: server.URL, AdminToken: "test"})
		server.Close()
		if selected != requested {
			t.Fatal("run selection not sent", selected)
		}
		if honor && (err != nil || manifest.SelectedRun != requested) {
			t.Fatal(manifest, err)
		}
		if !honor && err == nil {
			t.Fatal("old controller silently ignored run filter")
		}
	}
}

func TestOversizedHistoryCanBeExportedOneRunAtATime(t *testing.T) {
	snapshot := snapshotFixture()
	snapshot.SelectedRun = "run-1"
	snapshot.Results = snapshot.Results[:1]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			fmt.Fprint(w, "# TYPE modelgauge_controller_up gauge\nmodelgauge_controller_up 1\n")
			return
		}
		if r.URL.Query().Get("run") == "run-1" {
			_ = json.NewEncoder(w).Encode(snapshot)
			return
		}
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 65; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	options := Options{NodeID: "node-1", Output: filepath.Join(t.TempDir(), "all"), ControllerURL: server.URL, AdminToken: "test"}
	if _, err := Write(context.Background(), options); err == nil || !strings.Contains(err.Error(), "--run RUN_ID") {
		t.Fatal("missing bounded-export guidance", err)
	}
	options.RunID = "run-1"
	options.Output = filepath.Join(t.TempDir(), "one")
	if _, err := Write(context.Background(), options); err != nil {
		t.Fatal(err)
	}
}
