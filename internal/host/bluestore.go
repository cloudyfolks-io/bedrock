package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strconv"
	"strings"
)

const (
	bluestoreLabelBytes  = 4096
	bluestoreMagic       = "bluestore block device\n"
	bluestoreUUIDLine    = 37
	bluestoreStructHead  = 6
	bluestoreFixedFields = 16 + 8 + 8
	bluestoreMetaSince   = 2
	bluestoreFSIDKey     = "ceph_fsid"
)

var (
	castagnoli       = crc32.MakeTable(crc32.Castagnoli)
	errLabelAbsent   = errors.New("no BlueStore label")
	errLabelCut      = errors.New("BlueStore label is truncated")
	errLabelChecksum = errors.New("BlueStore label checksum mismatch")
)

func BlueStoreFSID(ctx context.Context, e Exec, path string) (string, error) {
	out, err := e.Run(ctx, "head", "-c", strconv.Itoa(bluestoreLabelBytes), path)
	if err != nil {
		return "", err
	}
	return ParseBlueStoreFSID([]byte(out))
}

func ParseBlueStoreFSID(label []byte) (string, error) {
	head := len(bluestoreMagic) + bluestoreUUIDLine
	if !bytes.HasPrefix(label, []byte(bluestoreMagic)) {
		return "", errLabelAbsent
	}
	if len(label) < head+bluestoreStructHead {
		return "", errLabelCut
	}
	version := label[head]
	bodyEnd := head + bluestoreStructHead + int(binary.LittleEndian.Uint32(label[head+2:]))
	if bodyEnd < head+bluestoreStructHead || len(label) < bodyEnd+4 {
		return "", errLabelCut
	}
	if ^crc32.Checksum(label[:bodyEnd], castagnoli) != binary.LittleEndian.Uint32(label[bodyEnd:]) {
		return "", errLabelChecksum
	}
	if version < bluestoreMetaSince {
		return "", nil
	}
	return metaValue(label[head+bluestoreStructHead:bodyEnd], bluestoreFSIDKey)
}

func metaValue(body []byte, wanted string) (string, error) {
	_, rest, err := takeBytes(body, bluestoreFixedFields)
	if err != nil {
		return "", err
	}
	if _, rest, err = takeString(rest); err != nil {
		return "", err
	}
	count, rest, err := takeUint32(rest)
	if err != nil {
		return "", err
	}
	for range count {
		var key, value string
		if key, rest, err = takeString(rest); err != nil {
			return "", err
		}
		if value, rest, err = takeString(rest); err != nil {
			return "", err
		}
		if key == wanted {
			return strings.TrimSpace(value), nil
		}
	}
	return "", nil
}

func takeBytes(b []byte, n uint32) ([]byte, []byte, error) {
	if uint64(n) > uint64(len(b)) {
		return nil, nil, errLabelCut
	}
	return b[:n], b[n:], nil
}

func takeUint32(b []byte) (uint32, []byte, error) {
	taken, rest, err := takeBytes(b, 4)
	if err != nil {
		return 0, nil, err
	}
	return binary.LittleEndian.Uint32(taken), rest, nil
}

func takeString(b []byte) (string, []byte, error) {
	n, rest, err := takeUint32(b)
	if err != nil {
		return "", nil, err
	}
	taken, rest, err := takeBytes(rest, n)
	return string(taken), rest, err
}
