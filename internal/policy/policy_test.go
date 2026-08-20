package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/policy"
)

func TestDecide_allowTierProceedsWithoutFriction(t *testing.T) {
	p := policy.Default()
	d := p.Decide("git.commit")
	if d.Tier != policy.Allow {
		t.Fatalf("Tier = %q, want allow", d.Tier)
	}
}

func TestDecide_confirmTierRequiresConfirmation(t *testing.T) {
	p := policy.Default()
	d := p.Decide("gh.pr.create")
	if d.Tier != policy.Confirm {
		t.Fatalf("Tier = %q, want confirm", d.Tier)
	}
}

func TestDecide_denyTierFailsWithAReason(t *testing.T) {
	p := policy.Default()
	d := p.Decide("gh.pr.merge")
	if d.Tier != policy.Deny {
		t.Fatalf("Tier = %q, want deny", d.Tier)
	}
	if d.Reason == "" {
		t.Fatal("Reason is empty, want the mandatory deny reason")
	}
}

func TestDecide_waiverGrantIsDeniedByDefault(t *testing.T) {
	d := policy.Default().Decide("waiver.grant")
	if d.Tier != policy.Deny {
		t.Fatalf("Tier = %q, want deny — an agent must never be able to waive its own gate", d.Tier)
	}
}

func TestDecide_unknownEffectDefaultsToDeny(t *testing.T) {
	d := policy.Default().Decide("some.effect.nobody.declared")
	if d.Tier != policy.Deny {
		t.Fatalf("Tier = %q, want deny — absence of a grant IS a denial", d.Tier)
	}
}

func TestDecide_wildcardRuleMatchesPrefix(t *testing.T) {
	p := policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
		"deploy.*": {Deny: "*", Reason: "no deploys"},
	}}
	d := p.Decide("deploy.aws")
	if d.Tier != policy.Deny {
		t.Fatalf("Tier = %q, want deny via wildcard match", d.Tier)
	}
}

func TestDecide_exactRuleBeatsWildcard(t *testing.T) {
	p := policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
		"deploy.*":     {Deny: "*", Reason: "no deploys"},
		"deploy.local": {Allow: "*"},
	}}
	d := p.Decide("deploy.local")
	if d.Tier != policy.Allow {
		t.Fatalf("Tier = %q, want allow — exact match must beat the wildcard rule", d.Tier)
	}
}

func TestLoad_missingFileReturnsDefault(t *testing.T) {
	p, err := policy.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Decide("gh.pr.merge").Tier != policy.Deny {
		t.Fatal("Load of a missing file should still deny gh.pr.merge, matching Default()")
	}
}

func TestLoad_parsesARealFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	content := `
default: deny
effects:
  fs.write: { allow: "*" }
  jira.transition: { confirm: each }
  terraform.apply: { deny: "*", reason: "No infrastructure mutation." }
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	p, err := policy.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Decide("fs.write").Tier != policy.Allow {
		t.Error("fs.write should be allow")
	}
	if p.Decide("jira.transition").Tier != policy.Confirm {
		t.Error("jira.transition should be confirm")
	}
	if p.Decide("terraform.apply").Tier != policy.Deny {
		t.Error("terraform.apply should be deny")
	}
}
