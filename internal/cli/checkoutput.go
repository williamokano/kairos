package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/williamokano/kairos/internal/registry"
)

// newCheckOutputCmd is 04-agents.md's "kairos check-output": an actor
// running inside its own workspace validates its own output.json before
// finishing, in its own loop, with no permission prompt (it's in
// --allowedTools) and no daemon round trip — this is a local file check,
// not an API call, which is why it has no apispec.Op and is listed in
// internal/archtest's exemptCLIVerbs instead. Reads $KAIROS_OUTPUT/
// $KAIROS_SCHEMA from the env the engine already set (actor_llm.go), so
// it takes no flags.
func newCheckOutputCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check-output",
		Short: "validate $KAIROS_OUTPUT against $KAIROS_SCHEMA, printing one violation per line",
		RunE: func(cmd *cobra.Command, args []string) error {
			outputPath := os.Getenv("KAIROS_OUTPUT")
			schemaPath := os.Getenv("KAIROS_SCHEMA")
			if outputPath == "" || schemaPath == "" {
				return &usageError{msg: "KAIROS_OUTPUT and KAIROS_SCHEMA must both be set"}
			}

			valid, violations, err := registry.ValidateFile(outputPath, schemaPath)
			if err != nil {
				return err
			}
			if valid {
				return nil
			}
			for _, v := range violations {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), v)
			}
			return fmt.Errorf("%s does not validate against %s", outputPath, schemaPath)
		},
	}
}
