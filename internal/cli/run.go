package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/williamokano/kairos/internal/config"
)

// ErrUnattendedAckMissing is returned by --unattended when no local
// config carries an acknowledgement string — 05-gates.md: "kairos run
// --unattended refuses unless config contains
// unattended.iUnderstandEffectsWillNotBeConfirmed". Deliberately not a
// flag: a bare --yes that silently proceeds headless is the exact failure
// mode this exists to prevent, so the operator has to have set the string
// in config at some point, not merely typed a flag on this invocation.
var ErrUnattendedAckMissing = fmt.Errorf("cli: --unattended refuses to run without KAIROS_UNATTENDED_ACK set to a \"yes-<hostname>\"-shaped value in config")

func newRunCmd(app *appCtx) *cobra.Command {
	var params []string
	var unattended bool
	cmd := &cobra.Command{
		Use:   "run <file>",
		Short: "run a named workflow definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if unattended {
				cfg, err := config.Load()
				if err != nil {
					return fmt.Errorf("loading config: %w", err)
				}
				if !strings.HasPrefix(cfg.UnattendedAck, "yes-") {
					return ErrUnattendedAckMissing
				}
			}
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
			resp, err := client.CreateRun(ctx, args[0], paramsJSON, "")
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
	cmd.Flags().BoolVar(&unattended, "unattended", false, "run headless; refuses without KAIROS_UNATTENDED_ACK set in config")
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
