package release

import (
	"context"
	"fmt"
	"maps"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	VarMasterIPs = "BEDROCK_MASTER_IPS"
	VarVIP       = "BEDROCK_VIP"
)

func Vars(ctx context.Context, c client.Reader, vip string) (map[string]string, error) {
	masters, err := MasterNodes(ctx, c)
	if err != nil {
		return nil, err
	}
	vars := map[string]string{VarVIP: vip}
	if len(masters) > 0 {
		vars = WithMasters(vars, masters)
	}
	values, err := settingValues(ctx, c)
	if err != nil {
		return nil, err
	}
	dns, err := dnsVars(ctx, c)
	if err != nil {
		return nil, err
	}
	maps.Copy(vars, SettingVars(values, vip))
	maps.Copy(vars, dns)
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
