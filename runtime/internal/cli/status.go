package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/iykeevans/konduite/runtime/internal/auth"
	"github.com/iykeevans/konduite/runtime/internal/config"
)

func newStatusCmd(configPath func() (string, error), client func(*cobra.Command) auth.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show this machine's runtime and what the server knows about it",
		Long: "Prints the server, workspace and runtime id from the config file and whether a\n" +
			"credential is in the OS keychain. When one is, it is exchanged for a short-lived\n" +
			"access token to ask the server about the runtime. A server that cannot be reached\n" +
			"is reported, not treated as an error.",
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
			st, err := client(cmd).Status(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !st.LoggedIn {
				_, err = fmt.Fprintf(out, "server:     %s\nnot logged in (run agenthub login)\n", cfg.ServerURL)
				return err
			}
			_, err = fmt.Fprintf(out, "server:     %s\nworkspace:  %s\nruntime:    %s\ncredential: in the OS keychain\non server:  %s\n",
				cfg.ServerURL, cfg.WorkspaceID, runtimeOrUnknown(cfg.RuntimeID), remoteLine(st, cfg))
			return err
		},
	}
}

func remoteLine(st auth.Status, cfg config.Config) string {
	switch {
	case st.Rejected:
		return "credential rejected (revoked or expired); run agenthub login"
	case st.RemoteErr != nil:
		return fmt.Sprintf("unreachable (%v)", st.RemoteErr)
	case st.Remote == nil:
		return "(unknown)"
	}
	rt := st.Remote
	line := fmt.Sprintf("%q, active, registered %s", rt.Name, rt.CreatedAt.Format(time.RFC3339))
	if rt.Revoked {
		line = fmt.Sprintf("%q, revoked", rt.Name)
	}
	if cfg.RuntimeID != "" && rt.ID != cfg.RuntimeID {
		line += fmt.Sprintf("; the credential belongs to runtime %s, not the one in the config", rt.ID)
	}
	return line
}
