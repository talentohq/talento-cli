package schema

import "strings"

func BuildManifest(snapshot Snapshot, snapshotData []byte) Manifest {
	manifest := Manifest{
		ManifestVersion: 1,
		SnapshotVersion: snapshot.SnapshotVersion,
		SnapshotDigest:  SnapshotDigest(snapshotData),
		Endpoint:        snapshot.Endpoint,
		Tools:           make([]ToolMapping, 0, len(snapshot.Tools)),
		Resources:       make([]ResourceMapping, 0, len(snapshot.Resources)),
	}
	used := make(map[string]int)
	for _, tool := range snapshot.Tools {
		domain := domainFor(tool.Name)
		command := commandFor(domain, tool.Name)
		path := domain + " " + command
		if used[path] > 0 {
			command += "-" + tool.Name
		}
		used[domain+" "+command]++
		manifest.Tools = append(manifest.Tools, ToolMapping{
			Tool: tool.Name, Domain: domain, Command: command, ReadOnly: tool.Annotations.ReadOnlyHint,
		})
	}
	for _, resource := range snapshot.Resources {
		manifest.Resources = append(manifest.Resources, ResourceMapping{
			Resource: resource.Name, URITemplate: resource.URI, Command: "resources read",
		})
	}
	return manifest
}

func domainFor(name string) string {
	switch {
	case name == "confirm_action":
		return "action"
	case hasToken(name, "view", "views") || name == "create_version" || name == "list_versions":
		return "views"
	case strings.Contains(name, "training") || strings.Contains(name, "topic") || strings.Contains(name, "lesson") || strings.Contains(name, "segment"):
		return "trainings"
	case strings.Contains(name, "job_offer") || strings.Contains(name, "candidate"):
		return "recruitment"
	case strings.Contains(name, "onboarding") || strings.Contains(name, "boarding_action"):
		return "onboarding"
	case strings.Contains(name, "evaluation"):
		return "evaluations"
	case strings.Contains(name, "survey"):
		return "surveys"
	case strings.Contains(name, "competenc") || strings.Contains(name, "skill"):
		return "skills"
	case strings.Contains(name, "goal"):
		return "goals"
	case strings.Contains(name, "personal_todo"):
		return "todos"
	case strings.Contains(name, "task"):
		return "tasks"
	case strings.Contains(name, "absence"):
		return "absences"
	case strings.Contains(name, "expense"):
		return "expenses"
	case strings.Contains(name, "appointment"):
		return "appointments"
	case strings.Contains(name, "schedule") || strings.Contains(name, "reschedule") || strings.Contains(name, "availabilit"):
		return "schedules"
	case strings.Contains(name, "clock_in") || strings.Contains(name, "activity"):
		return "time"
	case strings.Contains(name, "employee") || strings.Contains(name, "job_categories"):
		return "people"
	case strings.Contains(name, "document"):
		return "documents"
	case strings.Contains(name, "customer"):
		return "customers"
	case strings.Contains(name, "contact"):
		return "contacts"
	case strings.Contains(name, "lead"):
		return "leads"
	case strings.Contains(name, "opportunit"):
		return "opportunities"
	case strings.Contains(name, "invoice") && !strings.Contains(name, "purchase"):
		return "invoices"
	case strings.Contains(name, "purchase"):
		return "purchases"
	case strings.Contains(name, "provider"):
		return "providers"
	case strings.Contains(name, "item"):
		return "items"
	case strings.Contains(name, "assignment") || strings.Contains(name, "crm"):
		return "crm"
	case strings.Contains(name, "changelog"):
		return "reports"
	default:
		return "projects"
	}
}

func hasToken(name string, candidates ...string) bool {
	for _, token := range strings.Split(name, "_") {
		for _, candidate := range candidates {
			if token == candidate {
				return true
			}
		}
	}
	return false
}

var commandOverrides = map[string]string{
	"confirm_action":             "confirm",
	"list_employees":             "list",
	"get_employee":               "get",
	"list_absence_categories":    "categories",
	"list_absences":              "list",
	"create_absence":             "create",
	"update_absence":             "update",
	"list_expense_categories":    "categories",
	"list_expenses":              "list",
	"create_expense":             "create",
	"list_schedules":             "list",
	"list_schedule_catalog":      "catalog",
	"manage_schedule":            "manage",
	"assign_schedule":            "assign",
	"get_employee_schedule":      "employee",
	"list_reschedules":           "reschedules",
	"create_reschedule":          "reschedule-create",
	"update_reschedule":          "reschedule-update",
	"swap_availabilities":        "swap",
	"list_appointments":          "list",
	"get_appointment":            "get",
	"create_appointment":         "create",
	"manage_appointment":         "manage",
	"list_appointment_slots":     "slots",
	"list_goals":                 "list",
	"create_goal":                "create",
	"edit_goal":                  "update",
	"list_goal_comments":         "comments",
	"create_goal_status_update":  "status-update",
	"list_skills":                "list",
	"list_employee_skills":       "employees",
	"list_trainings":             "list",
	"get_training":               "get",
	"create_training":            "create",
	"update_training":            "update",
	"delete_training":            "delete",
	"list_surveys":               "list",
	"create_survey":              "create",
	"get_survey_results":         "results",
	"list_evaluations":           "list",
	"create_evaluation":          "create",
	"get_evaluation_results":     "results",
	"list_job_offers":            "offers",
	"create_job_offer":           "offer-create",
	"update_job_offer":           "offer-update",
	"list_candidates":            "candidates",
	"move_candidate":             "candidate-move",
	"rate_candidate":             "candidate-rate",
	"list_onboardings":           "list",
	"create_onboarding_template": "template-create",
	"update_onboarding_template": "template-update",
	"update_boarding_action":     "action-update",
	"list_personal_todos":        "list",
	"create_personal_todo":       "create",
	"update_personal_todo":       "update",
	"delete_personal_todo":       "delete",
	"list_customers":             "list",
	"get_customer":               "get",
	"create_customer":            "create",
	"edit_customer":              "update",
	"list_contacts":              "list",
	"create_contact":             "create",
	"edit_contact":               "update",
	"list_leads":                 "list",
	"create_lead":                "create",
	"edit_lead":                  "update",
	"convert_lead":               "convert",
	"list_opportunities":         "list",
	"get_opportunity":            "get",
	"create_opportunity":         "create",
	"edit_opportunity":           "update",
	"close_opportunity":          "close",
	"reopen_opportunity":         "reopen",
	"list_providers":             "list",
	"create_provider":            "create",
	"edit_provider":              "update",
	"list_items":                 "list",
	"create_item":                "create",
	"edit_item":                  "update",
	"list_invoices":              "list",
	"get_invoice":                "get",
	"create_invoice":             "create",
	"edit_invoice":               "update",
	"send_invoice":               "send",
	"reopen_invoice":             "reopen",
	"list_views":                 "list",
	"list_versions":              "versions",
	"read_view":                  "read",
	"preview_view":               "preview",
	"edit_view":                  "edit",
	"write_view":                 "write",
	"create_version":             "version-create",
}

func commandFor(_ string, name string) string {
	if command, ok := commandOverrides[name]; ok {
		return command
	}
	return strings.ReplaceAll(name, "_", "-")
}
