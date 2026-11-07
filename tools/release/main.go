// Command release builds matching, source-independent distribution artifacts.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/internal/releases"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	version := flag.String("version", "", "release tag, e.g. v0.1.0")
	repository := flag.String("repository", buildinfo.Repository, "GitHub owner/repository")
	commit := flag.String("commit", "", "source commit (default: Git HEAD)")
	date := flag.String("date", "", "UTC build date (default: commit time)")
	output := flag.String("output", "dist", "artifact directory")
	image := flag.String("image", "", "digest-pinned ghcr.io controller image")
	finalize := flag.Bool("finalize", false, "add the published image digest and regenerate checksums without rebuilding")
	snapshot := flag.Bool("snapshot", false, "allow a local, unpublished build without a committed checkout")
	localArch := flag.String("local-controller-arch", "", "snapshot only: create a local OCI controller image for empty-directory validation")
	flag.Parse()
	if *finalize {
		return finish(*output, *image)
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`).MatchString(*version) {
		return fmt.Errorf("--version must be a semantic release tag")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(*repository) {
		return fmt.Errorf("invalid GitHub repository")
	}
	if *commit == "" {
		data, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err == nil {
			*commit = strings.TrimSpace(string(data))
		}
	}
	if !*snapshot {
		if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(*commit) {
			return fmt.Errorf("release builds require a committed 40-character source SHA")
		}
		head, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(head)) != *commit {
			return fmt.Errorf("--commit does not match the checked-out source")
		}
		data, err := exec.Command("git", "status", "--porcelain").Output()
		if err != nil {
			return err
		}
		if len(data) > 0 {
			return fmt.Errorf("release checkout is dirty; use --snapshot for local validation")
		}
	} else if *commit == "" {
		*commit = "uncommitted"
	}
	if *localArch != "" && (!*snapshot || (*localArch != "amd64" && *localArch != "arm64")) {
		return fmt.Errorf("local controller image requires --snapshot and an amd64/arm64 architecture")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(*commit) {
		return fmt.Errorf("invalid source commit")
	}
	if *date == "" {
		data, err := exec.Command("git", "show", "-s", "--format=%cI", *commit).Output()
		if err == nil {
			*date = strings.TrimSpace(string(data))
		} else if *snapshot {
			*date = time.Now().UTC().Format(time.RFC3339)
		} else {
			return fmt.Errorf("determine source date: %w", err)
		}
	}
	if _, err := time.Parse(time.RFC3339, *date); err != nil {
		return err
	}
	if err := os.MkdirAll(*output, 0750); err != nil {
		return err
	}
	// Refuse overwriting old release artifacts, which can otherwise produce a
	// manifest mixing binaries from different source revisions.
	if entries, err := os.ReadDir(*output); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("artifact directory is not empty; choose a new --output directory")
	}
	channel := "release"
	if *snapshot {
		channel = "snapshot"
	}
	manifest := releases.Manifest{Schema: releases.Schema, Version: *version, Repository: *repository, Commit: *commit, Date: *date, ControllerImage: *image}
	values := map[string]string{"Version": *version, "Commit": *commit, "Date": *date, "Repository": *repository, "Channel": channel}
	flags := []string{"-s", "-w"}
	for _, name := range []string{"Version", "Commit", "Date", "Repository", "Channel"} {
		flags = append(flags, "-X", "github.com/nerdylucifer/modelgauge/internal/buildinfo."+name+"="+values[name])
	}
	for _, target := range []struct{ os, arch string }{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name := "modelgauge_" + target.os + "_" + target.arch
		path := filepath.Join(*output, name)
		command := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", strings.Join(flags, " "), "-o", path, "./cmd/modelgauge")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "GOOS=") && !strings.HasPrefix(value, "GOARCH=") && !strings.HasPrefix(value, "CGO_ENABLED=") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		fmt.Println("Building", name)
		if err := command.Run(); err != nil {
			return err
		}
		asset, err := describe(path, target.os, target.arch)
		if err != nil {
			return err
		}
		manifest.Assets = append(manifest.Assets, asset)
	}
	if *localArch != "" {
		reference, err := localImage(*output, *localArch, *repository)
		if err != nil {
			return err
		}
		manifest.ControllerImage = reference
	}
	if err := platform.WriteJSON(filepath.Join(*output, "release.json"), manifest, 0644); err != nil {
		return err
	}
	if manifest.ControllerImage != "" {
		if err := validateImage(manifest); err != nil {
			return err
		}
	}
	return checksums(*output, manifest)
}
func describe(path, goos, arch string) (releases.Asset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return releases.Asset{}, err
	}
	hash := sha256.Sum256(data)
	return releases.Asset{Name: filepath.Base(path), OS: goos, Arch: arch, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}, nil
}
func validateImage(m releases.Manifest) error {
	prefix := "ghcr.io/" + strings.ToLower(m.Repository) + "@sha256:"
	if !strings.HasPrefix(m.ControllerImage, prefix) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(strings.TrimPrefix(m.ControllerImage, prefix)) {
		return fmt.Errorf("--image must be ghcr.io/%s@sha256:DIGEST", strings.ToLower(m.Repository))
	}
	return nil
}
func finish(output, image string) error {
	data, err := os.ReadFile(filepath.Join(output, "release.json"))
	if err != nil {
		return err
	}
	var manifest releases.Manifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest.Schema != releases.Schema {
		return fmt.Errorf("invalid release manifest")
	}
	for _, asset := range manifest.Assets {
		if asset.Name != "modelgauge_"+asset.OS+"_"+asset.Arch {
			return fmt.Errorf("invalid asset filename")
		}
		actual, err := describe(filepath.Join(output, asset.Name), asset.OS, asset.Arch)
		if err != nil {
			return err
		}
		if actual.SHA256 != asset.SHA256 || actual.Size != asset.Size {
			return fmt.Errorf("artifact changed before publication: %s", asset.Name)
		}
	}
	manifest.ControllerImage = image
	if err = validateImage(manifest); err != nil {
		return err
	}
	if err = platform.WriteJSON(filepath.Join(output, "release.json"), manifest, 0644); err != nil {
		return err
	}
	return checksums(output, manifest)
}
func checksums(output string, m releases.Manifest) error {
	var lines strings.Builder
	for _, asset := range m.Assets {
		fmt.Fprintf(&lines, "%s  %s\n", asset.SHA256, asset.Name)
	}
	data, err := os.ReadFile(filepath.Join(output, "release.json"))
	if err != nil {
		return err
	}
	fmt.Fprintf(&lines, "%x  release.json\n", sha256.Sum256(data))
	return os.WriteFile(filepath.Join(output, "SHA256SUMS"), []byte(lines.String()), 0644)
}
