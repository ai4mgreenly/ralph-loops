// Package reqs reads requirement IDs from a project's spec tree and
// the agent's verification ledger, then computes the set difference
// between them. It is the read-side counterpart to the agent's
// per-iteration update of [LedgerPath].
package reqs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// IDPattern matches the canonical R-XXXX-XXXX requirement-ID shape:
// "R-" followed by two four-character chunks of upper alphanumerics
// separated by a dash. It is the same pattern the agent's prompt
// instructs scanning by, surfaced here so the CLI does not have to
// shell out to grep.
var IDPattern = regexp.MustCompile(`R-[A-Z0-9]{4}-[A-Z0-9]{4}`)

// PlaceholderID is the literal token reserved for use in templates,
// docs, and example bullets. It matches [IDPattern] but is never a
// real minted ID, so [SpecIDs] filters it out — that way a sample
// requirement copy-pasted into a spec does not pollute the work queue.
const PlaceholderID = "R-XXXX-XXXX"

// LedgerPath is the workdir-relative path of the ledger the agent
// appends to as it verifies requirements. The value is fixed: the
// agent must always read and write at exactly this path.
const LedgerPath = ".ralph/requirements-verified.jsonl"

// SpecIDs walks reqsDir, scans every regular file's contents for
// [IDPattern] matches, drops [PlaceholderID], and returns the deduped
// result sorted lexicographically. A missing reqsDir is reported as an
// error; an empty tree returns an empty slice.
func SpecIDs(reqsDir string) ([]string, error) {
	seen := map[string]struct{}{}
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read %q: %w", path, rerr)
		}
		for _, m := range IDPattern.FindAll(body, -1) {
			id := string(m)
			if id == PlaceholderID {
				continue
			}
			seen[id] = struct{}{}
		}
		return nil
	}
	if err := filepath.WalkDir(reqsDir, walk); err != nil {
		return nil, fmt.Errorf("walk %q: %w", reqsDir, err)
	}
	return sortedKeys(seen), nil
}

// VerifiedIDs reads <workDir>/<LedgerPath> and returns the deduped,
// sorted set of "id" fields. A missing ledger is not an error — the
// run hasn't verified anything yet, so the empty set is the right
// answer. A malformed line is reported with its file:line location so
// the operator can fix it.
func VerifiedIDs(workDir string) ([]string, error) {
	path := filepath.Join(workDir, LedgerPath)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	seen := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if rec.ID == "" {
			return nil, fmt.Errorf("%s:%d: missing or empty id field", path, line)
		}
		seen[rec.ID] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %q: %w", path, err)
	}
	return sortedKeys(seen), nil
}

// Unverified returns the IDs that appear in the spec under reqsDir
// but are not yet recorded in workDir's verification ledger, sorted.
// It is the single-call replacement for the prompt's grep + jsonl +
// diff procedure: a [SpecIDs] minus a [VerifiedIDs].
func Unverified(reqsDir, workDir string) ([]string, error) {
	spec, err := SpecIDs(reqsDir)
	if err != nil {
		return nil, err
	}
	verified, err := VerifiedIDs(workDir)
	if err != nil {
		return nil, err
	}
	v := make(map[string]struct{}, len(verified))
	for _, id := range verified {
		v[id] = struct{}{}
	}
	out := make([]string, 0, len(spec))
	for _, id := range spec {
		if _, ok := v[id]; ok {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
