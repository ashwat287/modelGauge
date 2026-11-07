package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// localImage is only a test fixture: production uses Dockerfile.release with
// distroless, CA certificates, a non-root user, and both Linux architectures.
func localImage(output, arch, repository string) (string, error) {
	binary, err := os.ReadFile(filepath.Join(output, "modelgauge_linux_"+arch))
	if err != nil {
		return "", err
	}
	var layer bytes.Buffer
	writer := tar.NewWriter(&layer)
	if err = writer.WriteHeader(&tar.Header{Name: "usr/local/bin/modelgauge", Size: int64(len(binary)), Mode: 0755}); err != nil {
		return "", err
	}
	if _, err = writer.Write(binary); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	digest := func(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
	descriptor := func(media string, data []byte) map[string]any {
		return map[string]any{"mediaType": media, "digest": digest(data), "size": len(data)}
	}
	config, _ := json.Marshal(map[string]any{"architecture": arch, "os": "linux", "config": map[string]any{"Entrypoint": []string{"/usr/local/bin/modelgauge"}}, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{digest(layer.Bytes())}}})
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": descriptor("application/vnd.oci.image.config.v1+json", config), "layers": []any{descriptor("application/vnd.oci.image.layer.v1.tar", layer.Bytes())}})
	reference := "ghcr.io/" + strings.ToLower(repository) + "@" + digest(manifest)
	entry := descriptor("application/vnd.oci.image.manifest.v1+json", manifest)
	entry["annotations"] = map[string]string{"org.opencontainers.image.ref.name": reference}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{entry}})
	file, err := os.OpenFile(filepath.Join(output, "controller_linux_"+arch+".oci.tar"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer file.Close()
	writer = tar.NewWriter(file)
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`), "index.json": index, "blobs/sha256/" + strings.TrimPrefix(digest(config), "sha256:"): config, "blobs/sha256/" + strings.TrimPrefix(digest(manifest), "sha256:"): manifest, "blobs/sha256/" + strings.TrimPrefix(digest(layer.Bytes()), "sha256:"): layer.Bytes()}
	for path, data := range files {
		if err = writer.WriteHeader(&tar.Header{Name: path, Size: int64(len(data)), Mode: 0644}); err != nil {
			return "", err
		}
		if _, err = writer.Write(data); err != nil {
			return "", err
		}
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	return reference, file.Sync()
}
