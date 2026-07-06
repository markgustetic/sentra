package cli

import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/ui"
)

func wrapAWSSSOFlowError(command string, profile string, err error) error {
	return setup.WrapAWSSSOFlowError(command, profile, err)
}

func wrapAWSPrepareError(cfg *config.Config, method SetupAWSAuthMethod, err error) error {
	var c config.Config
	if cfg != nil {
		c = *cfg
	}
	return setup.WrapAWSPrepareError(c, method, err)
}

func wrapAWSLoginFlowError(profile string, err error) error {
	return setup.WrapAWSLoginFlowError(profile, err)
}

func isAWSMissingCredentialsError(err error) bool {
	return setup.IsAWSMissingCredentialsError(err)
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
