package release

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const (
	VarMasterIPs = "BEDROCK_MASTER_IPS"
	VarVIP       = "BEDROCK_VIP"
)

func Vars(ctx context.Context, c client.Client, vip string) (map[string]string, error) {
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes, client.MatchingLabels{roles.FabricRoleLabel: "master"}); err != nil {
		return nil, err
	}
	var ips []string
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				ips = append(ips, addr.Address)
				break
			}
		}
	}
	sort.Strings(ips)
	vars := map[string]string{VarVIP: vip}
	if len(ips) > 0 {
		vars[VarMasterIPs] = strings.Join(ips, ",")
	}
	return vars, nil
}

func WaitVars(ctx context.Context, c client.Client, vip string, interval time.Duration) (map[string]string, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastErr := fmt.Errorf("no master node registered yet")
	for {
		vars, err := Vars(ctx, c, vip)
		if err != nil {
			lastErr = err
		} else if vars[VarMasterIPs] != "" {
			return vars, nil
		}
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-ticker.C:
		}
	}
}
