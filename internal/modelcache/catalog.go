package modelcache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The initial catalog deliberately uses the model publisher's GGUF repository.
const catalogRepository = "Qwen/Qwen2.5-1.5B-Instruct-GGUF"

type CatalogEntry struct {
	ID           string `json:"id"`
	Quantization string `json:"quantization"`
	Repository   string `json:"repository"`
	Revision     string `json:"revision"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	URL          string `json:"url"`
}

func Search(query string) ([]CatalogEntry, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Get("https://huggingface.co/api/models/" + catalogRepository + "?blobs=true")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog metadata: %s", response.Status)
	}
	var metadata struct {
		SHA      string `json:"sha"`
		Siblings []struct {
			Filename string `json:"rfilename"`
			LFS      *struct {
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		return nil, err
	}
	entries := []CatalogEntry{}
	for _, quant := range []string{"q4_k_m", "q5_k_m", "q8_0"} {
		filename := "qwen2.5-1.5b-instruct-" + quant + ".gguf"
		for _, file := range metadata.Siblings {
			if !strings.EqualFold(file.Filename, filename) || file.LFS == nil || len(file.LFS.SHA256) != 64 {
				continue
			}
			entry := CatalogEntry{ID: "qwen2.5-1.5b-instruct:" + quant, Quantization: quant, Repository: catalogRepository, Revision: metadata.SHA, SizeBytes: file.LFS.Size, SHA256: file.LFS.SHA256, URL: "https://huggingface.co/" + catalogRepository + "/resolve/" + url.PathEscape(metadata.SHA) + "/" + url.PathEscape(file.Filename)}
			if strings.Contains(strings.ToLower(entry.ID), strings.ToLower(query)) {
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}

func Resolve(id string) (CatalogEntry, error) {
	entries, err := Search(id)
	if err != nil {
		return CatalogEntry{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return CatalogEntry{}, fmt.Errorf("unknown model %q; use modelgauge model search", id)
}

func ValidateID(id string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`).MatchString(id) {
		return fmt.Errorf("invalid model ID: use letters, digits, dots, underscores, colons, or hyphens")
	}
	return nil
}
