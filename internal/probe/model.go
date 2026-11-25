package probe

import "sort"

var workloadKinds = map[string]bool{
	"apps/Deployment":        true,
	"apps/StatefulSet":       true,
	"apps/DaemonSet":         true,
	"batch/Job":              true,
	"/Pod":                   true,
	"/PersistentVolumeClaim": true,
}

func ModelledKinds() []string {
	out := make([]string, 0, len(workloadKinds)+len(conditionTable)+len(statelessKinds))
	for k := range workloadKinds {
		out = append(out, k)
	}
	for k := range conditionTable {
		out = append(out, k)
	}
	for k := range statelessKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
