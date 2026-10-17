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

type AdoptFacts struct {
	MonStores      map[string]bool
	ConfigFSID     string
	ListeningPorts []int
}

func GatherAdopt(ctx context.Context, e Exec, root string, monitorIDs []string) (AdoptFacts, error) {
	rook := filepath.Join(root, "var", "lib", "rook")
	stores := make(map[string]bool, len(monitorIDs))
	for _, id := range monitorIDs {
		stores[id] = exists(filepath.Join(rook, "mon-"+id, "data", "store.db"))
	}
	fsid, err := configFSID(filepath.Join(rook, "rook-ceph", "rook-ceph.config"))
	if err != nil {
		return AdoptFacts{}, err
	}
	out, err := e.Run(ctx, "ss", "-H", "-ltn")
	if err != nil {
		return AdoptFacts{}, err
	}
	return AdoptFacts{MonStores: stores, ConfigFSID: fsid, ListeningPorts: listeningPorts(out)}, nil
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
