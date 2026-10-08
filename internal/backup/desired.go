package backup

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/schedule"
)

var (
	ErrTargetNotFound      = errors.New("target not found")
	ErrKeepInvalid         = errors.New("keep out of range")
	ErrScheduleTooFrequent = errors.New("schedule fires more than once per hour")
	ErrScheduleInvalid     = errors.New("schedule is not a 5-field cron expression")
)

func KeepOf(vmb v1alpha1.VirtualMachineBackup) int {
	if vmb.Spec.Keep == 0 {
		return DefaultKeep
	}
	return int(vmb.Spec.Keep)
}

func checkCommon(vmb v1alpha1.VirtualMachineBackup) error {
	if vmb.Spec.Target != "" && vmb.Spec.Target != LocationName {
		return fmt.Errorf("%w: %q", ErrTargetNotFound, vmb.Spec.Target)
	}
	if vmb.Spec.Keep < 0 || vmb.Spec.Keep > MaxKeep {
		return fmt.Errorf("%w: %d, allowed 1 to %d", ErrKeepInvalid, vmb.Spec.Keep, MaxKeep)
	}
	return nil
}

func checkSchedule(expr string, now time.Time) error {
	if err := schedule.Validate(expr); err != nil {
		return fmt.Errorf("%w: %v", ErrScheduleInvalid, err)
	}
	interval, err := schedule.MinInterval(expr, now)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrScheduleInvalid, err)
	}
	if interval < MinScheduleInterval {
		return fmt.Errorf("%w: %q fires every %s", ErrScheduleTooFrequent, expr, interval)
	}
	return nil
}

func ScheduleFor(vmb v1alpha1.VirtualMachineBackup, scope Scope, now time.Time) (*unstructured.Unstructured, error) {
	if err := checkCommon(vmb); err != nil {
		return nil, err
	}
	if err := checkSchedule(vmb.Spec.Schedule, now); err != nil {
		return nil, err
	}
	return Schedule(scope, vmb.Name, vmb.Spec.VMRef, vmb.Spec.Schedule, ScheduleTTL), nil
}

func OneShotFor(vmb v1alpha1.VirtualMachineBackup, scope Scope) (*unstructured.Unstructured, error) {
	if err := checkCommon(vmb); err != nil {
		return nil, err
	}
	return Backup(scope, vmb.Name, vmb.Spec.VMRef, OneShotTTL), nil
}
