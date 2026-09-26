package release

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	annotationRefName   = "org.opencontainers.image.ref.name"
	annotationImageName = "io.containerd.image.name"
)

var pullBackoff = remote.Backoff{Duration: time.Second, Factor: 3, Jitter: 0.1, Steps: 5}

func PullLayout(ctx context.Context, ref, dest, cacheDir, arch string) (string, error) {
	return pullLayout(ctx, blobCache{dir: cacheDir, next: remote.DefaultTransport, idle: blobIdleTimeout, pause: blobPauseBase}, ref, dest, v1.Platform{OS: "linux", Architecture: arch})
}

func pullLayout(ctx context.Context, transport http.RoundTripper, ref, dest string, platform v1.Platform) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	desc, err := remote.Get(parsed, remote.WithContext(ctx), remote.WithTransport(transport), remote.WithRetryBackoff(pullBackoff))
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", ref, err)
	}
	dir, err := os.MkdirTemp(filepath.Dir(dest), ".layout-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path, err := layout.Write(dir, empty.Index)
	if err != nil {
		return "", fmt.Errorf("write layout %s: %w", ref, err)
	}
	if err := appendNamed(path, desc, ref, platform); err != nil {
		return "", fmt.Errorf("write layout %s: %w", ref, err)
	}
	if err := tarDirectory(dir, dest); err != nil {
		os.Remove(dest)
		return "", err
	}
	return desc.Digest.String(), nil
}

func appendNamed(path layout.Path, desc *remote.Descriptor, ref string, platform v1.Platform) error {
	names, err := ContainerdNames(ref)
	if err != nil {
		return err
	}
	top, err := writePlatform(path, desc, platform)
	if err != nil {
		return err
	}
	for _, named := range names {
		if err := path.AppendDescriptor(withRefName(top, named)); err != nil {
			return err
		}
	}
	return nil
}

func withRefName(desc v1.Descriptor, ref string) v1.Descriptor {
	annotations := map[string]string{}
	maps.Copy(annotations, desc.Annotations)
	maps.Copy(annotations, map[string]string{annotationRefName: ref, annotationImageName: ref})
	desc.Annotations = annotations
	return desc
}

func ContainerdNames(ref string) ([]string, error) {
	base, digest, pinned := strings.Cut(ref, "@")
	if !pinned {
		named, err := containerdName(ref)
		return []string{named}, err
	}
	byDigest, err := containerdName(repositoryOf(base) + "@" + digest)
	if err != nil {
		return nil, err
	}
	if !hasTag(base) {
		return []string{byDigest}, nil
	}
	byTag, err := containerdName(base)
	if err != nil {
		return nil, err
	}
	return []string{byTag, byDigest}, nil
}

func hasTag(ref string) bool {
	return strings.LastIndex(ref, ":") > strings.LastIndex(ref, "/")
}

func repositoryOf(ref string) string {
	if !hasTag(ref) {
		return ref
	}
	return ref[:strings.LastIndex(ref, ":")]
}

func containerdName(ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	registry := parsed.Context().RegistryStr()
	if registry == name.DefaultRegistry {
		registry = "docker.io"
	}
	repository := registry + "/" + parsed.Context().RepositoryStr()
	if digest, ok := parsed.(name.Digest); ok {
		return repository + "@" + digest.DigestStr(), nil
	}
	return repository + ":" + parsed.Identifier(), nil
}

func tarDirectory(dir, dest string) error {
	part := dest + ".part"
	file, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(file)
	writeErr := writeTarEntries(tw, dir)
	closeErr := firstError(writeErr, tw.Close(), file.Close())
	if closeErr != nil {
		os.Remove(part)
		return closeErr
	}
	return os.Rename(part, dest)
}

func writeTarEntries(tw *tar.Writer, dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if entry.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}
