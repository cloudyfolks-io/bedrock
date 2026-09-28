package methods

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const failureWindow = 15 * time.Minute

const baseLockDuration = 15 * time.Minute

const maxLockDuration = 4 * time.Hour

func Locked(status v1alpha1.UserStatus, now time.Time) bool {
	return status.LockedUntil != nil && now.Before(status.LockedUntil.Time)
}

func LockDuration(locks int32) time.Duration {
	if locks < 1 {
		return 0
	}
	d := baseLockDuration
	for i := int32(1); i < locks; i++ {
		d *= 2
		if d >= maxLockDuration {
			return maxLockDuration
		}
	}
	return d
}

func RecordFailure(status v1alpha1.UserStatus, threshold int, now time.Time) v1alpha1.UserStatus {
	next := status
	if next.FailureWindowStart == nil || now.Sub(next.FailureWindowStart.Time) > failureWindow {
		next.FailureWindowStart = &metav1.Time{Time: now}
		next.FailedAttempts = 1
	} else {
		next.FailedAttempts++
	}
	if int(next.FailedAttempts) >= threshold {
		next.Locks++
		next.LockedUntil = &metav1.Time{Time: now.Add(LockDuration(next.Locks))}
		next.FailedAttempts = 0
		next.FailureWindowStart = nil
	}
	return next
}

func RecordSuccess(status v1alpha1.UserStatus, now time.Time) v1alpha1.UserStatus {
	next := status
	next.LastLogin = &metav1.Time{Time: now}
	next.FailedAttempts = 0
	next.FailureWindowStart = nil
	next.LockedUntil = nil
	next.Locks = 0
	return next
}
