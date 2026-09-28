package methods

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestLockDurationDoublesToFourHours(t *testing.T) {
	cases := map[int32]time.Duration{
		0: 0,
		1: 15 * time.Minute,
		2: 30 * time.Minute,
		3: time.Hour,
		4: 2 * time.Hour,
		5: 4 * time.Hour,
		6: 4 * time.Hour,
	}
	for locks, want := range cases {
		if got := LockDuration(locks); got != want {
			t.Fatalf("LockDuration(%d) = %v, want %v", locks, got, want)
		}
	}
}

func TestRecordFailureWindow(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	status := v1alpha1.UserStatus{}
	for i := 0; i < 4; i++ {
		status = RecordFailure(status, 5, now.Add(time.Duration(i)*time.Minute))
	}
	if status.FailedAttempts != 4 || status.LockedUntil != nil {
		t.Fatalf("after 4 failures in the window: %+v", status)
	}
	status = RecordFailure(status, 5, now.Add(4*time.Minute))
	if status.FailedAttempts != 0 || status.Locks != 1 || status.LockedUntil == nil {
		t.Fatalf("the 5th failure must lock: %+v", status)
	}
	if !status.LockedUntil.Time.Equal(now.Add(4 * time.Minute).Add(15 * time.Minute)) {
		t.Fatalf("lockedUntil = %v", status.LockedUntil)
	}

	later := v1alpha1.UserStatus{FailedAttempts: 3, FailureWindowStart: &metav1.Time{Time: now}}
	afterWindow := RecordFailure(later, 5, now.Add(16*time.Minute))
	if afterWindow.FailedAttempts != 1 {
		t.Fatalf("a failure after the 15 minute window must restart the count: %+v", afterWindow)
	}
}

func TestRecordSuccessResets(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	status := v1alpha1.UserStatus{
		FailedAttempts:     4,
		FailureWindowStart: &metav1.Time{Time: now},
		LockedUntil:        &metav1.Time{Time: now.Add(time.Hour)},
		Locks:              2,
	}
	got := RecordSuccess(status, now)
	if got.FailedAttempts != 0 || got.FailureWindowStart != nil || got.LockedUntil != nil || got.Locks != 0 {
		t.Fatalf("RecordSuccess must reset every counter: %+v", got)
	}
	if got.LastLogin == nil || !got.LastLogin.Time.Equal(now) {
		t.Fatalf("RecordSuccess must set lastLogin: %+v", got.LastLogin)
	}
}
