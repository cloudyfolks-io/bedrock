package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

const (
	SignatureBlueStore = "ceph_bluestore"
	SignatureLVM       = "LVM2_member"
)

type Device struct {
	Path      string
	Block     bool
	Signature string
	CephFSID  string
}

func ProbeDevice(ctx context.Context, e Exec, statFn func(string) (fs.FileInfo, error), path string) (Device, error) {
	info, err := statFn(path)
	if err != nil {
		return Device{}, err
	}
	mode := info.Mode()
	block := mode&fs.ModeDevice != 0 && mode&fs.ModeCharDevice == 0
	if !block {
		return Device{Path: path}, nil
	}
	out, err := e.Run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", path)
	signature := strings.TrimSpace(out)
	var exit *ExitError
	if err != nil && (!errors.As(err, &exit) || signature != "") {
		return Device{}, err
	}
	return Device{Path: path, Block: true, Signature: signature}, nil
}

func ProbeCephLabel(ctx context.Context, e Exec, device Device) (Device, error) {
	fsid, err := cephFSID(ctx, e, device)
	if err != nil {
		return Device{}, fmt.Errorf("read Ceph label: %w", err)
	}
	device.CephFSID = fsid
	return device, nil
}

func cephFSID(ctx context.Context, e Exec, device Device) (string, error) {
	switch device.Signature {
	case SignatureBlueStore:
		return BlueStoreFSID(ctx, e, device.Path)
	case SignatureLVM:
		return LVMCephFSID(ctx, e, device.Path)
	default:
		return "", nil
	}
}
