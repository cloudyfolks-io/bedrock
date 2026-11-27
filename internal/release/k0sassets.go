package release

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func K0sAirgapURL(baseURL, version, arch string) string {
	return fmt.Sprintf("%s/%s/k0s-airgap-bundle-%s-linux-%s.tar", baseURL, version, version, arch)
}

func K0sSumsURL(baseURL, version string) string {
	return fmt.Sprintf("%s/%s/sha256sums.txt", baseURL, version)
}

func ParseSums(text string) map[string]string {
	sums := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = "sha256:" + fields[0]
	}
	return sums
}

func K0sAirgapBundle(ctx context.Context, baseURL, version, arch, cacheDir string) (string, error) {
	sums, err := fetchSums(ctx, K0sSumsURL(baseURL, version))
	if err != nil {
		return "", err
	}
	file := fmt.Sprintf("k0s-airgap-bundle-%s-linux-%s.tar", version, arch)
	want, ok := sums[file]
	if !ok {
		return "", fmt.Errorf("k0s %s: no checksum for %s", version, file)
	}
	path := filepath.Join(cacheDir, "k0s", version, file)
	if err := ensureDownloaded(ctx, K0sAirgapURL(baseURL, version, arch), path); err != nil {
		return "", err
	}
	got, err := FileSHA256(path)
	if err != nil {
		return "", err
	}
	if got != want {
		os.Remove(path)
		return "", fmt.Errorf("k0s %s: checksum mismatch for %s", version, file)
	}
	return path, nil
}

func fetchSums(ctx context.Context, url string) (map[string]string, error) {
	var sums map[string]string
	err := fetch(ctx, downloadClient, downloadDelays, url, func(body io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(body, 1<<20))
		sums = ParseSums(string(data))
		return err
	})
	return sums, err
}
