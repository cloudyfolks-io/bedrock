package release

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const ImageDir = "release"

func FromImage(ctx context.Context, ref, arch, dest string) error {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return err
	}
	img, err := remote.Image(parsed, remote.WithContext(ctx), remote.WithTransport(downloadTransport), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: arch}))
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	reader := mutate.Extract(img)
	defer reader.Close()
	return extractPrefix(tar.NewReader(reader), ImageDir+"/", dest)
}

func extractPrefix(tr *tar.Reader, prefix, dest string) error {
	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		cleaned := strings.TrimPrefix(filepath.ToSlash(header.Name), "./")
		if !strings.HasPrefix(cleaned, prefix) || header.Typeflag != tar.TypeReg {
			continue
		}
		rel := strings.TrimPrefix(cleaned, prefix)
		if rel == "" || hasUnsafeComponent(rel) {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, tr); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fmt.Errorf("image has no %s directory", prefix)
	}
	return nil
}

func hasUnsafeComponent(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == ".." {
			return true
		}
	}
	return false
}
