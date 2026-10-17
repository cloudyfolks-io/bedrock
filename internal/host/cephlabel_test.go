package host

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProbeCephLabel(t *testing.T) {
	cases := map[string]struct {
		device    Device
		responses map[string]string
		want      string
		wantCalls int
	}{
		"bluestore": {
			Device{Path: "/dev/disk/by-id/wwn-0x1", Block: true, Signature: "ceph_bluestore"},
			map[string]string{"head -c 4096 /dev/disk/by-id/wwn-0x1": string(sampleLabel(t))},
			sampleFSID, 1,
		},
		"lvm": {
			Device{Path: "/dev/disk/by-id/wwn-0x1", Block: true, Signature: "LVM2_member"},
			map[string]string{pvsKey: lvmPVs, lvsKey: lvsJSON("ceph.cluster_fsid=" + sampleFSID)},
			sampleFSID, 2,
		},
		"filesystem": {Device{Path: "/dev/disk/by-id/wwn-0x1", Block: true, Signature: "ext4"}, nil, "", 0},
		"blank":      {Device{Path: "/dev/disk/by-id/wwn-0x1", Block: true}, nil, "", 0},
		"not block":  {Device{Path: "/dev/disk/by-id/wwn-0x1"}, nil, "", 0},
	}
	for name, tc := range cases {
		e := &FakeExec{Responses: tc.responses}
		before := tc.device
		got, err := ProbeCephLabel(context.Background(), e, tc.device)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.CephFSID != tc.want || got.Path != before.Path || got.Signature != before.Signature || got.Block != before.Block {
			t.Fatalf("%s: device %+v", name, got)
		}
		if tc.device != before {
			t.Fatalf("%s: input changed", name)
		}
		if len(e.Calls) != tc.wantCalls {
			t.Fatalf("%s: calls %v", name, e.Calls)
		}
	}
}

func TestProbeCephLabelFailureNamesTheDevice(t *testing.T) {
	e := &FakeExec{Errors: map[string]error{"head -c 4096 /dev/disk/by-id/wwn-0x1": errors.New("permission denied")}}
	_, err := ProbeCephLabel(context.Background(), e, Device{Path: "/dev/disk/by-id/wwn-0x1", Block: true, Signature: "ceph_bluestore"})
	if err == nil || !strings.Contains(err.Error(), "read Ceph label") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err %v", err)
	}
}

func TestProbeCephLabelRefusesAnUnreadableLabel(t *testing.T) {
	label := sampleLabel(t)
	label[0x100] ^= 0xff
	e := &FakeExec{Responses: map[string]string{"head -c 4096 /dev/disk/by-id/wwn-0x1": string(label)}}
	_, err := ProbeCephLabel(context.Background(), e, Device{Path: "/dev/disk/by-id/wwn-0x1", Block: true, Signature: "ceph_bluestore"})
	if err == nil || strings.Contains(err.Error(), "AQBW") {
		t.Fatalf("err %v", err)
	}
}
