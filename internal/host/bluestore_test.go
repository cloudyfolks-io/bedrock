package host

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"strings"
	"testing"
)

const sampleFSID = "7e1b4a3c-5d2f-4c8e-9a61-0b3f2d8c4e57"

func sampleLabel(t *testing.T) []byte {
	t.Helper()
	label, err := os.ReadFile("testdata/bluestore-label.bin")
	if err != nil {
		t.Fatal(err)
	}
	return label
}

func appendString(out []byte, s string) []byte {
	out = binary.LittleEndian.AppendUint32(out, uint32(len(s)))
	return append(out, s...)
}

func buildLabel(version byte, meta [][2]string) []byte {
	label := []byte("bluestore block device\n3f9c2a7e-84b1-4d65-a0c3-9e5b17d2f6a8\n")
	body := make([]byte, 32)
	body = appendString(body, "main")
	body = binary.LittleEndian.AppendUint32(body, uint32(len(meta)))
	for _, pair := range meta {
		body = appendString(appendString(body, pair[0]), pair[1])
	}
	label = append(label, version, 1)
	label = binary.LittleEndian.AppendUint32(label, uint32(len(body)))
	label = append(label, body...)
	label = binary.LittleEndian.AppendUint32(label, ^crc32.Checksum(label, crc32.MakeTable(crc32.Castagnoli)))
	return append(label, make([]byte, 4096-len(label))...)
}

func TestParseBlueStoreFSIDRealLabel(t *testing.T) {
	fsid, err := ParseBlueStoreFSID(sampleLabel(t))
	if err != nil {
		t.Fatal(err)
	}
	if fsid != sampleFSID {
		t.Fatalf("fsid %q, want %q", fsid, sampleFSID)
	}
}

func TestParseBlueStoreFSIDBuiltLabel(t *testing.T) {
	label := buildLabel(2, [][2]string{{"osd_key", "secret\n"}, {"ceph_fsid", sampleFSID}, {"whoami", "3"}})
	fsid, err := ParseBlueStoreFSID(label)
	if err != nil || fsid != sampleFSID {
		t.Fatalf("fsid %q err %v", fsid, err)
	}
}

func TestParseBlueStoreFSIDWithoutFSIDKey(t *testing.T) {
	for name, label := range map[string][]byte{
		"no key":      buildLabel(2, [][2]string{{"whoami", "3"}}),
		"old version": buildLabel(1, nil),
	} {
		fsid, err := ParseBlueStoreFSID(label)
		if err != nil || fsid != "" {
			t.Fatalf("%s: fsid %q err %v", name, fsid, err)
		}
	}
}

func TestParseBlueStoreFSIDRefusesBadLabels(t *testing.T) {
	real := sampleLabel(t)
	flipped := append([]byte(nil), real...)
	flipped[0x100] ^= 0xff
	huge := buildLabel(2, nil)
	binary.LittleEndian.PutUint32(huge[62:], 1<<30)
	cases := map[string][]byte{
		"empty":         nil,
		"no magic":      make([]byte, 4096),
		"short header":  real[:40],
		"cut in body":   real[:200],
		"cut at crc":    real[:60+6+0x271],
		"cut in crc":    real[:60+6+0x271+2],
		"flipped byte":  flipped,
		"huge length":   huge,
		"magic only":    []byte("bluestore block device\n"),
		"wrong magic":   append([]byte("bluestore block devicf\n"), real[23:]...),
		"zeroed length": append(append([]byte(nil), real[:62]...), make([]byte, 4034)...),
	}
	for name, label := range cases {
		fsid, err := ParseBlueStoreFSID(label)
		if err == nil || fsid != "" {
			t.Fatalf("%s: fsid %q err %v", name, fsid, err)
		}
		if strings.Contains(err.Error(), "AQBW") {
			t.Fatalf("%s: error carries label content: %v", name, err)
		}
	}
}

func TestBlueStoreFSIDReadsFirstFourKiB(t *testing.T) {
	e := &FakeExec{Responses: map[string]string{"head -c 4096 /dev/disk/by-id/wwn-0x1": string(sampleLabel(t))}}
	fsid, err := BlueStoreFSID(context.Background(), e, "/dev/disk/by-id/wwn-0x1")
	if err != nil || fsid != sampleFSID {
		t.Fatalf("fsid %q err %v", fsid, err)
	}
}

func TestBlueStoreFSIDReadFailure(t *testing.T) {
	e := &FakeExec{}
	if _, err := BlueStoreFSID(context.Background(), e, "/dev/sdb"); err == nil {
		t.Fatal("read failure must be an error")
	}
}
