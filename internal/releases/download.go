// Package releases resolves matching, checksum-verified release components.
package releases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
)

const Schema = "modelgauge-release/v1"

type Asset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size_bytes"`
}
type Manifest struct {
	Schema          string  `json:"schema_version"`
	Version         string  `json:"version"`
	Commit          string  `json:"commit"`
	Repository      string  `json:"repository"`
	Date            string  `json:"date"`
	ControllerImage string  `json:"controller_image"`
	Assets          []Asset `json:"assets"`
}

func BaseURL() (string, error) {
	base := os.Getenv("MODELGAUGE_RELEASE_BASE_URL")
	if base == "" {
		if !buildinfo.Released() {
			return "", fmt.Errorf("development build needs --source; install a versioned release to bootstrap without Go")
		}
		base = "https://github.com/" + buildinfo.Repository + "/releases/download/" + buildinfo.Version
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
		return "", fmt.Errorf("release downloads require HTTPS (loopback HTTP is allowed for local tests)")
	}
	if parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("invalid release base URL")
	}
	return strings.TrimRight(base, "/"), nil
}
func get(ctx context.Context, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many release redirects")
		}
		if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && (req.URL.Hostname() == "localhost" || req.URL.Hostname() == "127.0.0.1")) {
			return fmt.Errorf("release redirect requires HTTPS")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("release download returned %s", response.Status)
	}
	return response, nil
}
func Load(ctx context.Context) (Manifest, error) {
	base, err := BaseURL()
	if err != nil {
		return Manifest{}, err
	}
	response, err := get(ctx, base+"/release.json")
	if err != nil {
		return Manifest{}, err
	}
	defer response.Body.Close()
	var m Manifest
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&m); err != nil {
		return m, err
	}
	if m.Schema != Schema || m.Version != buildinfo.Version || m.Repository != buildinfo.Repository || m.Commit != buildinfo.Commit {
		return m, fmt.Errorf("release metadata does not match this CLI's version, repository, or commit")
	}
	prefix := "ghcr.io/" + strings.ToLower(buildinfo.Repository) + "@sha256:"
	if !strings.HasPrefix(m.ControllerImage, prefix) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(strings.TrimPrefix(m.ControllerImage, prefix)) {
		return m, fmt.Errorf("controller image must be a digest-pinned image for this release repository")
	}
	return m, nil
}
func DownloadAgent(ctx context.Context, arch string) ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return downloadAgent(ctx, arch, filepath.Join(home, ".cache", "modelgauge", "releases"))
}

func downloadAgent(ctx context.Context, arch, root string) ([]byte, error) {
	manifest, err := Load(ctx)
	if err != nil {
		return nil, err
	}
	var selected *Asset
	for index := range manifest.Assets {
		asset := &manifest.Assets[index]
		if asset.OS == "linux" && asset.Arch == arch {
			if selected != nil {
				return nil, fmt.Errorf("duplicate agent asset")
			}
			selected = asset
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("release has no Linux %s agent", arch)
	}
	if selected.Name != "modelgauge_linux_"+arch || selected.Size < 1 || selected.Size > 128<<20 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(selected.SHA256) {
		return nil, fmt.Errorf("invalid agent asset metadata")
	}
	cache := filepath.Join(root, buildinfo.Version, selected.SHA256)
	verify := func(data []byte) bool {
		hash := sha256.Sum256(data)
		return int64(len(data)) == selected.Size && hex.EncodeToString(hash[:]) == selected.SHA256
	}
	if data, err := os.ReadFile(cache); err == nil && verify(data) {
		return data, nil
	}
	base, err := BaseURL()
	if err != nil {
		return nil, err
	}
	response, err := get(ctx, base+"/"+selected.Name)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, selected.Size+1))
	if err != nil {
		return nil, err
	}
	if !verify(data) {
		return nil, fmt.Errorf("agent checksum or size mismatch; refusing installation")
	}
	if err = os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(cache), ".download-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err = os.Rename(file.Name(), cache); err != nil {
		return nil, err
	}
	return data, nil
}
