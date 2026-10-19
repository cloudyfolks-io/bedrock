package database

import (
	"slices"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var operationsNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func timeAgo(age time.Duration) *time.Time {
	at := operationsNow.Add(-age)
	return &at
}

func classSpec(minGi, maxGi int32, highAvailability bool) v1alpha1.DatabaseClassSpec {
	return v1alpha1.DatabaseClassSpec{MinStorageGi: minGi, MaxStorageGi: maxGi, HighAvailability: highAvailability, Enabled: true}
}

func appliedState() v1alpha1.DatabaseApplied {
	return v1alpha1.DatabaseApplied{Class: "small", Version: "16", StorageGi: 20}
}

func desiredSpec(edit func(*v1alpha1.DatabaseSpec)) v1alpha1.DatabaseSpec {
	spec := v1alpha1.DatabaseSpec{Engine: v1alpha1.DatabaseEnginePostgreSQL, Version: "16", Class: "small", StorageGi: 20}
	if edit != nil {
		edit(&spec)
	}
	return spec
}

func baseFacts() OperationFacts {
	return OperationFacts{
		PreviousClass:    classSpec(10, 500, false),
		Class:            classSpec(10, 500, false),
		ExpansionAllowed: true,
		Upgrade:          UpgradeFacts{Targets: []string{"17", "18"}, MaxAge: 26 * time.Hour, Now: operationsNow},
	}
}

func TestPlanStepsAndRefusals(t *testing.T) {
	cases := []struct {
		name        string
		applied     v1alpha1.DatabaseApplied
		spec        v1alpha1.DatabaseSpec
		facts       func(*OperationFacts)
		annotations map[string]string
		steps       []Step
		refusal     string
		hold        string
		backup      bool
	}{
		{name: "nothing changed", spec: desiredSpec(nil)},
		{name: "class change", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Class = "medium" }), steps: []Step{StepResize}},
		{name: "class change that loses high availability", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Class = "ha" }),
			facts: func(f *OperationFacts) { f.Class.HighAvailability = true }, refusal: ReasonClassChangeInvalid},
		{name: "storage growth", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.StorageGi = 40 }), steps: []Step{StepGrowStorage}},
		{name: "class and storage together", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Class = "medium"; s.StorageGi = 40 }), steps: []Step{StepResize, StepGrowStorage}},
		{name: "storage above the class limit", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.StorageGi = 501 }), refusal: ReasonStorageOutOfRange},
		{name: "storage below the class minimum after a class change", applied: v1alpha1.DatabaseApplied{Class: "small", Version: "16", StorageGi: 20},
			spec:  desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Class = "big"; s.StorageGi = 20 }),
			facts: func(f *OperationFacts) { f.Class = classSpec(100, 500, false) }, refusal: ReasonStorageOutOfRange},
		{name: "storage class cannot expand", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.StorageGi = 40 }),
			facts: func(f *OperationFacts) { f.ExpansionAllowed = false }, refusal: ReasonExpansionNotSupported},
		{name: "same storage on a class that cannot expand", spec: desiredSpec(nil), facts: func(f *OperationFacts) { f.ExpansionAllowed = false }},
		{name: "version upgrade with a fresh backup", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }),
			facts: func(f *OperationFacts) { f.Upgrade.LatestBackup = timeAgo(time.Hour) }, steps: []Step{StepUpgrade}},
		{name: "version upgrade with a backup at the limit", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }),
			facts: func(f *OperationFacts) { f.Upgrade.LatestBackup = timeAgo(26 * time.Hour) }, steps: []Step{StepUpgrade}},
		{name: "version upgrade with an old backup", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }),
			facts: func(f *OperationFacts) { f.Upgrade.LatestBackup = timeAgo(27 * time.Hour) }, hold: ReasonBackupRequired, backup: true},
		{name: "version upgrade without a backup", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }), hold: ReasonBackupRequired, backup: true},
		{name: "version upgrade with a backup already requested", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }),
			facts: func(f *OperationFacts) { f.Upgrade.PreRequested = true }, hold: ReasonBackupRequired},
		{name: "version upgrade of valkey needs no backup", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Engine = v1alpha1.DatabaseEngineValkey; s.Version = "9.1" }),
			applied: v1alpha1.DatabaseApplied{Class: "small", Version: "9.0", StorageGi: 20},
			facts:   func(f *OperationFacts) { f.Upgrade.Targets = []string{"9.1"} }, steps: []Step{StepUpgrade}},
		{name: "version not in the target list", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "15" }), refusal: ReasonVersionNotAllowed},
		{name: "downgrade", applied: v1alpha1.DatabaseApplied{Class: "small", Version: "17", StorageGi: 20},
			spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "16" }), refusal: ReasonVersionNotAllowed},
		{name: "restart token", spec: desiredSpec(nil), annotations: map[string]string{v1alpha1.AnnotationRestart: "t1"}, steps: []Step{StepRestart}},
		{name: "restart token already applied", applied: v1alpha1.DatabaseApplied{Class: "small", Version: "16", StorageGi: 20, RestartToken: "t1"},
			spec: desiredSpec(nil), annotations: map[string]string{v1alpha1.AnnotationRestart: "t1"}},
		{name: "empty restart token", spec: desiredSpec(nil), annotations: map[string]string{v1alpha1.AnnotationRestart: ""}},
		{name: "refresh annotation without a new token", applied: v1alpha1.DatabaseApplied{Class: "small", Version: "16", StorageGi: 20, RestartToken: "t1"},
			spec: desiredSpec(nil), annotations: map[string]string{v1alpha1.AnnotationRestart: "t1", v1alpha1.AnnotationRefreshImage: "true"}},
		{name: "refresh annotation with a new token", spec: desiredSpec(nil),
			annotations: map[string]string{v1alpha1.AnnotationRestart: "t2", v1alpha1.AnnotationRefreshImage: "true"}, steps: []Step{StepRefreshImage, StepRestart}},
		{name: "refresh annotation set to false", spec: desiredSpec(nil),
			annotations: map[string]string{v1alpha1.AnnotationRestart: "t2", v1alpha1.AnnotationRefreshImage: "false"}, steps: []Step{StepRestart}},
		{name: "suspend", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Suspended = true }), steps: []Step{StepSuspend}},
		{name: "resume", applied: v1alpha1.DatabaseApplied{Class: "small", Version: "16", StorageGi: 20, Suspended: true},
			spec: desiredSpec(nil), steps: []Step{StepResume}},
		{name: "a refusal blocks the other steps", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "15"; s.StorageGi = 40 }), refusal: ReasonVersionNotAllowed},
		{name: "a hold keeps the other steps", spec: desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17"; s.StorageGi = 40 }),
			hold: ReasonBackupRequired, backup: true, steps: []Step{StepGrowStorage}},
	}
	for _, c := range cases {
		applied := c.applied
		if applied.Version == "" {
			applied = appliedState()
		}
		facts := baseFacts()
		if c.facts != nil {
			c.facts(&facts)
		}
		got := Plan(applied, c.spec, facts, c.annotations)
		if !slices.Equal(got.Steps, c.steps) {
			t.Errorf("%s: steps %v, want %v", c.name, got.Steps, c.steps)
		}
		if reason := reasonOf(got.Refusal); reason != c.refusal {
			t.Errorf("%s: refusal %q, want %q", c.name, reason, c.refusal)
		}
		if reason := reasonOf(got.Hold); reason != c.hold {
			t.Errorf("%s: hold %q, want %q", c.name, reason, c.hold)
		}
		if got.RequestBackup != c.backup {
			t.Errorf("%s: request backup %v, want %v", c.name, got.RequestBackup, c.backup)
		}
		if got.Refusal != nil && got.Refusal.Message == "" || got.Hold != nil && got.Hold.Message == "" {
			t.Errorf("%s: a reason without a message", c.name)
		}
	}
}

func reasonOf(refusal *Refusal) string {
	if refusal == nil {
		return ""
	}
	return refusal.Reason
}

func TestImageToApply(t *testing.T) {
	const running, catalog, other = "engine@sha256:aaa", "engine@sha256:bbb", "engine@sha256:ccc"
	cases := []struct {
		name    string
		status  string
		applied string
		version string
		catalog string
		refresh bool
		want    string
	}{
		{"first apply uses the catalog", "", "", "16", catalog, false, catalog},
		{"a catalog change does not roll a running database", running, "16", "16", catalog, false, running},
		{"the refresh applies the catalog", running, "16", "16", catalog, true, catalog},
		{"a version change applies the catalog of the new version", running, "16", "17", other, false, other},
		{"a refresh without a change keeps the catalog", catalog, "16", "16", catalog, true, catalog},
	}
	for _, c := range cases {
		if got := ImageToApply(c.status, c.applied, c.version, c.catalog, c.refresh); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestPreUpgradeBackupName(t *testing.T) {
	if got := PreUpgradeBackupName("uid-1", operationsNow); got != "uid-1-pre-1792411200" {
		t.Fatalf("name %s", got)
	}
}

func TestSecondReconcileDoesNotRequestASecondBackup(t *testing.T) {
	facts := baseFacts()
	first := Plan(appliedState(), desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }), facts, nil)
	if !first.RequestBackup {
		t.Fatal("the first plan did not request a backup")
	}
	facts.Upgrade.PreRequested = true
	second := Plan(appliedState(), desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }), facts, nil)
	if second.RequestBackup || second.Hold == nil {
		t.Fatalf("second plan %+v", second)
	}
	facts.Upgrade.LatestBackup = timeAgo(time.Minute)
	third := Plan(appliedState(), desiredSpec(func(s *v1alpha1.DatabaseSpec) { s.Version = "17" }), facts, nil)
	if !slices.Equal(third.Steps, []Step{StepUpgrade}) || third.Hold != nil || third.RequestBackup {
		t.Fatalf("third plan %+v", third)
	}
}

func TestOperationReasonsListEveryOperationReason(t *testing.T) {
	listed := append(Reasons(), OperationReasons()...)
	for _, want := range []string{ReasonClassChangeInvalid, ReasonStorageOutOfRange, ReasonExpansionNotSupported, ReasonVersionNotAllowed, ReasonBackupRequired} {
		if !slices.Contains(listed, want) {
			t.Errorf("reason %s is not listed", want)
		}
	}
}
