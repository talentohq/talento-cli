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
