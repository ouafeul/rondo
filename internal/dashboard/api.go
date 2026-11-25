package dashboard

import "rondo/internal/dashapi"

// The views are dashapi's, which `rondo tui` reads without this package's Kubernetes client.
type (
	Row            = dashapi.Row
	Summary        = dashapi.Summary
	PlacementView  = dashapi.PlacementView
	ComponentView  = dashapi.ComponentView
	AttemptView    = dashapi.AttemptView
	EnrollmentView = dashapi.EnrollmentView
	TargetView     = dashapi.TargetView
	JobView        = dashapi.JobView
	PodView        = dashapi.PodView
	ServiceView    = dashapi.ServiceView
)
