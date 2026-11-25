// Package dashapi is what the dashboard serves: the views of the management cluster, as JSON, and
// a client to read them. It imports no Kubernetes package, so `rondo tui` reads the cluster through
// it without a Kubernetes client in the rondo binary (scripts/ci/check-rondo-deps.sh).
package dashapi

const (
	PathOverview    = "/api/overview"
	PathComponents  = "/api/components"
	PathEnrollments = "/api/enrollments"
	PathTargets     = "/api/targets"
	PathJobs        = "/api/jobs"
	PathServices    = "/api/services"
	// PathLogs takes `job` or `pod`, and `tail`.
	PathLogs = "/api/logs"
)

type Row struct {
	Name    string `json:"name"`
	Ready   string `json:"ready"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
}

type Summary struct {
	Kind  string `json:"kind"`
	Total int    `json:"total"`
	Ready int    `json:"ready"`
	Rows  []Row  `json:"rows"`
}

type PlacementView struct {
	Name   string `json:"name"`
	Ready  string `json:"ready"`
	Target string `json:"target"`
}

type ComponentView struct {
	Name        string          `json:"name"`
	Archetype   string          `json:"archetype"`
	Chart       string          `json:"chart,omitempty"`
	Version     string          `json:"version,omitempty"`
	Namespace   string          `json:"namespace,omitempty"`
	Groups      []string        `json:"groups,omitempty"`
	DependsOn   []string        `json:"dependsOn,omitempty"`
	Enrollments []PlacementView `json:"enrollments"`
}

type AttemptView struct {
	JobName    string `json:"jobName"`
	Result     string `json:"result"`
	Version    string `json:"version,omitempty"`
	Message    string `json:"message,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type EnrollmentView struct {
	Name                string        `json:"name"`
	Component           string        `json:"component"`
	Target              string        `json:"target"`
	Phase               string        `json:"phase,omitempty"`
	Ready               string        `json:"ready"`
	Message             string        `json:"message,omitempty"`
	Health              string        `json:"health,omitempty"`
	HealthMessage       string        `json:"healthMessage,omitempty"`
	ChartVersion        string        `json:"chartVersion,omitempty"`
	ValuesHash          string        `json:"valuesHash,omitempty"`
	InventoryCount      int           `json:"inventoryCount"`
	Attempts            int64         `json:"attempts"`
	ConsecutiveFailures int32         `json:"consecutiveFailures"`
	LastAttempt         *AttemptView  `json:"lastAttempt,omitempty"`
	History             []AttemptView `json:"history,omitempty"`
}

type TargetView struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	InCluster bool              `json:"inCluster"`
	Server    string            `json:"server,omitempty"`
	Ready     string            `json:"ready"`
	Message   string            `json:"message,omitempty"`
	Roles     []string          `json:"roles,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
}

type JobView struct {
	Name      string `json:"name"`
	Verb      string `json:"verb,omitempty"`
	Owner     string `json:"owner,omitempty"`
	State     string `json:"state"`
	Active    int32  `json:"active"`
	Succeeded int32  `json:"succeeded"`
	Failed    int32  `json:"failed"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
}

type PodView struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Ready     bool   `json:"ready"`
	Restarts  int32  `json:"restarts"`
	Node      string `json:"node,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type ServiceView struct {
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	Version   string    `json:"version"`
	Desired   int32     `json:"desired"`
	Ready     int32     `json:"ready"`
	Updated   int32     `json:"updated"`
	Available int32     `json:"available"`
	State     string    `json:"state"`
	Message   string    `json:"message"`
	Pods      []PodView `json:"pods"`
}
