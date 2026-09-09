package policy

import (
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/walker"
)

// TestBackupWalkerOptions pins the one rule every surface's walk must
// share: the config's backup.* keys drive the walker, and IgnoreFile is
// never left empty — a zero walker.Options reads as "legacy defaults"
// to the repo (ExcludeCaches forced true), so an operator's explicit
// exclude_caches: false would be silently overridden wherever a site
// forgot the fill-in. nil is the one exception: no config means no
// operator intent, and the repo's legacy defaults are exactly right.
func TestBackupWalkerOptions(t *testing.T) {
	withBackup := func(ignore string, exclude bool, conc int) *config.Config {
		var cfg config.Config
		cfg.Backup.IgnoreFile = ignore
		cfg.Backup.ExcludeCaches = exclude
		cfg.Backup.Concurrency = conc
		return &cfg
	}
	cases := []struct {
		name string
		cfg  *config.Config
		want walker.Options
	}{
		{
			name: "nil config keeps the repo's legacy defaults",
			cfg:  nil,
			want: walker.Options{},
		},
		{
			name: "explicit exclude_caches false survives an empty ignore_file",
			cfg:  withBackup("", false, 3),
			want: walker.Options{IgnoreFile: ".sentraignore", ExcludeCaches: false, Concurrency: 3},
		},
		{
			name: "configured values pass through",
			cfg:  withBackup(".myignore", true, 8),
			want: walker.Options{IgnoreFile: ".myignore", ExcludeCaches: true, Concurrency: 8},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BackupWalkerOptions(tc.cfg)
			// Options carries a func field, so compare the three keys the
			// config drives rather than the struct.
			if got.IgnoreFile != tc.want.IgnoreFile || got.ExcludeCaches != tc.want.ExcludeCaches || got.Concurrency != tc.want.Concurrency {
				t.Fatalf("BackupWalkerOptions = %+v, want %+v", got, tc.want)
			}
		})
	}
}
