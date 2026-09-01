package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testKeyID  = "AKIAEXAMPLEEXAMPLE01"
	testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

func TestWriteAWSCredentialsProfile(t *testing.T) {
	tests := []struct {
		name     string
		existing string // "" means no file
		profile  string
		wantErr  error  // errors.Is target, nil for success
		want     string // exact file content after a successful write
	}{
		{
			name:    "new file",
			profile: "sentra",
			want:    "[sentra]\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n",
		},
		{
			name:     "append after other sections, byte-preserved",
			existing: "; my creds\n[work]\naws_access_key_id = AKIAWORK\naws_secret_access_key = x\n",
			profile:  "sentra",
			want: "; my creds\n[work]\naws_access_key_id = AKIAWORK\naws_secret_access_key = x\n" +
				"\n[sentra]\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n",
		},
		{
			name:     "append to file without trailing newline",
			existing: "[work]\nregion = us-west-2",
			profile:  "sentra",
			want: "[work]\nregion = us-west-2\n" +
				"\n[sentra]\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n",
		},
		{
			name:     "insert into existing keyless section, later sections preserved",
			existing: "[sentra]\nregion = us-east-1\n\n[work]\naws_access_key_id = AKIAWORK\n",
			profile:  "sentra",
			want: "[sentra]\nregion = us-east-1\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n" +
				"\n[work]\naws_access_key_id = AKIAWORK\n",
		},
		{
			name:     "refuse section that holds keys",
			existing: "[sentra]\naws_access_key_id = AKIAOLD\naws_secret_access_key = old\n",
			profile:  "sentra",
			wantErr:  ErrCredentialsProfileExists,
		},
		{
			name:     "refuse section that holds keys, mixed case",
			existing: "[sentra]\nAWS_SECRET_ACCESS_KEY=old\n",
			profile:  "sentra",
			wantErr:  ErrCredentialsProfileExists,
		},
		{
			name:    "refuse default",
			profile: "default",
			wantErr: ErrBackupUserProfileDefault,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".aws", "credentials")
			if tc.existing != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.existing), 0o644); err != nil { //nolint:gosec // deliberately permissive: the writer must replace it with 0600
					t.Fatal(err)
				}
			}
			err := WriteAWSCredentialsProfile(path, tc.profile, testKeyID, testSecret)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
				}
				// A refusal must leave the file exactly as it was.
				if tc.existing != "" {
					got, _ := os.ReadFile(path)
					if string(got) != tc.existing {
						t.Fatalf("refused write modified the file:\n%s", got)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteAWSCredentialsProfile: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("content mismatch\n--- got\n%s\n--- want\n%s", got, tc.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Fatalf("mode = %o, want 0600", perm)
			}
			// Atomic write leaves no temp files behind.
			entries, _ := os.ReadDir(filepath.Dir(path))
			for _, e := range entries {
				if strings.Contains(e.Name(), ".tmp") {
					t.Fatalf("temp file left behind: %s", e.Name())
				}
			}
		})
	}
}

func TestCheckAWSCredentialsProfileFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := CheckAWSCredentialsProfileFree(path, "sentra"); err != nil {
		t.Fatalf("missing file must be free: %v", err)
	}
	if err := os.WriteFile(path, []byte("[sentra]\naws_access_key_id = x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckAWSCredentialsProfileFree(path, "sentra"); !errors.Is(err, ErrCredentialsProfileExists) {
		t.Fatalf("keyed section must report exists, got %v", err)
	}
	if err := CheckAWSCredentialsProfileFree(path, "other"); err != nil {
		t.Fatalf("absent section must be free: %v", err)
	}
	if err := CheckAWSCredentialsProfileFree(path, "default"); !errors.Is(err, ErrBackupUserProfileDefault) {
		t.Fatalf("default must be refused, got %v", err)
	}
}

func TestAWSCredentialsPathHonorsEnv(t *testing.T) {
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/tmp/x/creds")
	got, err := AWSCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/x/creds" {
		t.Fatalf("path = %q, want env override", got)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	got, err = AWSCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, filepath.Join(".aws", "credentials")) {
		t.Fatalf("path = %q, want ~/.aws/credentials", got)
	}
}
