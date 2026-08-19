package registry

import (
	"os"
	"strings"
	"testing"
)

func TestLoad_fixIssueProducesExpectedDefinition(t *testing.T) {
	def, err := Load("testdata/fix-issue.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if def.Name != "fix-issue" {
		t.Errorf("Name = %q, want fix-issue", def.Name)
	}
	if len(def.Nodes) != 4 {
		t.Fatalf("len(Nodes) = %d, want 4", len(def.Nodes))
	}
	wantIDs := []string{"plan", "implement", "approve", "pr"}
	for i, want := range wantIDs {
		if string(def.Nodes[i].ID) != want {
			t.Errorf("Nodes[%d].ID = %q, want %q", i, def.Nodes[i].ID, want)
		}
	}
	implement := def.Nodes[1]
	if implement.Workspace != WorkspaceWrite {
		t.Errorf("implement.Workspace = %q, want write", implement.Workspace)
	}
	if implement.Retry.MaxAttempts != 2 {
		t.Errorf("implement.Retry.MaxAttempts = %d, want 2 (write + agent actor default)", implement.Retry.MaxAttempts)
	}
	if len(implement.Gates) != 5 {
		t.Errorf("implement.Gates = %v, want 5 entries", implement.Gates)
	}
	if implement.OutputSchema == nil {
		t.Error("implement.OutputSchema is nil, want compiled")
	}
	if implement.Inputs["tasks"].Path != "$.outputs.plan.tasks" {
		t.Errorf("implement.Inputs[tasks].Path = %q", implement.Inputs["tasks"].Path)
	}

	plan := def.Nodes[0]
	if plan.Timeout.String() != "30m0s" {
		t.Errorf("plan.Timeout = %v, want 30m0s (default)", plan.Timeout)
	}
	if plan.Retry.MaxAttempts != 1 {
		t.Errorf("plan.Retry.MaxAttempts = %d, want 1 (read workspace default)", plan.Retry.MaxAttempts)
	}

	if def.ParamsSchema == nil {
		t.Error("ParamsSchema is nil, want compiled from params: { issue: int! }")
	}
}

func TestLoad_ciWatchProjectsAWaitWithBothPollAndWebhookSources(t *testing.T) {
	def, err := Load("testdata/ci-watch.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ciWatch := def.Nodes[1]
	if ciWatch.Wait == nil {
		t.Fatal("expected ci-watch node to have a Wait")
	}
	if len(ciWatch.Wait.On) != 2 {
		t.Fatalf("Wait.On = %v, want 2 sources (poll, webhook)", ciWatch.Wait.On)
	}
	if ciWatch.Wait.On[0].Kind != WaitPoll {
		t.Errorf("Wait.On[0].Kind = %q, want poll", ciWatch.Wait.On[0].Kind)
	}
	if ciWatch.Wait.OnTimeout != "escalate" {
		t.Errorf("Wait.OnTimeout = %q, want escalate", ciWatch.Wait.OnTimeout)
	}
	if ciWatch.Wait.Timeout.String() != "3h0m0s" {
		t.Errorf("Wait.Timeout = %v, want 3h0m0s", ciWatch.Wait.Timeout)
	}
}

func TestLoad_humanApprovalDefaultsOnTimeoutToPark(t *testing.T) {
	def, err := Load("testdata/human-approval.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	approve := def.Nodes[1]
	if approve.Wait.OnTimeout != "park" {
		t.Errorf("Wait.OnTimeout = %q, want park", approve.Wait.OnTimeout)
	}
}

func TestLoad_rejectsNetworkEgress(t *testing.T) {
	_, err := Load("testdata/invalid/network-egress.yaml")
	if err == nil {
		t.Fatal("expected network.egress to be rejected")
	}
	if got := err.Error(); !strings.Contains(got, "network.egress") {
		t.Errorf("error = %q, want it to name network.egress", got)
	}
}

func TestLoad_rejectsDeletedFields(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{"testdata/invalid/deleted-worker.yaml", "worker"},
		{"testdata/invalid/deleted-resources-cpu.yaml", "resources.cpu"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			_, err := Load(tt.file)
			if err == nil {
				t.Fatalf("expected %s to be rejected", tt.file)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to name %q", err.Error(), tt.want)
			}
		})
	}
}

func TestLoad_requiresOutputSchemaForAgentActors(t *testing.T) {
	_, err := Load("testdata/invalid/missing-output-schema.yaml")
	if err == nil {
		t.Fatal("expected missing output/outputSchema on a claude-actor node to be rejected")
	}
}

func TestLoad_requiresOnTimeoutWhenWaitIsPresent(t *testing.T) {
	_, err := Load("testdata/invalid/missing-on-timeout.yaml")
	if err == nil {
		t.Fatal("expected a wait: block with no onTimeout to be rejected")
	}
}

func TestLoadBytes_readsSameContentAsLoad(t *testing.T) {
	data, err := os.ReadFile("testdata/fix-issue.yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	def, err := LoadBytes(data, "fix-issue.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Name != "fix-issue" {
		t.Errorf("Name = %q, want fix-issue", def.Name)
	}
}
