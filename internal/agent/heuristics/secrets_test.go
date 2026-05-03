package heuristics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/walker"
)

// stageFile copies a fixture from testdata/ to dir/relpath and returns
// a walker.Entry pointing at it. Many secret tests need a copy at a
// specific filename (e.g. ".env") that doesn't exist in testdata/
// directly — keeping fixtures human-readable means we have to map
// from "dotenv.txt" to ".env" at test time.
func stageFile(t *testing.T, dir, fixture, relpath string) walker.Entry {
	t.Helper()
	src := filepath.Join("testdata", fixture)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	abs := filepath.Join(dir, relpath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, raw, 0o600); err != nil { //nolint:gosec // test path under t.TempDir()
		t.Fatalf("write: %v", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return walker.Entry{
		AbsPath: abs,
		RelPath: relpath,
		Size:    st.Size(),
		Mode:    st.Mode(),
		MTime:   st.ModTime(),
	}
}

// TestSecrets_AWSAccessKey verifies the AWS access-key prefix pattern
// fires on a fixture containing AKIAIOSFODNN7EXAMPLE (the canonical
// AWS docs example value).
func TestSecrets_AWSAccessKey(t *testing.T) {
	dir := t.TempDir()
	entry := stageFile(t, dir, "secrets_aws.txt", "config.txt")

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Category != "secrets" || f.Severity != SeverityCritical {
		t.Errorf("finding category/severity: got %s/%s, want secrets/critical", f.Category, f.Severity)
	}
	if f.Target != entry.AbsPath {
		t.Errorf("target: got %s, want %s", f.Target, entry.AbsPath)
	}
	if pattern, _ := f.Details["pattern"].(string); pattern != "aws_access_key" {
		t.Errorf("pattern: got %v, want aws_access_key", f.Details["pattern"])
	}
	if line, _ := f.Details["line"].(int); line != 2 {
		t.Errorf("line: got %v, want 2", f.Details["line"])
	}
	preview, _ := f.Details["preview"].(string)
	if preview == "" {
		t.Errorf("preview is empty")
	}
	// Belt and braces: the preview must NOT contain the actual secret
	// value. The fixture's match is "AKIAIOSFODNN7EXAMPLE"; preview is
	// supposed to redact it.
	if strings.Contains(preview, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("preview leaks secret: %q", preview)
	}
}

// TestSecrets_CleanFile: a benign Go source file produces no findings.
func TestSecrets_CleanFile(t *testing.T) {
	dir := t.TempDir()
	entry := stageFile(t, dir, "clean.go", "main.go")

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestSecrets_BinaryFileSkipped: a file with NUL bytes in its first
// 8 KiB is treated as binary and skipped — even if a regex would
// otherwise match somewhere in the content.
func TestSecrets_BinaryFileSkipped(t *testing.T) {
	dir := t.TempDir()
	entry := stageFile(t, dir, "binary.bin", "blob.bin")

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings on binary, got %+v", got)
	}
}

// TestSecrets_DotEnvGenericKey: a .env-named file containing a generic
// API key produces a critical finding via the .env-specific generic
// pattern.
func TestSecrets_DotEnvGenericKey(t *testing.T) {
	dir := t.TempDir()
	entry := stageFile(t, dir, "dotenv.txt", ".env")

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Severity != SeverityCritical {
		t.Errorf("severity: got %s, want critical", got[0].Severity)
	}
	if pattern, _ := got[0].Details["pattern"].(string); pattern != "dotenv_generic_key" {
		t.Errorf("pattern: got %v, want dotenv_generic_key", got[0].Details["pattern"])
	}
}

// TestSecrets_DotEnvPatternOnlyOnDotEnvFiles: the dotenv generic
// pattern would generate too many false positives on non-.env files
// (any all-caps env-style assignment of a long string), so it must be
// gated to .env-named files specifically.
func TestSecrets_DotEnvPatternOnlyOnDotEnvFiles(t *testing.T) {
	dir := t.TempDir()
	// Same content as the .env fixture, but file is config.txt.
	entry := stageFile(t, dir, "dotenv.txt", "config.txt")

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings on non-.env, got %+v", got)
	}
}

// TestSecrets_LargeFileSkipped: files larger than the 1 MiB scan cap
// are skipped wholesale — we don't try to scan the first 1 MiB and
// stop because that would require state to track partial scans.
func TestSecrets_LargeFileSkipped(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "big.txt")
	// Create a 2 MiB file with the AKIA pattern at the start. The
	// pattern WOULD match if we were scanning, but we skip large files
	// up front.
	body := make([]byte, 2<<20)
	copy(body, []byte("AKIAIOSFODNN7EXAMPLE\n"))
	if err := os.WriteFile(abs, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, _ := os.Stat(abs)
	entry := walker.Entry{
		AbsPath: abs,
		RelPath: "big.txt",
		Size:    st.Size(),
		Mode:    st.Mode(),
		MTime:   time.Now(),
	}

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings on >1MiB file, got %+v", got)
	}
}

// TestSecrets_PrivateKeyHeader: the generic private-key header
// (-----BEGIN ... PRIVATE KEY-----) fires on its own.
func TestSecrets_PrivateKeyHeader(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "id_rsa")
	body := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpQIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----\n" //nolint:gosec // test fixture, not a real key
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, _ := os.Stat(abs)
	entry := walker.Entry{
		AbsPath: abs,
		RelPath: "id_rsa",
		Size:    st.Size(),
		Mode:    st.Mode(),
		MTime:   time.Now(),
	}

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if pattern, _ := got[0].Details["pattern"].(string); pattern != "private_key" {
		t.Errorf("pattern: got %v, want private_key", got[0].Details["pattern"])
	}
}

// TestSecrets_GitHubPAT: a `ghp_<36 chars>` GitHub personal access
// token pattern fires.
func TestSecrets_GitHubPAT(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "notes.md")
	// Made-up PAT — 36 alphanumeric chars after the prefix
	// (a..z is 26, 0..9 is 10 = 36 total).
	body := "auth: ghp_abcdefghijklmnopqrstuvwxyz0123456789\n" //nolint:gosec // test fixture
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, _ := os.Stat(abs)
	entry := walker.Entry{
		AbsPath: abs,
		RelPath: "notes.md",
		Size:    st.Size(),
		Mode:    st.Mode(),
		MTime:   time.Now(),
	}

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if pattern, _ := got[0].Details["pattern"].(string); pattern != "github_pat" {
		t.Errorf("pattern: got %v, want github_pat", got[0].Details["pattern"])
	}
}

// TestSecrets_AWSSecretAccessKey: aws_secret_access_key=... pattern
// fires on its own (separate from the AKIA prefix pattern).
func TestSecrets_AWSSecretAccessKey(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "credentials")
	body := "[default]\naws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n"
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, _ := os.Stat(abs)
	entry := walker.Entry{
		AbsPath: abs,
		RelPath: "credentials",
		Size:    st.Size(),
		Mode:    st.Mode(),
		MTime:   time.Now(),
	}

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if pattern, _ := got[0].Details["pattern"].(string); pattern != "aws_secret_key" {
		t.Errorf("pattern: got %v, want aws_secret_key", got[0].Details["pattern"])
	}
}

// TestSecrets_Name: stable name used by the registry to label findings.
func TestSecrets_Name(t *testing.T) {
	if got, want := NewSecrets().Name(), "secrets"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}

// TestSecrets_PreviewRedactsMultipleMatchesOnSameLine guards against
// the leak where a line containing two AWS keys would only have the
// first one redacted in the preview, exposing the second to the LLM
// in Phase 11. EVERY match on the line — across all patterns — must
// be replaced with [REDACTED] before windowing.
func TestSecrets_PreviewRedactsMultipleMatchesOnSameLine(t *testing.T) {
	//nolint:gosec // synthetic AWS-style fixtures, not real keys
	body := "first AKIAIOSFODNN7EXAMPLE second AKIAIOSFODNN7BACKUPX\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // test fixture, not a real secret
		t.Fatal(err)
	}
	entry := walker.Entry{
		AbsPath: path,
		RelPath: "creds.txt",
		Size:    int64(len(body)),
	}

	h := NewSecrets()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected at least 2 findings, got %d: %+v", len(got), got)
	}

	// Every preview must redact BOTH canonical keys, regardless of
	// which finding's preview we're inspecting.
	for i, f := range got {
		preview, _ := f.Details["preview"].(string)
		for _, leak := range []string{"AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7BACKUPX"} {
			if strings.Contains(preview, leak) {
				t.Errorf("finding %d preview leaks %q: %q", i, leak, preview)
			}
		}
	}
}
