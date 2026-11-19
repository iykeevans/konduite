// Package cli builds the agenthub command tree.
package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/iykeevans/konduite/runtime/internal/auth"
	"github.com/iykeevans/konduite/runtime/internal/config"
	"github.com/iykeevans/konduite/runtime/internal/security"
	"github.com/iykeevans/konduite/runtime/internal/version"
)

// Options injects build info, the environment and the outside world so tests
// can drive the CLI in-process. Nil fields get the real implementations.
type Options struct {
	Version     version.Info
	Getenv      func(string) string
	Keychain    security.Keychain
	HTTPClient  *http.Client
	OpenBrowser func(url string) error
}

// New returns the root agenthub command.
func New(opts Options) *cobra.Command {
	var configFlag string

	root := &cobra.Command{
		Use:           "agenthub",
		Short:         "Local runtime connector for Konduite",
		Version:       opts.Version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("agenthub {{.Version}}\n")
	root.PersistentFlags().StringVar(&configFlag, "config", "", "config file (default $AGENTHUB_CONFIG, then $XDG_CONFIG_HOME/agenthub/config.yaml, then ~/.config/agenthub/config.yaml)")

	configPath := func() (string, error) { return config.Path(configFlag, opts.Getenv) }
	if opts.Keychain == nil {
		opts.Keychain = security.OSKeychain{}
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = openBrowser
	}
	client := func(cmd *cobra.Command) auth.Client {
		return auth.Client{Keychain: opts.Keychain, HTTP: opts.HTTPClient, Open: opts.OpenBrowser, Out: cmd.ErrOrStderr()}
	}

	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print version information",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "agenthub %s\n", opts.Version)
				return err
			},
		},
		newConfigCmd(configPath),
		newLoginCmd(configPath, client),
		newLogoutCmd(configPath, client),
		newWhoamiCmd(configPath, client),
	)
	return root
}

func newConfigCmd(configPath func() (string, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and change non-secret settings",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file location",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				path, err := configPath()
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
				return err
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective settings",
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
				out, err := yaml.Marshal(cfg)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(out)
				return err
			},
		},
		&cobra.Command{
			Use:   "set <key> <value>",
			Short: "Change a setting (" + config.Keys + ")",
			Args:  cobra.ExactArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				path, err := configPath()
				if err != nil {
					return err
				}
				cfg, err := config.Load(path)
				if err != nil {
					return err
				}
				if err := cfg.Set(args[0], args[1]); err != nil {
					return err
				}
				return config.Save(path, cfg)
			},
		},
	)
	return cmd
}
