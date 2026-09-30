package schema

import "testing"

func TestDomainForDoesNotTreatReviewAsView(t *testing.T) {
	if got, want := domainFor("submit_training_for_review"), "trainings"; got != want {
		t.Fatalf("domainFor(submit_training_for_review) = %q, want %q", got, want)
	}
}

func TestDomainForViewTools(t *testing.T) {
	for _, name := range []string{"list_views", "read_view", "preview_view", "edit_view", "write_view"} {
		t.Run(name, func(t *testing.T) {
			if got, want := domainFor(name), "views"; got != want {
				t.Fatalf("domainFor(%s) = %q, want %q", name, got, want)
			}
		})
	}
}

func TestDomainForCompanyClockIsTime(t *testing.T) {
	if got, want := domainFor("get_current_datetime"), "time"; got != want {
		t.Fatalf("domainFor(get_current_datetime) = %q, want %q", got, want)
	}
}

func TestNewGatewayToolsMapToStableCommands(t *testing.T) {
	tests := []struct {
		tool, domain, command string
	}{
		{"list_meeting_templates", "meetings", "list"},
		{"create_meeting_template", "meetings", "create"},
		{"update_meeting_template", "meetings", "update"},
		{"delete_meeting_template", "meetings", "delete"},
		{"return_training_to_draft", "trainings", "return-to-draft"},
		{"edit_crm_custom_field", "crm", "edit-crm-custom-field"},
		{"record_lead_custom_field_observation", "leads", "record-custom-field-observation"},
		{"list_lead_custom_field_observations", "leads", "list-custom-field-observations"},
		{"get_current_datetime", "time", "now"},
		{"list_goal_actions", "goals", "list-actions"},
		{"create_goal_action", "goals", "create-action"},
		{"update_goal_action", "goals", "update-action"},
		{"delete_goal_action", "goals", "delete-action"},
		{"create_candidate", "recruitment", "candidate-create"},
		{"add_candidate_skill", "recruitment", "candidate-skill-add"},
		{"update_candidate_skill", "recruitment", "candidate-skill-update"},
		{"remove_candidate_skill", "recruitment", "candidate-skill-remove"},
	}
	for _, test := range tests {
		t.Run(test.tool, func(t *testing.T) {
			if got, want := domainFor(test.tool), test.domain; got != want {
				t.Fatalf("domainFor(%s) = %q, want %q", test.tool, got, want)
			}
			if got, want := commandFor(test.domain, test.tool), test.command; got != want {
				t.Fatalf("commandFor(%s) = %q, want %q", test.tool, got, want)
			}
		})
	}
}
