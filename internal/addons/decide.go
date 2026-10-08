package addons

type Action string

const (
	ActionApply Action = "apply"
	ActionPrune Action = "prune"
	ActionHold  Action = "hold"
)

type DecisionInput struct {
	Known          bool
	Enabled        bool
	ValuesErr      error
	Verdict        Verdict
	ClusterSettled bool
}

type Decision struct {
	Action  Action
	Reason  string
	Message string
}

func Decide(in DecisionInput) Decision {
	switch {
	case !in.Known:
		return Decision{Action: ActionHold, Reason: "UnknownAddon", Message: "no addon with this name exists in this release"}
	case !in.ClusterSettled:
		return Decision{Action: ActionHold, Reason: "WaitingForCluster", Message: "the cluster has no settled version yet"}
	case in.ValuesErr != nil:
		return Decision{Action: ActionHold, Reason: "InvalidValues", Message: in.ValuesErr.Error()}
	case !in.Enabled:
		return Decision{Action: ActionPrune, Reason: "Disabled", Message: "the addon is disabled"}
	case in.Verdict.State == VerdictFailed:
		return Decision{Action: ActionPrune, Reason: "PreflightFailed", Message: in.Verdict.Message}
	case in.Verdict.State == VerdictUnknown:
		return Decision{Action: ActionHold, Reason: "PreflightPending", Message: in.Verdict.Message}
	}
	return Decision{Action: ActionApply}
}
