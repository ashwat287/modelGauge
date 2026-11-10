// Package modelcache owns discovery of GGUF models on a benchmark node.
package modelcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Model struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Path      string `json:"-"`
}

func ManagedDir(home string) string {
	return filepath.Join(home, ".local", "share", "modelgauge", "models")
}

type inventoryEntry struct {
	Size     int64  `json:"size"`
	Modified int64  `json:"modified_ns"`
	SHA256   string `json:"sha256"`
}

func Discover() ([]Model, error) { return DiscoverContext(context.Background()) }

func DiscoverContext(ctx context.Context) ([]Model, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(home, ".local", "share", "modelgauge", "model-inventory.json")
	cached := map[string]inventoryEntry{}
	if data, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(data, &cached)
	}
	if cached == nil {
		cached = map[string]inventoryEntry{}
	}
	next := map[string]inventoryEntry{}
	seen := map[string]bool{}
	models := []Model{}
	changed := false
	for _, directory := range []string{ManagedDir(home), filepath.Join(home, "models"), filepath.Join(home, "benchmark-work", "models")} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !strings.EqualFold(filepath.Ext(entry.Name()), ".gguf") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if seen[id] {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			seen[id] = true
			fingerprint := inventoryEntry{Size: info.Size(), Modified: info.ModTime().UnixNano()}
			prior, ok := cached[path]
			if ok && prior.Size == fingerprint.Size && prior.Modified == fingerprint.Modified && validInventorySHA(prior.SHA256) {
				fingerprint.SHA256 = prior.SHA256
			} else {
				fingerprint.SHA256, err = checksumContext(ctx, path)
				if err != nil {
					return nil, err
				}
				after, err := os.Stat(path)
				if err != nil {
					return nil, err
				}
				if !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
					return nil, fmt.Errorf("model changed during inventory scan: %s", id)
				}
				changed = true
			}
			next[path] = fingerprint
			models = append(models, Model{ID: id, SHA256: fingerprint.SHA256, SizeBytes: info.Size(), Path: path})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if changed || len(next) != len(cached) {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
			return nil, err
		}
		if err := platform.WriteJSON(cachePath, next, 0600); err != nil {
			return nil, err
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// Inventory fingerprints avoid repeated scans; execution still verifies the full model checksum.
func validInventorySHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func checksum(path string) (string, error) { return checksumContext(context.Background(), path) }
func checksumContext(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx, file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func DiscoverLlamaBench() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	managed := filepath.Join(home, ".local", "share", "modelgauge", "runtime", "current", "llama-bench")
	if info, err := os.Stat(managed); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
		return managed, nil
	}
	if binary, err := exec.LookPath("llama-bench"); err == nil {
		return binary, nil
	}
	path := filepath.Join(home, "benchmark-work", "src", "llama.cpp", "build", "bin", "llama-bench")
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return path, nil
	}
	return "", os.ErrNotExist
}

func Install(id, sourceURL, expectedSHA string) (Model, error) {
	return InstallContext(context.Background(), id, sourceURL, expectedSHA)
}

func InstallContext(ctx context.Context, id, sourceURL, expectedSHA string) (Model, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Model{}, err
	}
	return installAt(ctx, id, sourceURL, expectedSHA, ManagedDir(home))
}

func installAt(ctx context.Context, id, sourceURL, expectedSHA, directory string) (Model, error) {
	unlock, err := platform.Lock("execution")
	if err != nil {
		return Model{}, err
	}
	defer unlock()
	if err := ValidateID(id); err != nil {
		return Model{}, err
	}
	if id == "" || sourceURL == "" {
		return Model{}, os.ErrInvalid
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return Model{}, err
	}
	finalPath := filepath.Join(directory, id+".gguf")
	if expectedSHA != "" {
		if digest, err := checksumContext(ctx, finalPath); err == nil && strings.EqualFold(digest, expectedSHA) {
			info, err := os.Stat(finalPath)
			if err != nil {
				return Model{}, err
			}
			return Model{ID: id, SHA256: digest, SizeBytes: info.Size(), Path: finalPath}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return Model{}, err
	}
	partialPath := finalPath + ".part"
	if expectedSHA != "" {
		if digest, err := checksumContext(ctx, partialPath); err == nil && strings.EqualFold(digest, expectedSHA) {
			if err = os.Rename(partialPath, finalPath); err != nil {
				return Model{}, err
			}
			info, err := os.Stat(finalPath)
			if err != nil {
				return Model{}, err
			}
			return Model{ID: id, SHA256: digest, SizeBytes: info.Size(), Path: finalPath}, nil
		}
	}
	identity := struct {
		SourceSHA256   string `json:"source_sha256"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}{SourceSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(sourceURL))), ExpectedSHA256: expectedSHA}
	identityPath := partialPath + ".meta.json"
	if info, e := os.Stat(partialPath); e == nil && info.Size() > 0 {
		data, e := os.ReadFile(identityPath)
		var previous struct {
			SourceSHA256   string `json:"source_sha256"`
			ExpectedSHA256 string `json:"expected_sha256"`
		}
		if e != nil || json.Unmarshal(data, &previous) != nil || previous.SourceSHA256 != identity.SourceSHA256 || previous.ExpectedSHA256 != identity.ExpectedSHA256 {
			return Model{}, fmt.Errorf("partial download source identity is missing or differs; preserve it and use a new model ID or inspect the partial data")
		}
	}
	if err := platform.WriteJSON(identityPath, identity, 0600); err != nil {
		return Model{}, err
	}
	var offset int64
	if info, err := os.Stat(partialPath); err == nil {
		offset = info.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return Model{}, err
	}
	if expectedSHA == "" {
		offset = 0
	} // A URL alone cannot prove the identity of a resumed representation.
	if offset > 0 {
		req.Header.Set("Range", "bytes="+fmt.Sprint(offset)+"-")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return Model{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return Model{}, fmt.Errorf("download model: %s", response.Status)
	}
	if response.StatusCode == http.StatusOK {
		offset = 0
	}
	if response.StatusCode == http.StatusPartialContent {
		var start int64
		if _, err = fmt.Sscanf(response.Header.Get("Content-Range"), "bytes %d-", &start); err != nil || start != offset {
			return Model{}, fmt.Errorf("invalid resumed download range")
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	output, err := os.OpenFile(partialPath, flags, 0o640)
	if err != nil {
		return Model{}, err
	}
	_, copyErr := io.Copy(output, response.Body)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return Model{}, copyErr
	}
	if syncErr != nil {
		return Model{}, syncErr
	}
	if closeErr != nil {
		return Model{}, closeErr
	}
	digest, err := checksumContext(ctx, partialPath)
	if err != nil {
		return Model{}, err
	}
	if expectedSHA != "" && !strings.EqualFold(expectedSHA, digest) {
		_ = os.Rename(partialPath, partialPath+".checksum-failed")
		return Model{}, fmt.Errorf("SHA-256 mismatch for %s", id)
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		return Model{}, err
	}
	_ = os.Remove(identityPath)
	info, err := os.Stat(finalPath)
	if err != nil {
		return Model{}, err
	}
	return Model{ID: id, SHA256: digest, SizeBytes: info.Size(), Path: finalPath}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
