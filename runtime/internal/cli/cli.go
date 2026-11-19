// Package cli builds the agenthub command tree.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/iykeevans/konduite/runtime/internal/config"
	"github.com/iykeevans/konduite/runtime/internal/version"
)

// Options injects build info and the environment so tests can drive the CLI in-process.
type Options struct {
	Version version.Info
	Getenv  func(string) string
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
			Short: "Change a setting (server_url, runtime_id, log_level)",
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
