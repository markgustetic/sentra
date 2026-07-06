package cli

import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/ui"
)

// wrapAWSPrepareError classifies an AWS bucket-prep failure with actionable
// guidance. The AWS auth/prepare sequencing now lives in setup.Engine, which
// does its own wrapping; this cli wrapper survives because the oracle drives it
// directly (setup_test.go:710). Per plan correction C10 it dereferences the
// *config.Config to the value setup.WrapAWSPrepareError expects (nil → zero).
func wrapAWSPrepareError(cfg *config.Config, method SetupAWSAuthMethod, err error) error {
	var c config.Config
	if cfg != nil {
		c = *cfg
	}
	return setup.WrapAWSPrepareError(c, method, err)
}

func printSetupErrorDetail(out io.Writer, err error, cfg *config.Config) {
	if err == nil {
		return
	}
	var c config.Config
	if cfg != nil {
		c = *cfg
	}
	fmt.Fprintf(out, "%s %v\n", ui.Danger.Render("reason:"), err)
	for _, line := range setup.ErrorAdvice(err, c) {
		fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("advice:"), line)
	}
}

func setupErrorAdvice(err error, cfg *config.Config) []string {
	var c config.Config
	if cfg != nil {
		c = *cfg
	}
	return setup.ErrorAdvice(err, c)
}
