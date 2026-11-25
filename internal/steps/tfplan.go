package steps

import "encoding/json"

type PlanResult struct {
	Changed  bool
	Destroys []string
}

func (p PlanResult) Destructive() bool { return len(p.Destroys) > 0 }

func planSummary(showJSON string) (PlanResult, error) {
	var p struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(showJSON), &p); err != nil {
		return PlanResult{}, err
	}
	var res PlanResult
	for _, rc := range p.ResourceChanges {
		if isNoOp(rc.Change.Actions) {
			continue
		}
		res.Changed = true
		for _, a := range rc.Change.Actions {
			if a == "delete" {
				res.Destroys = append(res.Destroys, rc.Address)
				break
			}
		}
	}
	return res, nil
}

func isNoOp(actions []string) bool {
	if len(actions) == 0 {
		return true
	}
	for _, a := range actions {
		if a != "no-op" && a != "read" {
			return false
		}
	}
	return true
}
