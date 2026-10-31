package operator

import (
	"context"
	"fmt"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const ovnCentralContainer = "ovn-central"

var raftServer = regexp.MustCompile(`\(\w+ at `)

var raftDatabases = []struct{ control, database string }{
	{"/var/run/ovn/ovnnb_db.ctl", "OVN_Northbound"},
	{"/var/run/ovn/ovnsb_db.ctl", "OVN_Southbound"},
}

func (r *ClusterReconciler) waitRaftMembers(ctx context.Context, want int) error {
	if r.Exec == nil {
		return nil
	}
	waitCtx, cancel := withTimeout(ctx, r.GroupTimeout)
	defer cancel()
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		err := r.raftMembersAre(waitCtx, want)
		if err == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("ovn-central raft cluster does not have %d members: %w", want, err)
		case <-ticker.C:
		}
	}
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func (r *ClusterReconciler) raftMembersAre(ctx context.Context, want int) error {
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods, client.InNamespace(fabricNamespace), client.MatchingLabels{"app": ovnCentralDeployment}); err != nil {
		return err
	}
	ready := readyPods(pods.Items)
	if len(ready) != want {
		return fmt.Errorf("%d of %d pods ready", len(ready), want)
	}
	for _, pod := range ready {
		for _, db := range raftDatabases {
			out, err := r.Exec(ctx, fabricNamespace, pod.Name, ovnCentralContainer, []string{"ovn-appctl", "-t", db.control, "cluster/status", db.database})
			if err != nil {
				return err
			}
			if members := len(raftServer.FindAll(out, -1)); members != want {
				return fmt.Errorf("pod %s lists %d %s members", pod.Name, members, db.database)
			}
		}
	}
	return nil
}

func readyPods(pods []corev1.Pod) []corev1.Pod {
	var ready []corev1.Pod
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = append(ready, pod)
			}
		}
	}
	return ready
}
