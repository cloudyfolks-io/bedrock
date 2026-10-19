package operator

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

const stalledWaitingForEtcd = "WaitingForEtcd"

type rotationInput struct {
	Action    string
	KMS       bool
	Upgrading bool
	Status    v1alpha1.EncryptionRotationStatus
	Secret    []byte
	Hosts     []v1alpha1.Host
	Nodes     []corev1.Node
	Now       time.Time
	Random    io.Reader
}

type rotationDecision struct {
	Status      v1alpha1.EncryptionRotationStatus
	Secret      []byte
	Grant       string
	GrantHash   string
	Revoke      []string
	Rewrite     bool
	ClearAction bool
	Stalled     string
}

func nextRotation(in rotationInput) rotationDecision {
	switch in.Action {
	case v1alpha1.EncryptionActionRotate:
		return startRotation(in)
	case v1alpha1.EncryptionActionRevert:
		return revertRotation(in)
	}
	if in.Status.Phase == "" {
		return rotationDecision{Status: in.Status}
	}
	return advanceRotation(in)
}

func refuseAction(in rotationInput, message string) rotationDecision {
	status := in.Status
	status.Message = message
	return rotationDecision{Status: status, ClearAction: true}
}

func startRotation(in rotationInput) rotationDecision {
	switch {
	case in.Status.Phase != "":
		return refuseAction(in, "rotate ignored: a rotation is in progress")
	case in.KMS:
		return refuseAction(in, "rotate refused: the cluster uses a KMS, rotate the key in the KMS service")
	case in.Upgrading:
		return refuseAction(in, "rotate refused: an upgrade is in progress")
	case in.Secret == nil:
		return refuseAction(in, "rotate refused: the cluster is not encrypted, the encryption Secret does not exist")
	}
	names, err := encryption.Keys(in.Secret)
	if err != nil {
		return refuseAction(in, "rotate refused: "+err.Error())
	}
	if len(names) != 1 {
		return refuseAction(in, fmt.Sprintf("rotate refused: the encryption config has %d keys, so a rotation is unfinished", len(names)))
	}
	next, err := encryption.NextKeyName(in.Secret)
	if err != nil {
		return refuseAction(in, "rotate refused: "+err.Error())
	}
	status := v1alpha1.EncryptionRotationStatus{Phase: v1alpha1.RotationAddingKey, OldKey: names[0], NewKey: next, LastRotatedAt: in.Status.LastRotatedAt}
	content, err := encryption.AddKey(in.Secret, next, in.Random)
	if err != nil {
		return refuseAction(in, "rotate refused: "+err.Error())
	}
	return rotationDecision{Status: status, Secret: content, ClearAction: true}
}

func revertRotation(in rotationInput) rotationDecision {
	switch {
	case in.Status.Phase == "":
		return refuseAction(in, "revert ignored: no rotation is in progress")
	case in.Status.Reverting:
		return refuseAction(in, "revert ignored: the rotation is already reverting")
	case in.Status.Phase == v1alpha1.RotationRemovingKey:
		return refuseAction(in, "revert refused: the old key is already removed on some hosts")
	case in.Secret == nil:
		return refuseAction(in, "revert refused: the encryption Secret is missing")
	}
	if busy := unresolvedHost(controlPlaneHosts(in.Hosts)); busy != "" {
		return stalled(in.Status, fmt.Sprintf("revert waits for host %s to finish applying the encryption config", busy), nil)
	}
	status := in.Status
	status.OldKey, status.NewKey = in.Status.NewKey, in.Status.OldKey
	status.Reverting = true
	status.Message = "reverting the rotation"
	status.Phase = v1alpha1.RotationPromotingKey
	if in.Status.Phase == v1alpha1.RotationAddingKey {
		status.Phase = v1alpha1.RotationRemovingKey
	}
	content, err := phaseContent(status, in.Secret, in.Random)
	if err != nil {
		return refuseAction(in, "revert refused: "+err.Error())
	}
	return rotationDecision{Status: status, Secret: changedContent(content, in.Secret), ClearAction: true}
}

func phaseContent(status v1alpha1.EncryptionRotationStatus, secret []byte, random io.Reader) ([]byte, error) {
	switch status.Phase {
	case v1alpha1.RotationAddingKey:
		return encryption.AddKey(secret, status.NewKey, random)
	case v1alpha1.RotationPromotingKey, v1alpha1.RotationRewritingSecrets:
		return encryption.Promote(secret, status.NewKey)
	}
	return encryption.RemoveKey(secret, status.OldKey)
}

func changedContent(wanted, current []byte) []byte {
	if bytes.Equal(wanted, current) {
		return nil
	}
	return wanted
}

func stalled(status v1alpha1.EncryptionRotationStatus, message string, revoke []string) rotationDecision {
	status.Message = message
	return rotationDecision{Status: status, Revoke: revoke}
}

func advanceRotation(in rotationInput) rotationDecision {
	switch {
	case in.Upgrading:
		return stalled(in.Status, "rotation paused: an upgrade is in progress", nil)
	case in.Secret == nil:
		return stalled(in.Status, "rotation stalled: the encryption Secret is missing", nil)
	}
	wanted, err := phaseContent(in.Status, in.Secret, in.Random)
	if err != nil {
		return stalled(in.Status, "rotation stalled: "+err.Error(), nil)
	}
	if changed := changedContent(wanted, in.Secret); changed != nil {
		status := in.Status
		status.Message = ""
		return rotationDecision{Status: status, Secret: changed}
	}
	return convergeHosts(in)
}

func controlPlaneHosts(hosts []v1alpha1.Host) []v1alpha1.Host {
	selected := slices.DeleteFunc(slices.Clone(hosts), func(host v1alpha1.Host) bool {
		return !v1alpha1.HostHasRole(host, v1alpha1.RoleControlPlane)
	})
	slices.SortFunc(selected, func(a, b v1alpha1.Host) int { return strings.Compare(a.Name, b.Name) })
	return selected
}

func hostConverged(host v1alpha1.Host, hash string) bool {
	status := host.Status.Encryption
	grant, granted := hostGrant(host)
	return status != nil && status.Hash == hash && !status.RestartPending && (!granted || grant == hash)
}

func hostReports(host v1alpha1.Host, hash string) bool {
	status := host.Status.Encryption
	return status != nil && status.Hash == hash && !status.RestartPending
}

func grantResolved(host v1alpha1.Host) bool {
	grant, granted := hostGrant(host)
	status := host.Status.Encryption
	return !granted || (status != nil && (status.Hash == grant || status.FailedHash == grant || status.HandledGrant == grant))
}

func unresolvedHost(hosts []v1alpha1.Host) string {
	for _, host := range hosts {
		if !grantResolved(host) {
			return host.Name
		}
	}
	return ""
}

func hostGrant(host v1alpha1.Host) (string, bool) {
	value, ok := host.Annotations[v1alpha1.AnnotationEncryptionApply]
	return value, ok
}

func staleGrants(hosts []v1alpha1.Host, hash string) []string {
	var revoke []string
	for _, host := range hosts {
		if value, ok := hostGrant(host); ok && value != hash && grantResolved(host) {
			revoke = append(revoke, host.Name)
		}
	}
	return revoke
}

func convergeHosts(in rotationInput) rotationDecision {
	hosts := controlPlaneHosts(in.Hosts)
	if len(hosts) == 0 {
		return stalled(in.Status, "rotation stalled: no control-plane host", nil)
	}
	hash := encryption.Hash(in.Secret)
	revoke := staleGrants(hosts, hash)
	pending := slices.DeleteFunc(slices.Clone(hosts), func(host v1alpha1.Host) bool { return hostConverged(host, hash) })
	if len(pending) == 0 {
		return finishPhase(in, hosts, revoke)
	}
	if only := slices.DeleteFunc(slices.Clone(pending), func(host v1alpha1.Host) bool { return slices.Contains(revoke, host.Name) && hostReports(host, hash) }); len(only) == 0 {
		return rotationDecision{Status: in.Status, Revoke: revoke}
	}
	return grantNext(in, hosts, pending, hash, revoke)
}

func grantNext(in rotationInput, all, pending []v1alpha1.Host, hash string, revoke []string) rotationDecision {
	for _, host := range pending {
		if host.Status.Encryption != nil && host.Status.Encryption.FailedHash == hash {
			failed := stalled(in.Status, fmt.Sprintf("rotation stalled: host %s failed to apply the encryption config", host.Name), revoke)
			failed.Stalled = "HostFailed"
			return failed
		}
	}
	if busy := unresolvedHost(all); busy != "" {
		return stalled(in.Status, fmt.Sprintf("waiting for host %s to finish applying the encryption config", busy), revoke)
	}
	for _, host := range pending {
		if value, ok := hostGrant(host); ok && value == hash {
			return stalled(in.Status, fmt.Sprintf("waiting for host %s to apply the encryption config", host.Name), revoke)
		}
	}
	if problem := etcdGate(all, in.Nodes); problem != "" {
		waiting := stalled(in.Status, "waiting for "+problem, revoke)
		waiting.Stalled = stalledWaitingForEtcd
		return waiting
	}
	next := pending[0].Name
	status := in.Status
	status.Message = fmt.Sprintf("applying the encryption config on host %s", next)
	return rotationDecision{Status: status, Grant: next, GrantHash: hash, Revoke: slices.DeleteFunc(revoke, func(name string) bool { return name == next })}
}

func finishPhase(in rotationInput, hosts []v1alpha1.Host, revoke []string) rotationDecision {
	status := in.Status
	status.Message = ""
	switch status.Phase {
	case v1alpha1.RotationAddingKey:
		status.Phase = v1alpha1.RotationPromotingKey
	case v1alpha1.RotationPromotingKey:
		status.Phase = v1alpha1.RotationRewritingSecrets
	case v1alpha1.RotationRewritingSecrets:
		return rotationDecision{Status: status, Revoke: revoke, Rewrite: true}
	case v1alpha1.RotationRemovingKey:
		return rotationDecision{Status: endedRotation(in), Revoke: grantedHosts(hosts)}
	}
	return rotationDecision{Status: status, Revoke: revoke}
}

func endedRotation(in rotationInput) v1alpha1.EncryptionRotationStatus {
	finished := metav1.NewTime(in.Now)
	ended := v1alpha1.EncryptionRotationStatus{LastRotatedAt: &finished, RewrittenSecrets: in.Status.RewrittenSecrets}
	if in.Status.Reverting {
		ended.Message = "the rotation was reverted"
	}
	return ended
}

func grantedHosts(hosts []v1alpha1.Host) []string {
	var granted []string
	for _, host := range hosts {
		if _, ok := hostGrant(host); ok {
			granted = append(granted, host.Name)
		}
	}
	return granted
}

func finishRewrite(status v1alpha1.EncryptionRotationStatus, rewritten int64) v1alpha1.EncryptionRotationStatus {
	status.Phase = v1alpha1.RotationRemovingKey
	status.RewrittenSecrets += rewritten
	status.Message = ""
	return status
}
