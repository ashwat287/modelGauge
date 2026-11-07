// Package profile generates reproducible benchmark plans from named profiles.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

const StandardCPU = "standard-cpu"

type Options struct {
	NodeID        string
	ExecutionMode protocol.ExecutionMode
	ModelPath     string
	ModelID       string
	ModelSHA256   string
	Binary        string
	Threads       []int
	Contexts      []int
	Warmups       int
	Repetitions   int
}

func Names() []string { return []string{StandardCPU} }

func StandardCPUPlan(options Options) (protocol.Plan, error) {
	if options.NodeID == "" || options.ModelPath == "" {
		return protocol.Plan{}, fmt.Errorf("node ID and model path are required")
	}
	if options.ExecutionMode == "" {
		options.ExecutionMode = protocol.ExecutionModeNative
	}
	if options.ModelID == "" {
		options.ModelID = trimExtension(filepath.Base(options.ModelPath))
	}
	if len(options.Threads) == 0 {
		options.Threads = []int{1, 2}
	}
	if len(options.Contexts) == 0 {
		options.Contexts = []int{512, 4096, 8192}
	}
	if options.Warmups == 0 {
		options.Warmups = 3
	}
	if options.Repetitions == 0 {
		options.Repetitions = 5
	}
	threads, contexts := uniqueSorted(options.Threads), uniqueSorted(options.Contexts)
	for _, value := range append(append([]int{}, threads...), contexts...) {
		if value < 1 {
			return protocol.Plan{}, fmt.Errorf("threads and contexts must be positive")
		}
	}
	digest := options.ModelSHA256
	if digest == "" {
		var err error
		digest, err = sha256File(options.ModelPath)
		if err != nil {
			return protocol.Plan{}, err
		}
	}
	cases := make([]protocol.Case, 0, len(threads)*(len(contexts)+1))
	for _, contextTokens := range contexts {
		for _, threads := range threads {
			cases = append(cases, protocol.Case{ID: fmt.Sprintf("prompt-%d-t%d", contextTokens, threads), Threads: threads, PromptTokens: contextTokens})
		}
	}
	for _, threads := range threads {
		cases = append(cases, protocol.Case{ID: fmt.Sprintf("generation-128-t%d", threads), Threads: threads, GenerationTokens: 128})
	}
	maxContext := contexts[len(contexts)-1]
	return protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: options.NodeID, ExecutionMode: options.ExecutionMode, Runtime: protocol.Runtime{Kind: "llama.cpp", Binary: options.Binary}, Model: protocol.Model{ID: options.ModelID, Path: options.ModelPath, SHA256: digest}, Defaults: protocol.Defaults{WarmupRepetitions: options.Warmups, MeasureRepetitions: options.Repetitions, GPULayers: 0}, Cases: cases, MemoryCase: protocol.MemoryCase{ID: fmt.Sprintf("memory-p%d-g128-t%d", maxContext, threads[len(threads)-1]), Threads: threads[len(threads)-1], PromptTokens: maxContext, GenerationTokens: 128}}, nil
}

func uniqueSorted(values []int) []int {
	seen := map[int]bool{}
	for _, value := range values {
		seen[value] = true
	}
	result := make([]int, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}
func trimExtension(value string) string {
	extension := filepath.Ext(value)
	return value[:len(value)-len(extension)]
}
func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open model: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
