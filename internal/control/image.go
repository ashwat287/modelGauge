package control

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

func buildImage(ctx context.Context, name string, log io.Writer) (string, error) {
	arch, err := capture(ctx, name, "uname -m")
	if err != nil {
		return "", err
	}
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	default:
		return "", fmt.Errorf("unsupported VM architecture %s", arch)
	}
	binary, err := nodeBinary(arch)
	if err != nil {
		return "", err
	}
	payloads := map[string][]byte{"usr/local/bin/modelgauge": binary}
	for _, target := range []string{"amd64", "arm64"} {
		worker, err := nodeBinary(target)
		if err != nil {
			return "", err
		}
		helper, err := macBinary(target)
		if err != nil {
			return "", err
		}
		payloads["opt/modelgauge/runtimes/linux-"+target] = worker
		payloads["opt/modelgauge/runtimes/darwin-"+target] = helper
	}
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	names := make([]string, 0, len(payloads))
	for name := range payloads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		payload := payloads[name]
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(payload))}); err != nil {
			return "", err
		}
		if _, err = tw.Write(payload); err != nil {
			return "", err
		}
	}
	if err = tw.Close(); err != nil {
		return "", err
	}
	digest := func(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
	descriptor := func(media string, data []byte) map[string]any {
		return map[string]any{"mediaType": media, "digest": digest(data), "size": len(data)}
	}
	config, _ := json.Marshal(map[string]any{"architecture": arch, "os": "linux", "config": map[string]any{"Entrypoint": []string{"/usr/local/bin/modelgauge"}, "User": "65532:65532"}, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{digest(layer.Bytes())}}})
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": descriptor("application/vnd.oci.image.config.v1+json", config), "layers": []any{descriptor("application/vnd.oci.image.layer.v1.tar", layer.Bytes())}})
	// Content-addressed tags make repeat applies idempotent and ensure upgrades
	// schedule the intended image without relying on a mutable local tag.
	image := fmt.Sprintf("docker.io/library/modelgauge:bootstrap-%x", sha256.Sum256(layer.Bytes()))
	md := descriptor("application/vnd.oci.image.manifest.v1+json", manifest)
	md["annotations"] = map[string]string{"org.opencontainers.image.ref.name": image}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{md}})
	var archive bytes.Buffer
	tw = tar.NewWriter(&archive)
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`), "index.json": index, "blobs/sha256/" + strings.TrimPrefix(digest(config), "sha256:"): config, "blobs/sha256/" + strings.TrimPrefix(digest(manifest), "sha256:"): manifest, "blobs/sha256/" + strings.TrimPrefix(digest(layer.Bytes()), "sha256:"): layer.Bytes()}
	for path, data := range files {
		if err = tw.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(data))}); err != nil {
			return "", err
		}
		if _, err = tw.Write(data); err != nil {
			return "", err
		}
	}
	if err = tw.Close(); err != nil {
		return "", err
	}
	if err = shell(ctx, name, "sudo k3s ctr -n k8s.io images import -", &archive, log); err != nil {
		return "", err
	}
	return image, nil
}
