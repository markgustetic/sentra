package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrCredentialsProfileExists is returned when the target section of the
// AWS shared credentials file already holds an access key. Sentra never
// overwrites a credential it did not create: the operator may have put
// that key there on purpose, and a silent replacement would break every
// other tool reading the profile.
var ErrCredentialsProfileExists = errors.New("aws credentials profile already holds keys")

// AWSCredentialsPath returns the AWS shared credentials file path, honoring
// AWS_SHARED_CREDENTIALS_FILE and falling back to ~/.aws/credentials — the
// same resolution the AWS CLI and SDK use, so the key lands where the SDK
// will look for it.
func AWSCredentialsPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("AWS_SHARED_CREDENTIALS_FILE")); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate aws credentials: %w", err)
	}
	return filepath.Join(home, ".aws", "credentials"), nil
}

// CheckAWSCredentialsProfileFree reports whether WriteAWSCredentialsProfile
// would accept profile at path — the same refusals, without writing. The
// provisioner calls it BEFORE creating an access key, so a doomed run makes
// no IAM mutation it would only have to undo.
func CheckAWSCredentialsProfileFree(path, profile string) error {
	profile = strings.TrimSpace(profile)
	if err := ValidateBackupUserProfile(profile); err != nil {
		return err
	}
	existing, err := os.ReadFile(path) //nolint:gosec // path is the operator's own credentials file
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	_, err = upsertCredentialsSection(existing, profile, "", "")
	return err
}

// WriteAWSCredentialsProfile stores accessKeyID/secret under [profile] in
// the shared credentials file at path. It is a minimal-touch edit: the file
// is the operator's, so every byte outside the target section is preserved,
// including comments and unknown keys. The write is temp-file + rename and
// the result is mode 0600.
//
// Refusals (see ValidateBackupUserProfile and ErrCredentialsProfileExists)
// leave the file untouched.
func WriteAWSCredentialsProfile(path, profile, accessKeyID, secret string) error {
	profile = strings.TrimSpace(profile)
	if err := ValidateBackupUserProfile(profile); err != nil {
		return err
	}
	existing, err := os.ReadFile(path) //nolint:gosec // path is the operator's own credentials file
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated, err := upsertCredentialsSection(existing, profile, accessKeyID, secret)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// CreateTemp opens 0600; rename makes the replacement atomic, so a crash
	// mid-write can never leave a half-written credentials file behind.
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp credentials file: %w", err)
	}
	tmpPath := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s %s: %w", step, path, err)
	}
	if _, err := tmp.Write(updated); err != nil {
		return fail("write", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail("chmod", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// upsertCredentialsSection returns existing with [profile] holding the two
// key lines, or ErrCredentialsProfileExists when that section already has
// either key. Pure: the writer and the pre-check share it so they can never
// disagree about what counts as "taken".
func upsertCredentialsSection(existing []byte, profile, accessKeyID, secret string) ([]byte, error) {
	header := "[" + profile + "]"
	keyLines := []string{
		"aws_access_key_id = " + accessKeyID,
		"aws_secret_access_key = " + secret,
	}
	if len(existing) == 0 {
		return []byte(header + "\n" + strings.Join(keyLines, "\n") + "\n"), nil
	}

	text := string(existing)
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			start = i
			break
		}
	}
	if start == -1 {
		var b strings.Builder
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n" + header + "\n" + strings.Join(keyLines, "\n") + "\n")
		return []byte(b.String()), nil
	}

	end := len(lines) // exclusive: next section header or EOF
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	insertAt := start + 1
	for i := start + 1; i < end; i++ {
		key, _, ok := strings.Cut(lines[i], "=")
		if ok {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "aws_access_key_id", "aws_secret_access_key":
				return nil, fmt.Errorf("%w: [%s]", ErrCredentialsProfileExists, profile)
			}
		}
		if strings.TrimSpace(lines[i]) != "" {
			insertAt = i + 1 // after the section's last non-blank line
		}
	}
	out := make([]string, 0, len(lines)+len(keyLines))
	out = append(out, lines[:insertAt]...)
	out = append(out, keyLines...)
	out = append(out, lines[insertAt:]...)
	joined := strings.Join(out, "\n")
	if !strings.HasSuffix(joined, "\n") {
		joined += "\n"
	}
	return []byte(joined), nil
}
