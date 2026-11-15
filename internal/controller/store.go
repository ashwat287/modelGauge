// Package controller stores configuration and completed measurements as files.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

var ErrNotFound = errors.New("not found")

type Store struct{ directory string }
type Node struct {
	ID            string                 `json:"node_id"`
	DisplayName   string                 `json:"display_name"`
	Kind          string                 `json:"node_kind"`
	ExecutionMode protocol.ExecutionMode `json:"execution_mode"`
	Hardware      map[string]any         `json:"hardware"`
	AgentVersion  string                 `json:"agent_version"`
	Status        string                 `json:"status,omitempty"`
	LastSeenAt    *time.Time             `json:"last_seen_at,omitempty"`
}
type NodeModel struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Path      string `json:"path"`
}
type Record struct {
	Document benchmark.Document `json:"document"`
	Plan     protocol.Plan      `json:"plan"`
	Result   protocol.Result    `json:"result"`
}
type Experiment struct {
	Name          string             `json:"name"`
	Digest        string             `json:"spec_sha256"`
	Specification benchmark.Document `json:"specification"`
	CreatedAt     *time.Time         `json:"created_at,omitempty"`
	Plans         []ExperimentPlan   `json:"plans"`
}
type ExperimentPlan struct {
	CompletedAt   *time.Time    `json:"completed_at,omitempty"`
	Error         string        `json:"error,omitempty"`
	PlanID        string        `json:"plan_id,omitempty"`
	Status        string        `json:"status"`
	Specification protocol.Plan `json:"specification"`
}

func Open(_ context.Context, directory string) (*Store, error) {
	if directory == "" {
		return nil, errors.New("controller data directory is required")
	}
	if err := os.MkdirAll(filepath.Join(directory, "results"), 0700); err != nil {
		return nil, err
	}
	return &Store{directory: directory}, nil
}
func (s *Store) SaveNode(node Node) error {
	return platform.WriteJSON(filepath.Join(s.directory, "node.json"), node, 0600)
}
func (s *Store) ListNodes(_ context.Context) ([]Node, error) {
	nodes := []Node{}
	data, err := os.ReadFile(filepath.Join(s.directory, "node.json"))
	if os.IsNotExist(err) {
		return nodes, nil
	}
	if err != nil {
		return nil, err
	}
	var node Node
	if err = json.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	return append(nodes, node), nil
}
func (s *Store) Save(record Record) error {
	if record.Result.Status != protocol.RunStatusCompleted {
		return errors.New("only completed measurements are retained")
	}
	if !regexp.MustCompile(`^run-[a-zA-Z0-9_-]+$`).MatchString(record.Result.RunID) {
		return errors.New("invalid run ID")
	}
	if err := benchmark.ValidateResult(record.Result); err != nil {
		return err
	}
	return platform.WriteJSON(filepath.Join(s.directory, "results", record.Result.RunID+".json"), record, 0600)
}
func (s *Store) Records() ([]Record, error) {
	entries, err := os.ReadDir(filepath.Join(s.directory, "results"))
	if err != nil {
		return nil, err
	}
	records := []Record{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.directory, "results", entry.Name()))
		if err != nil {
			return nil, err
		}
		var record Record
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("read completed result %s: %w", entry.Name(), err)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Result.CompletedAt.After(records[j].Result.CompletedAt) })
	return records, nil
}
func (r Record) Experiment() Experiment {
	return Experiment{Name: r.Result.RunID, Digest: r.Document.Digest(), Specification: r.Document, CreatedAt: &r.Result.StartedAt, Plans: []ExperimentPlan{{PlanID: r.Plan.PlanID, Status: string(r.Result.Status), Specification: r.Plan, CompletedAt: &r.Result.CompletedAt}}}
}
func (s *Store) Experiments(_ context.Context) ([]Experiment, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	out := []Experiment{}
	for _, r := range records {
		out = append(out, r.Experiment())
	}
	return out, nil
}
func (s *Store) Experiment(_ context.Context, name string) (Experiment, error) {
	records, err := s.Records()
	if err != nil {
		return Experiment{}, err
	}
	for _, r := range records {
		if name == "current" || name == r.Result.RunID || name == r.Document.Metadata.Name {
			return r.Experiment(), nil
		}
	}
	return Experiment{}, ErrNotFound
}
