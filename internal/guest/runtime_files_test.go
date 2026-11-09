package guest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileFixture(t *testing.T) (Runtime, Request) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "executable")
	if err := os.WriteFile(source, []byte("Go helper fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	r := Runtime{Home: t.TempDir(), MachineID: strings.Repeat("b", 32), MountPoints: func() ([]string, error) { return nil, nil }, Executable: func() (string, error) { return source, nil }}
	request := Request{ID: strings.Repeat("a", 32), Role: "control", Operation: "prepare", Files: []string{".kube/modelgauge-config"}, MachineID: r.MachineID}
	if _, err := r.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return r, request
}
func TestManagedWriteCommitsBeforeFinalSeal(t *testing.T) {
	r, q := fileFixture(t)
	if err := r.WriteManaged(context.Background(), q.Files[0], 0600, strings.NewReader("configuration")); err != nil {
		t.Fatal(err)
	}
	state, err := r.load(q)
	if err != nil {
		t.Fatal(err)
	}
	if state.Files[q.Files[0]] == nil || len(state.Pending) != 0 {
		t.Fatal("write was not committed")
	}
	q.Operation = "prepare"
	if _, err = r.Execute(context.Background(), q); err != nil {
		t.Fatal("retry required final sealing:", err)
	}
}
func TestPendingRenameIsReconciledButForeignContentRefused(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "intended", true: "foreign"}[foreign], func(t *testing.T) {
			r, q := fileFixture(t)
			source := filepath.Join(t.TempDir(), "intended")
			_ = os.WriteFile(source, []byte("intended"), 0600)
			sum, _ := digest(source)
			state, _ := r.load(q)
			state.Pending = map[string]PendingFile{q.Files[0]: {SHA256: sum, Mode: 0600}}
			if err := r.save(state); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(r.Home, q.Files[0])
			_ = os.MkdirAll(filepath.Dir(target), 0700)
			data := []byte("intended")
			if foreign {
				data = []byte("replacement")
			}
			_ = os.WriteFile(target, data, 0600)
			_, err := r.Execute(context.Background(), q)
			if foreign {
				if err == nil {
					t.Fatal("unproven replacement adopted")
				}
				got, _ := os.ReadFile(target)
				if !bytes.Equal(got, data) {
					t.Fatal("foreign content changed")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				state, _ = r.load(q)
				if len(state.Pending) != 0 || state.Files[q.Files[0]] == nil {
					t.Fatal("pending commit not recovered")
				}
			}
		})
	}
}
func TestLegacyUnsealedFileIsNeverAdopted(t *testing.T) {
	r, q := fileFixture(t)
	target := filepath.Join(r.Home, q.Files[0])
	_ = os.MkdirAll(filepath.Dir(target), 0700)
	_ = os.WriteFile(target, []byte("unknown"), 0600)
	for _, action := range []string{"prepare", "seal"} {
		q.Operation = action
		if _, err := r.Execute(context.Background(), q); err == nil {
			t.Fatalf("%s adopted an unsealed file", action)
		}
	}
}
func TestCancelledWritePreservesCommittedVersion(t *testing.T) {
	r, q := fileFixture(t)
	if err := r.WriteManaged(context.Background(), q.Files[0], 0600, strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.WriteManaged(ctx, q.Files[0], 0600, strings.NewReader("replacement")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(r.Home, q.Files[0]))
	if string(got) != "original" {
		t.Fatal("cancelled write replaced committed version")
	}
	files, _ := filepath.Glob(filepath.Join(r.Home, ".kube/.modelgauge-install-*"))
	if len(files) != 0 {
		t.Fatal("staged file retained after ordinary cancellation")
	}
}
