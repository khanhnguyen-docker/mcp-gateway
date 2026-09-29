package commands

import (
	"github.com/spf13/cobra"

	catalognext "github.com/docker/mcp-gateway/pkg/catalog_next"
	"github.com/docker/mcp-gateway/pkg/db"
	"github.com/docker/mcp-gateway/pkg/oci"
)

func skillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "skill",
		Aliases: []string{"skills"},
		Short:   "Manage Agent Skills distributed through OCI catalogs",
		Long: `Manage Agent Skills (SEP-2640). Skills are pushed inside a catalog artifact,
pulled into the local store, and served by the gateway once added.
See docs/skills.md.`,
	}

	cmd.AddCommand(validateSkillCommand())
	cmd.AddCommand(pushSkillCommand())
	cmd.AddCommand(pullSkillCommand())
	cmd.AddCommand(listSkillCommand())
	cmd.AddCommand(addSkillCommand())
	cmd.AddCommand(removeSkillCommand())

	return cmd
}

func validateSkillCommand() *cobra.Command {
	var publisher string
	cmd := &cobra.Command{
		Use:   "validate <dir>",
		Short: "Validate a skill directory and print its entries as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return catalognext.ValidateSkills(args[0], publisher)
		},
	}
	cmd.Flags().StringVar(&publisher, "publisher", "", "Publisher segment of the skill URIs (default: local)")
	return cmd
}

func pushSkillCommand() *cobra.Command {
	var opts struct {
		Publisher string
		Title     string
	}
	cmd := &cobra.Command{
		Use:   "push <dir> <oci-reference>",
		Short: "Validate a skill directory and push it as a catalog artifact",
		Example: `  # Push one skill, publisher derived from the repository namespace (myorg)
  docker mcp skill push ./refunds myorg/skills:v1

  # Push a directory of skills with an explicit publisher
  docker mcp skill push ./skills localhost:5000/skills:v1 --publisher acme`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return catalognext.PushSkills(cmd.Context(), args[0], args[1], opts.Publisher, opts.Title)
		},
	}
	cmd.Flags().StringVar(&opts.Publisher, "publisher", "", "Publisher segment of the skill URIs (default: repository namespace)")
	cmd.Flags().StringVar(&opts.Title, "title", "", "Catalog title (default: \"<publisher> skills\")")
	return cmd
}

func pullSkillCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <oci-reference>",
		Short: "Pull a catalog and its skill files into the local store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dao, err := db.New()
			if err != nil {
				return err
			}
			return catalognext.Pull(cmd.Context(), dao, oci.NewService(), args[0])
		},
	}
}

func listSkillCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List added skills and whether the catalog still matches (ok, changed, missing)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dao, err := db.New()
			if err != nil {
				return err
			}
			return catalognext.ListSkills(cmd.Context(), dao, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func addSkillCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add <catalog-reference> <name-or-uri>",
		Short: "Approve a skill from a pulled catalog; records its manifest digest",
		Example: `  docker mcp skill add myorg/skills:v1 refunds
  docker mcp skill add myorg/skills:v1 skill://acme/refunds/SKILL.md`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dao, err := db.New()
			if err != nil {
				return err
			}
			return catalognext.AddSkill(cmd.Context(), dao, args[0], args[1], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func removeSkillCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name-or-uri>",
		Aliases: []string{"remove"},
		Short:   "Remove an added skill",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dao, err := db.New()
			if err != nil {
				return err
			}
			return catalognext.RemoveSkill(cmd.Context(), dao, args[0])
		},
	}
}
