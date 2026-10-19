package harbor

import "fmt"

const (
	minKeepLastN   = 1
	maxKeepLastN   = 1000
	minRetainDays  = 1
	maxRetainDays  = 3650
	anyTagPattern  = "**"
	scheduleHourly = "hourly"
	scheduleDaily  = "daily"
	scheduleOff    = "off"
)

var scheduleCrons = map[string]string{
	scheduleHourly: "0 0 * * * *",
	scheduleDaily:  "0 0 0 * * *",
	scheduleOff:    "",
	"":             "",
}

type RetentionInput struct {
	KeepLastN      *int
	RetainDays     *int
	TagPattern     string
	RetainUntagged bool
	Schedule       string
}

type Retention struct {
	ID        int64             `json:"id,omitempty"`
	Algorithm string            `json:"algorithm"`
	Rules     []RetentionRule   `json:"rules"`
	Trigger   *RetentionTrigger `json:"trigger,omitempty"`
	Scope     *RetentionScope   `json:"scope,omitempty"`
}

type RetentionRule struct {
	ID             int64                          `json:"id,omitempty"`
	Priority       int                            `json:"priority"`
	Disabled       bool                           `json:"disabled"`
	Action         string                         `json:"action"`
	Template       string                         `json:"template"`
	Params         map[string]any                 `json:"params"`
	TagSelectors   []RetentionSelector            `json:"tag_selectors"`
	ScopeSelectors map[string][]RetentionSelector `json:"scope_selectors"`
}

type RetentionSelector struct {
	Kind       string `json:"kind"`
	Decoration string `json:"decoration"`
	Pattern    string `json:"pattern"`
	Extras     string `json:"extras,omitempty"`
}

type RetentionTrigger struct {
	Kind       string         `json:"kind"`
	Settings   map[string]any `json:"settings"`
	References map[string]any `json:"references,omitempty"`
}

type RetentionScope struct {
	Level string `json:"level"`
	Ref   int64  `json:"ref"`
}

func BuildRetention(in RetentionInput) (Retention, error) {
	if in.KeepLastN != nil && (*in.KeepLastN < minKeepLastN || *in.KeepLastN > maxKeepLastN) {
		return Retention{}, fmt.Errorf("keepLastN must be between %d and %d", minKeepLastN, maxKeepLastN)
	}
	if in.RetainDays != nil && (*in.RetainDays < minRetainDays || *in.RetainDays > maxRetainDays) {
		return Retention{}, fmt.Errorf("retainDays must be between %d and %d", minRetainDays, maxRetainDays)
	}
	cron, known := scheduleCrons[in.Schedule]
	if !known {
		return Retention{}, fmt.Errorf("schedule %q is not one of %s, %s, %s", in.Schedule, scheduleOff, scheduleHourly, scheduleDaily)
	}
	pattern := in.TagPattern
	if pattern == "" {
		pattern = anyTagPattern
	}
	rules := []RetentionRule{}
	if in.KeepLastN != nil {
		rules = append(rules, retentionRule("latestPushedK", *in.KeepLastN, pattern, in.RetainUntagged))
	}
	if in.RetainDays != nil {
		rules = append(rules, retentionRule("nDaysSinceLastPush", *in.RetainDays, pattern, in.RetainUntagged))
	}
	trigger := &RetentionTrigger{Kind: "Schedule", Settings: map[string]any{"cron": cron}}
	return Retention{Algorithm: "or", Rules: rules, Trigger: trigger}, nil
}

func retentionRule(template string, value int, pattern string, untagged bool) RetentionRule {
	return RetentionRule{
		Action:   "retain",
		Template: template,
		Params:   map[string]any{template: value},
		TagSelectors: []RetentionSelector{{
			Kind:       "doublestar",
			Decoration: "matches",
			Pattern:    pattern,
			Extras:     fmt.Sprintf(`{"untagged":%t}`, untagged),
		}},
		ScopeSelectors: map[string][]RetentionSelector{
			"repository": {{Kind: "doublestar", Decoration: "repoMatches", Pattern: anyTagPattern}},
		},
	}
}
