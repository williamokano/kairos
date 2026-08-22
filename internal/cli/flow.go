package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// newFlowCmd is `kairos flow create/ls/run` — the durable answer to
// "there is no way to create a workflow definition anywhere in this
// system": until now a workflow could only be REFERENCED by a file path
// that already existed on disk (kairos run <file>). create saves a real
// one through internal/api's POST /flow-definitions, which validates via
// the exact same registry.Load path any hand-authored file already goes
// through — a bad workflow is rejected here with the real error, not
// silently written.
func newFlowCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{Use: "flow", Short: "manage saved workflow definitions"}

	var file string
	var fromStdin bool
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "save a new workflow definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" && !fromStdin {
				return &usageError{msg: "either --file <path> or --from-stdin is required"}
			}
			if file != "" && fromStdin {
				return &usageError{msg: "--file and --from-stdin are mutually exclusive"}
			}
			var data []byte
			var err error
			if fromStdin {
				data, err = io.ReadAll(cmd.InOrStdin())
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return fmt.Errorf("reading workflow yaml: %w", err)
			}
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			flow, err := client.CreateFlowDefinition(ctx, args[0], string(data))
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), flow)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", flow.Name, flow.Path)
			return err
		},
	}
	create.Flags().StringVar(&file, "file", "", "path to the workflow YAML to save")
	create.Flags().BoolVar(&fromStdin, "from-stdin", false, "read the workflow YAML from stdin instead of --file")
	root.AddCommand(create)

	root.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "list saved workflow definitions",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			flows, err := client.ListFlowDefinitions(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), flows)
			}
			tw := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(tw, "NAME\tPATH")
			for _, f := range flows {
				_, _ = fmt.Fprintf(tw, "%s\t%s\n", f.Name, f.Path)
			}
			return tw.Flush()
		},
	})

	var params []string
	run := &cobra.Command{
		Use:   "run <name>",
		Short: "start a run from a saved workflow definition, by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			flow, err := client.GetFlowDefinition(ctx, args[0])
			if err != nil {
				return err
			}
			// Dispatches through the IDENTICAL CreateRun path any
			// hand-run `kairos run <file>` already uses — resolving a
			// saved flow's name to its real path is all this does, no
			// special-cased run mechanism.
			paramMap := map[string]string{}
			for _, kv := range params {
				k, v, ok := splitKV(kv)
				if !ok {
					return &usageError{msg: "invalid --param, want k=v: " + kv}
				}
				paramMap[k] = v
			}
			paramsJSON, err := json.Marshal(paramMap)
			if err != nil {
				return err
			}
			resp, err := client.CreateRun(ctx, flow.Path, paramsJSON, "")
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp.RunID, resp.Status)
			return err
		},
	}
	run.Flags().StringArrayVar(&params, "param", nil, "workflow param, k=v (repeatable)")
	root.AddCommand(run)

	return root
}
