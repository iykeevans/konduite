package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/iykeevans/konduite/runtime/internal/auth"
	"github.com/iykeevans/konduite/runtime/internal/config"
)

func newAgentsCmd(configPath func() (string, error), client func(*cobra.Command) auth.Client) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Register and list the agents on this machine's runtime",
	}
	loadConfig := func() (config.Config, error) {
		path, err := configPath()
		if err != nil {
			return config.Config{}, err
		}
		return config.Load(path)
	}

	var in auth.AgentInput
	register := &cobra.Command{
		Use:   "register",
		Short: "Register an agent before this runtime first announces it",
		Long: "Registers an agent on this machine's runtime so it can be labelled and granted\n" +
			"project access before the runtime connects. --provider and --name must match what\n" +
			"the runtime will announce: when it does, the server keeps this agent and marks it\n" +
			"online instead of creating another. Authenticates with the runtime's own credential.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			a, err := client(cmd).RegisterAgent(cmd.Context(), cfg, in)
			if err != nil {
				return fmt.Errorf("register agent: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "registered agent %s/%s (%s), %s until the runtime announces it\n",
				a.Provider, a.Name, a.ID, a.Status)
			return err
		},
	}
	register.Flags().StringVar(&in.Provider, "provider", "", "adapter that runs the agent, e.g. claude_code, codex, echo (required)")
	register.Flags().StringVar(&in.Name, "name", "", "agent name, exactly as the runtime will announce it (required)")
	register.Flags().StringVar(&in.DisplayName, "display-name", "", "label shown in the dashboard")
	register.Flags().StringVar(&in.Description, "description", "", "what the agent is for")
	_ = register.MarkFlagRequired("provider")
	_ = register.MarkFlagRequired("name")

	list := &cobra.Command{
		Use:   "list",
		Short: "List the agents the server has for this runtime",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			agents, err := client(cmd).Agents(cmd.Context(), cfg)
			if err != nil {
				return fmt.Errorf("list agents: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(agents) == 0 {
				_, err = fmt.Fprintln(out, "no agents yet (agenthub agents register, or start the runtime to announce them)")
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "AGENT\tDISPLAY NAME\tSTATUS\tID")
			for _, a := range agents {
				display := "-"
				if a.DisplayName != nil {
					display = *a.DisplayName
				}
				_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\n", a.Provider, a.Name, display, a.Status, a.ID)
			}
			return tw.Flush()
		},
	}

	cmd.AddCommand(register, list)
	return cmd
}
