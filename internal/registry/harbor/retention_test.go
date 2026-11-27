package harbor

import (
	"encoding/json"
	"testing"
)

func TestBuildRetentionGolden(t *testing.T) {
	tests := []struct {
		name  string
		input RetentionInput
		want  string
	}{
		{
			"keep last and days daily",
			RetentionInput{KeepLastN: ptr(10), RetainDays: ptr(30), TagPattern: "v*", RetainUntagged: true, Schedule: "daily"},
			`{"algorithm":"or","rules":[{"priority":0,"disabled":false,"action":"retain","template":"latestPushedK","params":{"latestPushedK":10},"tag_selectors":[{"kind":"doublestar","decoration":"matches","pattern":"v*","extras":"{\"untagged\":true}"}],"scope_selectors":{"repository":[{"kind":"doublestar","decoration":"repoMatches","pattern":"**"}]}},{"priority":0,"disabled":false,"action":"retain","template":"nDaysSinceLastPush","params":{"nDaysSinceLastPush":30},"tag_selectors":[{"kind":"doublestar","decoration":"matches","pattern":"v*","extras":"{\"untagged\":true}"}],"scope_selectors":{"repository":[{"kind":"doublestar","decoration":"repoMatches","pattern":"**"}]}}],"trigger":{"kind":"Schedule","settings":{"cron":"0 0 0 * * *"}}}`,
		},
		{
			"keep last hourly default pattern",
			RetentionInput{KeepLastN: ptr(3), Schedule: "hourly"},
			`{"algorithm":"or","rules":[{"priority":0,"disabled":false,"action":"retain","template":"latestPushedK","params":{"latestPushedK":3},"tag_selectors":[{"kind":"doublestar","decoration":"matches","pattern":"**","extras":"{\"untagged\":false}"}],"scope_selectors":{"repository":[{"kind":"doublestar","decoration":"repoMatches","pattern":"**"}]}}],"trigger":{"kind":"Schedule","settings":{"cron":"0 0 * * * *"}}}`,
		},
		{
			"empty clears the rules",
			RetentionInput{},
			`{"algorithm":"or","rules":[],"trigger":{"kind":"Schedule","settings":{"cron":""}}}`,
		},
		{
			"off has no cron",
			RetentionInput{RetainDays: ptr(7), Schedule: "off"},
			`{"algorithm":"or","rules":[{"priority":0,"disabled":false,"action":"retain","template":"nDaysSinceLastPush","params":{"nDaysSinceLastPush":7},"tag_selectors":[{"kind":"doublestar","decoration":"matches","pattern":"**","extras":"{\"untagged\":false}"}],"scope_selectors":{"repository":[{"kind":"doublestar","decoration":"repoMatches","pattern":"**"}]}}],"trigger":{"kind":"Schedule","settings":{"cron":""}}}`,
		},
	}
	for _, test := range tests {
		retention, err := BuildRetention(test.input)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		raw, _ := json.Marshal(retention)
		if string(raw) != test.want {
			t.Errorf("%s:\n got %s\nwant %s", test.name, raw, test.want)
		}
	}
}

func TestBuildRetentionLimits(t *testing.T) {
	for _, input := range []RetentionInput{
		{KeepLastN: ptr(0)},
		{KeepLastN: ptr(1001)},
		{RetainDays: ptr(0)},
		{RetainDays: ptr(3651)},
		{Schedule: "weekly"},
	} {
		if _, err := BuildRetention(input); err == nil {
			t.Errorf("no error for %+v", input)
		}
	}
	for _, input := range []RetentionInput{{KeepLastN: ptr(1)}, {KeepLastN: ptr(1000)}, {RetainDays: ptr(1)}, {RetainDays: ptr(3650)}} {
		if _, err := BuildRetention(input); err != nil {
			t.Errorf("error for %+v: %v", input, err)
		}
	}
}
