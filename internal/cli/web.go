package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/web"
)

// WebDeps wires the side-effecting pieces of `sentra web` so tests can drop in a
// memory store, a stub Serve, and a no-op browser opener. It mirrors UIDeps.
type WebDeps struct {
	RepoDeps

	// Serve runs the HTTP server on ln until the context is cancelled.
	// Production wires it to serveWeb; tests capture the *web.Server instead.
	Serve func(ctx context.Context, srv *web.Server, ln net.Listener) error

	// OpenBrowser opens url in the default browser. Nil (or --no-open) skips.
	OpenBrowser func(url string) error

	// PassphraseFile resolves the --passphrase-file path, same as UIDeps.
	PassphraseFile func() string
}

// NewWeb builds the `sentra web` cobra command: a localhost-only browser UI over
// the same repo the CLI/TUI use.
func NewWeb(deps WebDeps) *cobra.Command {
	var cfgPath string
	var port int
	var noOpen bool
	cmd := &cobra.Command{
		Use:           "web",
		Short:         "Launch the browser dashboard (localhost only)",
		Long:          "Serve the sentra web UI on 127.0.0.1 and open your browser. The server never binds to a non-loopback address.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWeb(cmd, deps, cfgPath, port, noOpen)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName, "path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.Flags().IntVar(&port, "port", 0, "TCP port on 127.0.0.1 (0 = an ephemeral free port)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser automatically")
	return cmd
}

func runWeb(cmd *cobra.Command, deps WebDeps, cfgPath string, port int, noOpen bool) error {
	st, err := probeLaunchState(cmd, cfgPath, passphraseFileValue(deps.PassphraseFile))
	if err != nil {
		return err
	}
	if !st.ConfigExists {
		// The first-run setup wizard is a later web phase; until then, point the
		// operator at the CLI/TUI wizard.
		return errors.New("no sentra.yaml found — run `sentra setup` first (the web setup wizard arrives in a later phase)")
	}

	repoName := st.Config.Repo.S3.Bucket
	if repoName == "" {
		repoName = "sentra"
	}

	// If a non-interactive source can supply the passphrase, open now so the
	// browser lands unlocked. Otherwise start locked; the browser unlock gate
	// collects it and calls the Unlock closure below.
	var opened *repo.Repo
	if st.PassphraseAvailable {
		r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
		if err != nil {
			return err
		}
		crypto.Zeroize(pass)
		opened, st.Config = r, cfg
	}

	unlock := func(pass []byte) (*repo.Repo, error) {
		store, err := deps.NewStore(cmd.Context(), st.Config)
		if err != nil {
			return nil, fmt.Errorf("open blobstore: %w", err)
		}
		return repo.Open(cmd.Context(), store, pass)
	}

	srv := web.New(web.Deps{
		Repo:       opened,
		Config:     st.Config,
		RepoName:   repoName,
		ConfigPath: cfgPath,
		Unlock:     unlock,
		Assets:     web.Assets,
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	url := "http://" + ln.Addr().String()
	fmt.Fprintf(cmd.OutOrStdout(), "sentra web serving at %s  (Ctrl-C to stop)\n", url)
	if !noOpen && deps.OpenBrowser != nil {
		_ = deps.OpenBrowser(url) // best-effort; the URL is already printed
	}
	return deps.Serve(cmd.Context(), srv, ln)
}

// passphraseFileValue calls the resolver (if any) and returns "" when unset.
func passphraseFileValue(f func() string) string {
	if f == nil {
		return ""
	}
	return f()
}

// serveWeb is the production Serve: it runs the HTTP server and shuts it down
// gracefully when the command's context is cancelled (Ctrl-C).
func ServeWebProduction(ctx context.Context, srv *web.Server, ln net.Listener) error {
	hs := &http.Server{
		Handler:           srv.Handler(),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 10 * time.Second, // slow-header (Slowloris) guard
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutCtx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// openBrowser opens url in the platform default browser. Best-effort: a failure
// is not fatal because runWeb has already printed the URL.
func OpenBrowserProduction(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	return exec.Command(name, append(args, url)...).Start()
}
