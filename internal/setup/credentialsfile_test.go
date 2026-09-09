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

// TestWriteAWSCredentialsProfile_WritesThroughSymlink is the dotfiles rule
// applied to ~/.aws/credentials: operators keep it in a managed directory
// behind a symlink just as they keep sentra.yaml, and renaming the temp
// file over the link would swap the link for a regular file — the AWS CLI
// would keep working while the dotfiles repo silently stopped seeing the
// file. The write must land in the link's target and leave the link
// standing, with no temp file in either directory.
func TestWriteAWSCredentialsProfile_WritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "dotfiles")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDir, "credentials")
	existing := "[work]\naws_access_key_id = AKIAWORK\n"
	if err := os.WriteFile(target, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "credentials")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := WriteAWSCredentialsProfile(link, "sentra", testKeyID, testSecret); err != nil {
		t.Fatalf("WriteAWSCredentialsProfile through symlink: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("write replaced the symlink with a %v; the dotfiles link is severed", fi.Mode())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := existing + "\n[sentra]\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n"
	if string(got) != want {
		t.Errorf("link target content mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
	for _, d := range []string{dir, realDir} {
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp") {
				t.Errorf("%s holds a temp file after the write: %s", d, e.Name())
			}
		}
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

// A static key under [NAME] in ~/.aws/credentials shadows a [profile NAME]
// in ~/.aws/config: aws-sdk-go-v2's resolveCredsFromProfile tests
// Credentials.HasKeys() before hasSSOConfiguration(), so an SSO or
// assume-role profile of that name would silently authenticate as the
// backup user from then on. The pre-check refuses the name outright.
func TestCheckAWSConfigProfileFree(t *testing.T) {
	tests := []struct {
		name    string
		config  string // "" means no file
		profile string
		wantIs  error
		wantErr bool
	}{
		{"no config file", "", "sentra", nil, false},
		{"config without the profile", "[profile work]\nregion = us-east-1\n", "sentra", nil, false},
		{"sso profile of that name", "[profile sentra]\nsso_session = corp\nsso_account_id = 1\nsso_role_name = r\nregion = us-east-1\n", "sentra", ErrConfigProfileExists, true},
		{"region-only profile of that name", "[profile sentra]\nregion = us-east-1\n", "sentra", ErrConfigProfileExists, true},
		{"trimmed lookup", "[profile sentra]\nregion = us-east-1\n", "  sentra ", ErrConfigProfileExists, true},
		{"bare section is not a config profile", "[sentra]\nregion = us-east-1\n", "sentra", nil, false},
		{"default refused by name", "", "default", ErrBackupUserProfileDefault, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if tc.config != "" {
				if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckAWSConfigProfileFree(path, tc.profile)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CheckAWSConfigProfileFree err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want errors.Is %v", err, tc.wantIs)
			}
			if errors.Is(err, ErrConfigProfileExists) {
				// The refusal must say WHY a defined profile is off limits —
				// which section, in which file, and that a static key there
				// would shadow its settings — or the operator reads it as a
				// name clash they could resolve by editing the config file.
				for _, want := range []string{"[profile sentra] is defined in " + path, "shadow"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal %q lacks %q", err.Error(), want)
					}
				}
			}
		})
	}
}
