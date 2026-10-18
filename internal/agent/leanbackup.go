package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

const (
	k0sArchivePattern = "k0s_backup_*.tar.gz"
	k0sImagesEntry    = "images"
)

type k0sBackupStep func(ctx context.Context, deps Deps, work string) error

func plainK0sBackup(ctx context.Context, deps Deps, work string) error {
	_, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "backup", "--save-path", work)
	return err
}

func leanK0sBackup(ctx context.Context, deps Deps, work string) error {
	if err := plainK0sBackup(ctx, deps, work); err != nil {
		return err
	}
	return dropImages(work)
}

func dropImages(work string) error {
	archives, err := filepath.Glob(filepath.Join(work, k0sArchivePattern))
	if err != nil {
		return err
	}
	if len(archives) == 0 {
		return errNoK0sBackup
	}
	for _, archive := range archives {
		if err := writeAtomic(archive, func(dst io.Writer) error { return copyWithoutImages(dst, archive) }); err != nil {
			return err
		}
	}
	return nil
}

func copyWithoutImages(dst io.Writer, archive string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	return filterImages(dst, file)
}

func filterImages(dst io.Writer, src io.Reader) error {
	unzipped, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	zipped, err := gzip.NewWriterLevel(dst, gzip.BestSpeed)
	if err != nil {
		return err
	}
	writer := tar.NewWriter(zipped)
	copyErr := copyEntries(writer, tar.NewReader(unzipped))
	if copyErr == nil {
		_, copyErr = io.Copy(io.Discard, unzipped)
	}
	return errors.Join(copyErr, writer.Close(), zipped.Close())
}

func copyEntries(dst *tar.Writer, src *tar.Reader) error {
	for {
		header, err := src.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if isImagesEntry(header.Name) {
			continue
		}
		if err := dst.WriteHeader(header); err != nil {
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			return err
		}
	}
}

func isImagesEntry(name string) bool {
	clean := path.Clean(name)
	return clean == k0sImagesEntry || strings.HasPrefix(clean, k0sImagesEntry+"/")
}
