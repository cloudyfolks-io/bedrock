package host

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type AdoptMonitor struct {
	ID      string
	Address string
}

type AdoptFacts struct {
	MonStores        map[string]bool
	MonAddresses     map[string]bool
	ConfigFSID       string
	ListeningPorts   []int
	K0sRunning       bool
	BedrockInstalled bool
}

func GatherAdopt(ctx context.Context, e Exec, root string, monitors []AdoptMonitor) (AdoptFacts, error) {
	rook := filepath.Join(root, "var", "lib", "rook")
	stores := make(map[string]bool, len(monitors))
	held := make(map[string]bool, len(monitors))
	for _, monitor := range monitors {
		stores[monitor.ID] = exists(filepath.Join(rook, "mon-"+monitor.ID, "data", "store.db"))
		onHost, err := HasAddress(ctx, e, monitor.Address)
		if err != nil {
			return AdoptFacts{}, err
		}
		held[monitor.ID] = onHost
	}
	fsid, err := configFSID(filepath.Join(rook, "rook-ceph", "rook-ceph.config"))
	if err != nil {
		return AdoptFacts{}, err
	}
	out, err := e.Run(ctx, "ss", "-H", "-ltn")
	if err != nil {
		return AdoptFacts{}, err
	}
	return AdoptFacts{MonStores: stores, MonAddresses: held, ConfigFSID: fsid, ListeningPorts: listeningPorts(out), BedrockInstalled: exists(agentUnitPath(root))}, nil
}

func configFSID(path string) (string, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok && strings.TrimSpace(key) == "fsid" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", scanner.Err()
}

func listeningPorts(out string) []int {
	var ports []int
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[3]
		port, err := strconv.Atoi(local[strings.LastIndex(local, ":")+1:])
		if err == nil {
			ports = append(ports, port)
		}
	}
	return ports
}
