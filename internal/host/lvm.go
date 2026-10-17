package host

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const lvmFSIDTag = "ceph.cluster_fsid="

type pvReport struct {
	Report []struct {
		PV []struct {
			VGName string `json:"vg_name"`
		} `json:"pv"`
	} `json:"report"`
}

type lvReport struct {
	Report []struct {
		LV []struct {
			Tags string `json:"lv_tags"`
		} `json:"lv"`
	} `json:"report"`
}

func LVMCephFSID(ctx context.Context, e Exec, path string) (string, error) {
	group, err := volumeGroupOf(ctx, e, path)
	if err != nil || group == "" {
		return "", err
	}
	out, err := e.Run(ctx, "lvs", "--readonly", "--reportformat", "json", "-o", "lv_tags", group)
	if err != nil {
		return "", err
	}
	var report lvReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		return "", fmt.Errorf("parse lvs output: %w", err)
	}
	var fsids []string
	for _, section := range report.Report {
		for _, volume := range section.LV {
			fsids = appendClusterFSID(fsids, volume.Tags)
		}
	}
	switch len(fsids) {
	case 0:
		return "", nil
	case 1:
		return fsids[0], nil
	default:
		return "", fmt.Errorf("volume group %s holds volumes of %d Ceph clusters", group, len(fsids))
	}
}

func volumeGroupOf(ctx context.Context, e Exec, path string) (string, error) {
	out, err := e.Run(ctx, "pvs", "--readonly", "--reportformat", "json", "-o", "vg_name", path)
	if err != nil {
		return "", err
	}
	var report pvReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		return "", fmt.Errorf("parse pvs output: %w", err)
	}
	if len(report.Report) == 0 || len(report.Report[0].PV) == 0 {
		return "", nil
	}
	return report.Report[0].PV[0].VGName, nil
}

func appendClusterFSID(fsids []string, tags string) []string {
	for _, tag := range strings.Split(tags, ",") {
		fsid, ok := strings.CutPrefix(tag, lvmFSIDTag)
		if ok && fsid != "" && !slices.Contains(fsids, fsid) {
			return append(fsids, fsid)
		}
	}
	return fsids
}
