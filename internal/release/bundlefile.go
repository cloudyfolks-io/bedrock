package release

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/klauspost/compress/zstd"
	sigyaml "sigs.k8s.io/yaml"
)

const (
	BundleFileName    = "bundle.yaml"
	bundleReleaseDir  = "release"
	bundleK0sPath     = "k0s/k0s"
	bundleBedrockPath = "bedrock/bedrock"
	bundleImagesDir   = "images"
	bundleAirgapFile  = "k0s-airgap.tar"
)

var unsafeRefChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

type BundleImage struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
	File   string `json:"file"`
}

type BundleSpec struct {
	Version     string        `json:"version"`
	Arch        string        `json:"arch"`
	Image       string        `json:"image"`
	ImageDigest string        `json:"imageDigest"`
	K0sVersion  string        `json:"k0sVersion"`
	Images      []BundleImage `json:"images"`
}

type BundleInputs struct {
	ReleaseDir    string
	Arch          string
	K0sBinary     string
	BedrockBinary string
	K0sAirgap     string
	Pull          func(ctx context.Context, ref, dest string) (string, error)
}

func BuildBundle(ctx context.Context, in BundleInputs, workDir string) (BundleSpec, error) {
	bundle, err := Load(os.DirFS(in.ReleaseDir))
	if err != nil {
		return BundleSpec{}, err
	}
	if err := verifyBinary("k0s", in.K0sBinary, bundle.Spec.K0sChecksums[in.Arch]); err != nil {
		return BundleSpec{}, err
	}
	if err := verifyBinary("bedrock", in.BedrockBinary, bundle.Spec.BedrockChecksums[in.Arch]); err != nil {
		return BundleSpec{}, err
	}
	if err := copyTree(in.ReleaseDir, filepath.Join(workDir, bundleReleaseDir)); err != nil {
		return BundleSpec{}, err
	}
	if err := copyFileMode(in.K0sBinary, filepath.Join(workDir, bundleK0sPath), 0o755); err != nil {
		return BundleSpec{}, err
	}
	if err := copyFileMode(in.BedrockBinary, filepath.Join(workDir, bundleBedrockPath), 0o755); err != nil {
		return BundleSpec{}, err
	}
	imagesDir := filepath.Join(workDir, bundleImagesDir)
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return BundleSpec{}, err
	}
	if err := copyFileMode(in.K0sAirgap, filepath.Join(imagesDir, bundleAirgapFile), 0o644); err != nil {
		return BundleSpec{}, err
	}
	refs := mergeImages(bundle.Images, []string{bundle.Spec.Image})
	images := make([]BundleImage, 0, len(refs))
	imageDigest := ""
	for _, ref := range refs {
		file := ImageFileName(ref)
		digest, err := in.Pull(ctx, ref, filepath.Join(imagesDir, file))
		if err != nil {
			return BundleSpec{}, fmt.Errorf("image %s: %w", ref, err)
		}
		if ref == bundle.Spec.Image {
			imageDigest = digest
		}
		images = append(images, BundleImage{Ref: ref, Digest: digest, File: file})
	}
	spec := BundleSpec{Version: bundle.Spec.Version, Arch: in.Arch, Image: bundle.Spec.Image, ImageDigest: imageDigest, K0sVersion: bundle.Spec.K0sVersion, Images: images}
	raw, err := sigyaml.Marshal(spec)
	if err != nil {
		return BundleSpec{}, err
	}
	return spec, os.WriteFile(filepath.Join(workDir, BundleFileName), raw, 0o644)
}

func ImageFileName(ref string) string {
	name := unsafeRefChars.ReplaceAllString(ref, "_")
	if len(name) > 200 {
		name = name[:200]
	}
	return name + ".tar"
}

func verifyBinary(name, path, want string) error {
	if want == "" {
		return fmt.Errorf("%s: release has no checksum for this architecture", name)
	}
	got, err := FileSHA256(path)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if got != want {
		return fmt.Errorf("%s: checksum mismatch for %s", name, path)
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileMode(path, target, 0o644)
	})
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := CopyFile(src, dst); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func PackBundle(dir, out string) error {
	part := out + ".part"
	file, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		file.Close()
		os.Remove(part)
		return err
	}
	tw := tar.NewWriter(zw)
	writeErr := writeTarEntries(tw, dir)
	closeErr := firstError(writeErr, tw.Close(), zw.Close(), file.Close())
	if closeErr != nil {
		os.Remove(part)
		return closeErr
	}
	return os.Rename(part, out)
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func OpenBundle(path, dest string) (BundleSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return BundleSpec{}, err
	}
	defer file.Close()
	zr, err := zstd.NewReader(file)
	if err != nil {
		return BundleSpec{}, err
	}
	defer zr.Close()
	if err := extractAll(tar.NewReader(zr), dest); err != nil {
		return BundleSpec{}, err
	}
	return LoadBundleSpec(dest)
}

func LoadBundleSpec(dir string) (BundleSpec, error) {
	raw, err := os.ReadFile(filepath.Join(dir, BundleFileName))
	if err != nil {
		return BundleSpec{}, err
	}
	var spec BundleSpec
	if err := sigyaml.Unmarshal(raw, &spec); err != nil {
		return BundleSpec{}, fmt.Errorf("%s: %w", BundleFileName, err)
	}
	return spec, nil
}

func extractAll(tr *tar.Reader, dest string) error {
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel := filepath.Clean(filepath.FromSlash(header.Name))
		if hasUnsafeComponent(rel) || filepath.IsAbs(rel) {
			return fmt.Errorf("bundle entry %q escapes the destination", header.Name)
		}
		target := filepath.Join(dest, rel)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("bundle entry %q has unsupported type %c", header.Name, header.Typeflag)
		}
	}
}
