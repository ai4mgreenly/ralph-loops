package reqs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func TestSpecIDs_DedupesAcrossFilesAndDropsPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "OVERVIEW.md"), `
- R-052Y-EKE0: anonymous visitors cannot post.
- R-3HX7-91ZA: archived posts stay readable.
- placeholder example: R-XXXX-XXXX (must be filtered).
`)
	writeFile(t, filepath.Join(dir, "nested", "INTERACTIVE.md"), `
- R-052Y-EKE0: duplicate of one above.
- R-9PQR-12ST: closed accounts cannot log in.
`)
	writeFile(t, filepath.Join(dir, "notes.txt"), "no IDs in here\n")

	got, err := SpecIDs(dir)
	if err != nil {
		t.Fatalf("SpecIDs: %v", err)
	}
	want := []string{"R-052Y-EKE0", "R-3HX7-91ZA", "R-9PQR-12ST"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SpecIDs = %v, want %v", got, want)
	}
}

func TestSpecIDs_EmptyTreeYieldsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := SpecIDs(dir)
	if err != nil {
		t.Fatalf("SpecIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestSpecIDs_MissingDirIsError(t *testing.T) {
	if _, err := SpecIDs(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing reqsDir")
	}
}

func TestVerifiedIDs_MissingLedgerYieldsEmpty(t *testing.T) {
	got, err := VerifiedIDs(t.TempDir())
	if err != nil {
		t.Fatalf("VerifiedIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestVerifiedIDs_DedupesSortsAndTolersBlankLines(t *testing.T) {
	work := t.TempDir()
	writeFile(t, filepath.Join(work, LedgerPath), `{"id":"R-9PQR-12ST"}

{"id":"R-052Y-EKE0"}
{"id":"R-9PQR-12ST"}
`)
	got, err := VerifiedIDs(work)
	if err != nil {
		t.Fatalf("VerifiedIDs: %v", err)
	}
	want := []string{"R-052Y-EKE0", "R-9PQR-12ST"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VerifiedIDs = %v, want %v", got, want)
	}
}

func TestVerifiedIDs_BadJSONReportsLine(t *testing.T) {
	work := t.TempDir()
	writeFile(t, filepath.Join(work, LedgerPath), `{"id":"R-052Y-EKE0"}
not json
`)
	_, err := VerifiedIDs(work)
	if err == nil {
		t.Fatal("expected error for malformed line")
	}
	if !strings.Contains(err.Error(), ":2:") {
		t.Errorf("error should pin line 2, got: %v", err)
	}
}

func TestVerifiedIDs_EmptyIDIsError(t *testing.T) {
	work := t.TempDir()
	writeFile(t, filepath.Join(work, LedgerPath), `{"id":""}`+"\n")
	if _, err := VerifiedIDs(work); err == nil {
		t.Fatal("expected error for empty id field")
	}
}

func TestUnverified_DiffsSpecAgainstLedger(t *testing.T) {
	reqsDir := t.TempDir()
	writeFile(t, filepath.Join(reqsDir, "spec.md"), `
- R-052Y-EKE0
- R-3HX7-91ZA
- R-9PQR-12ST
- R-XXXX-XXXX (placeholder, ignored)
`)
	work := t.TempDir()
	writeFile(t, filepath.Join(work, LedgerPath), `{"id":"R-052Y-EKE0"}
{"id":"R-9PQR-12ST"}
`)

	got, err := Unverified(reqsDir, work)
	if err != nil {
		t.Fatalf("Unverified: %v", err)
	}
	want := []string{"R-3HX7-91ZA"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Unverified = %v, want %v", got, want)
	}
}

func TestUnverified_AllVerifiedYieldsEmpty(t *testing.T) {
	reqsDir := t.TempDir()
	writeFile(t, filepath.Join(reqsDir, "spec.md"), "- R-052Y-EKE0\n")
	work := t.TempDir()
	writeFile(t, filepath.Join(work, LedgerPath), `{"id":"R-052Y-EKE0"}`+"\n")

	got, err := Unverified(reqsDir, work)
	if err != nil {
		t.Fatalf("Unverified: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}
