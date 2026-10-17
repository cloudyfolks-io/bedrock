package host

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

const (
	lvmPVs = `  {
      "report": [
          {
              "pv": [
                  {"vg_name":"ceph-9c1d"}
              ]
          }
      ]
      ,
      "log": [
      ]
  }
`
	pvsKey = "pvs --readonly --reportformat json -o vg_name /dev/disk/by-id/wwn-0x1"
	lvsKey = "lvs --readonly --reportformat json -o lv_tags ceph-9c1d"
)

func lvsJSON(tags ...string) string {
	rows := make([]string, 0, len(tags))
	for _, tag := range tags {
		rows = append(rows, `{"lv_tags":"`+tag+`"}`)
	}
	return `{"report":[{"lv":[` + strings.Join(rows, ",") + `]}],"log":[]}`
}

func TestLVMCephFSID(t *testing.T) {
	cases := map[string]struct {
		pvs, lvs string
		want     string
		wantErr  bool
	}{
		"osd lv":               {lvmPVs, lvsJSON("ceph.cluster_fsid=" + sampleFSID + ",ceph.osd_id=0,ceph.type=block"), sampleFSID, false},
		"two lvs, one cluster": {lvmPVs, lvsJSON("ceph.cluster_fsid="+sampleFSID, "ceph.cluster_fsid="+sampleFSID+",ceph.type=db"), sampleFSID, false},
		"no ceph tags":         {lvmPVs, lvsJSON("", "other=1"), "", false},
		"no lvs":               {lvmPVs, lvsJSON(), "", false},
		"orphan pv":            {`{"report":[{"pv":[{"vg_name":""}]}]}`, "", "", false},
		"no pv rows":           {`{"report":[{"pv":[]}]}`, "", "", false},
		"two clusters":         {lvmPVs, lvsJSON("ceph.cluster_fsid="+sampleFSID, "ceph.cluster_fsid=11111111-2222-3333-4444-555555555555"), "", true},
		"bad pvs json":         {"not json", "", "", true},
		"bad lvs json":         {lvmPVs, "not json", "", true},
		"empty fsid tag":       {lvmPVs, lvsJSON("ceph.cluster_fsid="), "", false},
	}
	for name, tc := range cases {
		e := &FakeExec{Responses: map[string]string{pvsKey: tc.pvs, lvsKey: tc.lvs}}
		got, err := LVMCephFSID(context.Background(), e, "/dev/disk/by-id/wwn-0x1")
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Fatalf("%s: fsid %q err %v", name, got, err)
		}
	}
}

const lvm2Missing = "device /dev/disk/by-id/wwn-0x1: LVM2_member needs lvm2 on the host: install lvm2"

func TestLVMCephFSIDNamesLVM2WhenToolsAreMissing(t *testing.T) {
	notFound := fmt.Errorf(`pvs --readonly: %w: `, exec.ErrNotFound)
	cases := map[string]*FakeExec{
		"pvs missing": {Errors: map[string]error{pvsKey: notFound}},
		"lvs missing": {Responses: map[string]string{pvsKey: lvmPVs}, Errors: map[string]error{lvsKey: fmt.Errorf(`lvs --readonly: %w: `, exec.ErrNotFound)}},
	}
	for name, e := range cases {
		_, err := LVMCephFSID(context.Background(), e, "/dev/disk/by-id/wwn-0x1")
		if err == nil || err.Error() != lvm2Missing {
			t.Fatalf("%s: err %v", name, err)
		}
	}
}

func TestRealExecReportsMissingBinaryAsNotFound(t *testing.T) {
	_, err := RealExec{}.Run(context.Background(), "bedrock-no-such-binary")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestLVMCephFSIDKeepsOtherToolFailures(t *testing.T) {
	broken := errors.New("pvs: device not found")
	e := &FakeExec{Errors: map[string]error{pvsKey: broken}}
	if _, err := LVMCephFSID(context.Background(), e, "/dev/disk/by-id/wwn-0x1"); !errors.Is(err, broken) {
		t.Fatalf("err %v", err)
	}
	e = &FakeExec{Responses: map[string]string{pvsKey: lvmPVs}, Errors: map[string]error{lvsKey: &ExitError{Code: 5}}}
	var exit *ExitError
	if _, err := LVMCephFSID(context.Background(), e, "/dev/disk/by-id/wwn-0x1"); !errors.As(err, &exit) {
		t.Fatalf("err %v", err)
	}
}
