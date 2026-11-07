// Package evidence copies only manifest-listed, checksum-verified raw files.
package evidence

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxFileBytes int64 = 128 << 20
const MaxTotalBytes int64 = 1 << 30

var simpleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var checksum = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Relative(ref protocol.EvidenceRef) (string, error) {
	if !simpleName.MatchString(ref.RunID) || path.Clean(ref.Name) != ref.Name || path.IsAbs(ref.Name) || strings.ContainsAny(ref.Name, "\\\x00") || ref.Name == "." || strings.HasPrefix(ref.Name, "../") || !checksum.MatchString(ref.SHA256) {
		return "", fmt.Errorf("unsafe raw evidence reference")
	}
	for _, part := range strings.Split(ref.Name, "/") {
		if !simpleName.MatchString(part) {
			return "", fmt.Errorf("unsafe evidence filename")
		}
	}
	return ref.RunID + "/" + ref.Name, nil
}
func SafeFile(root, relative string) (string, error) {
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("evidence root must be a real directory")
	}
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink evidence paths are refused")
		}
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("evidence is not a regular file")
	}
	return current, nil
}
func copyVerified(destination, relative string, source io.Reader, size int64, expected string) error {
	if size < 0 || size > MaxFileBytes {
		return fmt.Errorf("raw evidence file exceeds size limit")
	}
	target := filepath.Join(destination, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, size+1))
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != expected {
		_ = os.Remove(target)
		return fmt.Errorf("raw evidence checksum/size mismatch: %s", relative)
	}
	return nil
}
func CopyLocal(source, destination string, refs []protocol.EvidenceRef) error {
	var total int64
	for _, ref := range refs {
		relative, err := Relative(ref)
		if err != nil {
			return err
		}
		filePath, err := SafeFile(source, relative)
		if err != nil {
			return err
		}
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		total += info.Size()
		if total > MaxTotalBytes {
			file.Close()
			return fmt.Errorf("raw evidence exceeds total size limit")
		}
		err = copyVerified(destination, relative, file, info.Size(), ref.SHA256)
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func ReceiveArchive(source io.Reader, destination string, refs []protocol.EvidenceRef) error {
	expected := map[string]string{}
	for _, ref := range refs {
		relative, err := Relative(ref)
		if err != nil {
			return err
		}
		if _, found := expected[relative]; found {
			return fmt.Errorf("duplicate evidence reference")
		}
		expected[relative] = ref.SHA256
	}
	reader := tar.NewReader(source)
	var total int64
	for {
		entry, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		digest, found := expected[entry.Name]
		if !found || entry.Typeflag != tar.TypeReg {
			return fmt.Errorf("unexpected archive entry or link")
		}
		total += entry.Size
		if entry.Size < 0 || total > MaxTotalBytes {
			return fmt.Errorf("raw evidence exceeds total size limit")
		}
		if err = copyVerified(destination, entry.Name, reader, entry.Size, digest); err != nil {
			return err
		}
		delete(expected, entry.Name)
	}
	if len(expected) != 0 {
		return fmt.Errorf("raw archive is missing %d manifest files", len(expected))
	}
	return nil
}

// WriteArchive streams only inventory-listed files and preserves their bytes.
func WriteArchive(ctx context.Context, source string, refs []protocol.EvidenceRef, out io.Writer) error {
	if len(refs) > 10000 {
		return fmt.Errorf("too many evidence files")
	}
	writer := tar.NewWriter(out)
	var total int64
	seen := map[string]bool{}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := Relative(ref)
		if err != nil {
			return err
		}
		if seen[relative] {
			return fmt.Errorf("duplicate evidence reference")
		}
		seen[relative] = true
		path, err := SafeFile(source, relative)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		total += info.Size()
		if info.Size() > MaxFileBytes || total > MaxTotalBytes {
			file.Close()
			return fmt.Errorf("evidence size limit exceeded")
		}
		hash := sha256.New()
		if _, err = io.Copy(hash, file); err != nil {
			file.Close()
			return err
		}
		if hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
			file.Close()
			return fmt.Errorf("raw evidence checksum mismatch")
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			return err
		}
		if err = writer.WriteHeader(&tar.Header{Name: relative, Mode: 0600, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
			file.Close()
			return err
		}
		_, err = io.Copy(writer, file)
		file.Close()
		if err != nil {
			return err
		}
	}
	return writer.Close()
}
