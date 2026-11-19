package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/iykeevans/konduite/runtime/internal/auth"
	"github.com/iykeevans/konduite/runtime/internal/config"
)

func newLoginCmd(configPath func() (string, error), client func(*cobra.Command) auth.Client) *cobra.Command {
	var workspace, name, provider, email string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in through the browser and register this machine as a runtime",
		Long: "Signs you in with Supabase through your browser (OAuth provider or emailed link),\n" +
			"registers this machine as a runtime in the workspace and stores the runtime's\n" +
			"credential in the OS keychain. Only the runtime and workspace ids go in the config file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := configPath()
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			if workspace == "" {
				workspace = cfg.WorkspaceID
			}
			if provider == "" && email == "" {
				provider = cfg.AuthProvider
			}
			if name == "" {
				name, _ = os.Hostname()
			}

			rt, err := client(cmd).Login(cmd.Context(), auth.LoginInput{
				Config: cfg, WorkspaceID: workspace, Name: name, Provider: provider, Email: email,
			})
			if err != nil {
				return err
			}
			cfg.RuntimeID, cfg.WorkspaceID = rt.ID, rt.WorkspaceID
			if err := config.Save(path, cfg); err != nil {
				return fmt.Errorf("logged in, but saving the runtime id failed: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Logged in. Runtime %q (%s) is registered in workspace %s.\nIts credential is in the OS keychain.\n",
				rt.Name, rt.ID, rt.WorkspaceID)
			return err
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace id to register in (default: workspace_id from config)")
	cmd.Flags().StringVar(&name, "name", "", "runtime name (default: this machine's hostname)")
	cmd.Flags().StringVar(&provider, "provider", "", "Supabase OAuth provider, e.g. github (default: auth_provider from config)")
	cmd.Flags().StringVar(&email, "email", "", "sign in with a link sent to this address instead of an OAuth provider")
	return cmd
}

func newLogoutCmd(configPath func() (string, error), client func(*cobra.Command) auth.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke this runtime's credential and remove it from the OS keychain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := configPath()
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			res, err := client(cmd).Logout(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			runtimeID := cfg.RuntimeID
			if runtimeID != "" {
				cfg.RuntimeID = ""
				if err := config.Save(path, cfg); err != nil {
					return err
				}
			}

			out := cmd.OutOrStdout()
			switch {
			case !res.WasLoggedIn:
				_, err = fmt.Fprintf(out, "Not logged in to %s.\n", cfg.ServerURL)
			case res.Revoked:
				_, err = fmt.Fprintln(out, "Logged out: the runtime credential was revoked and removed from the OS keychain.")
			case res.AlreadyRevoked:
				_, err = fmt.Fprintln(out, "Logged out: the server no longer accepted the credential (already revoked); it was removed from the OS keychain.")
			default:
				return fmt.Errorf("removed the credential from the OS keychain, but could not revoke it on the server: %w; "+
					"it stays valid there until runtime %s is revoked from the dashboard", res.RevokeErr, runtimeOrUnknown(runtimeID))
			}
			return err
		},
	}
}

func newWhoamiCmd(configPath func() (string, error), client func(*cobra.Command) auth.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show which server, workspace and runtime this machine is logged in as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := configPath()
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			ok, err := client(cmd).LoggedIn(cfg)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !ok {
				_, err = fmt.Fprintf(out, "server:     %s\nnot logged in (run agenthub login)\n", cfg.ServerURL)
				return err
			}
			_, err = fmt.Fprintf(out, "server:     %s\nworkspace:  %s\nruntime:    %s\ncredential: in the OS keychain\n",
				cfg.ServerURL, cfg.WorkspaceID, runtimeOrUnknown(cfg.RuntimeID))
			return err
		},
	}
}

func runtimeOrUnknown(id string) string {
	if id == "" {
		return "(unknown)"
	}
	return id
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec,noctx // fixed program; the URL is the Supabase sign-in page built by auth
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec,noctx // as above
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec,noctx // as above
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
