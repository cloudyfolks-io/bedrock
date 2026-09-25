package depot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const (
	Port = 9480
	Dir  = "var/lib/bedrock/depot"
)

func BaseURL(ip string) string {
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(Port))
}

func BundleURL(base, version, arch string) string {
	return base + "/" + version + "/" + arch
}

func BundleDir(root, version, arch string) string {
	return filepath.Join(root, Dir, version, arch)
}

func Handler(root string) http.Handler {
	files := http.Dir(filepath.Join(root, Dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		file, err := files.Open(path.Clean("/" + r.URL.Path))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	})
}

func Serve(ctx context.Context, ip, root string) error {
	return serve(ctx, net.JoinHostPort(ip, strconv.Itoa(Port)), root)
}

func serve(ctx context.Context, addr, root string) error {
	server := &http.Server{Addr: addr, Handler: Handler(root), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case err := <-errs:
		return err
	}
}

func Scan(root string) ([]v1alpha1.DepotBundle, error) {
	manifests, err := filepath.Glob(filepath.Join(root, Dir, "*", "*", release.BundleFileName))
	if err != nil {
		return nil, err
	}
	bundles := make([]v1alpha1.DepotBundle, 0, len(manifests))
	for _, manifest := range manifests {
		dir := filepath.Dir(manifest)
		size, err := treeSize(dir)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, v1alpha1.DepotBundle{Version: filepath.Base(filepath.Dir(dir)), Arch: filepath.Base(dir), Bytes: size})
	}
	sort.Slice(bundles, func(i, j int) bool {
		if bundles[i].Version != bundles[j].Version {
			return bundles[i].Version < bundles[j].Version
		}
		return bundles[i].Arch < bundles[j].Arch
	})
	return bundles, nil
}

func treeSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func Download(ctx context.Context, c *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	if err := writePart(part, resp.Body); err != nil {
		os.Remove(part)
		return fmt.Errorf("download %s: %w", url, err)
	}
	return os.Rename(part, dest)
}

func writePart(part string, body io.Reader) error {
	file, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func Verify(file, checksum string) error {
	if checksum == "" {
		return fmt.Errorf("%s: no checksum to verify against", file)
	}
	got, err := release.FileSHA256(file)
	if err != nil {
		return err
	}
	if got != checksum {
		return errors.Join(fmt.Errorf("%s: checksum %s, want %s", file, got, checksum), os.Remove(file))
	}
	return nil
}
