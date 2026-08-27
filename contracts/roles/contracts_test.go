package roles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type roleContract struct {
	ContractVersion int      `json:"contract_version"`
	Role            string   `json:"role"`
	SnapshotDigest  string   `json:"snapshot_sha256"`
	Tools           []string `json:"representative_tools"`
	ReleaseRequired bool     `json:"release_evidence_required"`
}

type manifest struct {
	SnapshotDigest string `json:"snapshot_sha256"`
	Tools          []struct {
		Tool string `json:"tool"`
	} `json:"tools"`
}

func TestRoleContractsReferenceReviewedTools(t *testing.T) {
	manifestData, err := os.ReadFile("../../coverage/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var coverage manifest
	if err := json.Unmarshal(manifestData, &coverage); err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool)
	for _, mapping := range coverage.Tools {
		known[mapping.Tool] = true
	}
	for _, role := range []string{"admin", "manager", "employee", "external"} {
		data, err := os.ReadFile(filepath.Join(role + ".json"))
		if err != nil {
			t.Fatal(err)
		}
		var contract roleContract
		if err := json.Unmarshal(data, &contract); err != nil {
			t.Fatal(err)
		}
		if contract.ContractVersion != 1 || contract.Role != role || !contract.ReleaseRequired {
			t.Fatalf("invalid %s role contract: %#v", role, contract)
		}
		if contract.SnapshotDigest != coverage.SnapshotDigest {
			t.Fatalf("%s contract uses stale snapshot digest", role)
		}
		if len(contract.Tools) == 0 {
			t.Fatalf("%s has no representative tools", role)
		}
		for _, tool := range contract.Tools {
			if !known[tool] {
				t.Errorf("%s references unknown tool %s", role, tool)
			}
		}
	}
}
