package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newRunCmd(app *appCtx) *cobra.Command {
	var params []string
	cmd := &cobra.Command{
		Use:   "run <file>",
		Short: "run a named workflow definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
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
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			resp, err := client.CreateRun(ctx, args[0], paramsJSON)
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
	cmd.Flags().StringArrayVar(&params, "param", nil, "workflow param, k=v (repeatable)")
	return cmd
}

func splitKV(s string) (k, v string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}
