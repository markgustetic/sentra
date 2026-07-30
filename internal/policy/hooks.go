package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

// Hook execution lives here — below both internal/cli and internal/tui
// — so a policy run behaves identically from either surface. A TUI run
// that silently skipped an operator's pg_dump before-hook would back
// up different data than the CLI run of the same policy.

// RunHook executes one hook command via `sh -c`, streaming its output
// to out. The command comes from the operator's own sentra.yaml —
// running what the operator wrote is the feature, not an injection
// surface.
func RunHook(ctx context.Context, out io.Writer, label, script string) error {
	fmt.Fprintf(out, "  hook %s: %s\n", label, script)
	hook := exec.CommandContext(ctx, "sh", "-c", script) //nolint:gosec // operator-authored command from their own config
	hook.Stdout = out
	hook.Stderr = out
	if err := hook.Run(); err != nil {
		return fmt.Errorf("policy hook %s: %w", label, err)
	}
	return nil
}

// FireFailureHooks runs the on_failure command and/or webhook. Both
// are best-effort: the run's own error is what the caller reports,
// and a broken notifier must not mask it.
func FireFailureHooks(ctx context.Context, out io.Writer, name string, hooks config.PolicyHooks, cause error) {
	if hooks.OnFailure != "" {
		if err := RunHook(ctx, out, "on_failure", hooks.OnFailure); err != nil {
			fmt.Fprintf(out, "  hook on_failure failed: %v\n", err)
		}
	}
	if hooks.OnFailureWebhookEnv == "" {
		return
	}
	// Only the env var NAME lives in sentra.yaml; the URL (which
	// often embeds a token) stays in the environment.
	url := os.Getenv(hooks.OnFailureWebhookEnv)
	if url == "" {
		fmt.Fprintf(out, "  webhook skipped: env %s is unset\n", hooks.OnFailureWebhookEnv)
		return
	}
	if err := postFailureWebhook(ctx, url, name, cause); err != nil {
		fmt.Fprintf(out, "  webhook failed: %v\n", err)
	}
}

func postFailureWebhook(ctx context.Context, url, name string, cause error) error {
	payload, err := json.Marshal(map[string]string{
		"policy": name,
		"status": "failed",
		"error":  cause.Error(),
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// gosec flags the variable URL as SSRF. Posting to an
	// operator-chosen endpoint IS the feature: the URL comes from an
	// environment variable the operator set on their own machine,
	// the same trust level as the hook commands themselves.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload)) //nolint:gosec // G704: operator-configured webhook URL from their own environment
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: same operator-configured URL as above
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
