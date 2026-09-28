package release

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	ComponentLabel = "bedrock.cloudyfolks.io/component"
	FieldManager   = "bedrock-release"
	releaseFile    = "release.yaml"
	imagesFile     = "images.txt"
	manifestsDir   = "manifests"
)

var GroupDirPattern = regexp.MustCompile(`^([0-9]{2})-([a-z0-9-]+)$`)

type Group struct {
	Order   int
	Name    string
	Objects []*unstructured.Unstructured
}

type Bundle struct {
	Spec   v1alpha1.ReleaseSpec
	Groups []Group
	Images []string
}

func Load(fsys fs.FS) (Bundle, error) {
	spec, err := loadSpec(fsys)
	if err != nil {
		return Bundle{}, err
	}
	groups, err := loadGroups(fsys)
	if err != nil {
		return Bundle{}, err
	}
	images, err := loadImages(fsys)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Spec: spec, Groups: groups, Images: images}, nil
}

func loadSpec(fsys fs.FS) (v1alpha1.ReleaseSpec, error) {
	raw, err := fs.ReadFile(fsys, releaseFile)
	if err != nil {
		return v1alpha1.ReleaseSpec{}, fmt.Errorf("read %s: %w", releaseFile, err)
	}
	var spec v1alpha1.ReleaseSpec
	if err := sigyaml.UnmarshalStrict(raw, &spec); err != nil {
		return v1alpha1.ReleaseSpec{}, fmt.Errorf("parse %s: %w", releaseFile, err)
	}
	if spec.Version == "" {
		return v1alpha1.ReleaseSpec{}, fmt.Errorf("%s: version is required", releaseFile)
	}
	return spec, nil
}

func loadGroups(fsys fs.FS) ([]Group, error) {
	entries, err := fs.ReadDir(fsys, manifestsDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestsDir, err)
	}
	groups := make([]Group, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		match := GroupDirPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("group directory %q must match %s", entry.Name(), GroupDirPattern)
		}
		order, _ := strconv.Atoi(match[1])
		objects, err := loadObjects(fsys, path.Join(manifestsDir, entry.Name()), match[2])
		if err != nil {
			return nil, err
		}
		groups = append(groups, Group{Order: order, Name: match[2], Objects: objects})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Order < groups[j].Order })
	return groups, nil
}

func loadObjects(fsys fs.FS, dir, component string) ([]*unstructured.Unstructured, error) {
	files, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var objects []*unstructured.Unstructured
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(dir, file.Name()))
		if err != nil {
			return nil, err
		}
		docs, err := splitDocuments(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, file.Name(), err)
		}
		for _, doc := range docs {
			objects = append(objects, withComponentLabel(doc, component))
		}
	}
	return objects, nil
}

func splitDocuments(raw []byte) ([]*unstructured.Unstructured, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var docs []*unstructured.Unstructured
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err == io.EOF {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if len(obj.Object) == 0 {
			continue
		}
		if obj.GetKind() == "" || obj.GetAPIVersion() == "" {
			return nil, fmt.Errorf("document without apiVersion or kind")
		}
		docs = append(docs, &obj)
	}
}

func withComponentLabel(obj *unstructured.Unstructured, component string) *unstructured.Unstructured {
	out := obj.DeepCopy()
	labels := out.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ComponentLabel] = component
	out.SetLabels(labels)
	return out
}

func loadImages(fsys fs.FS) ([]string, error) {
	raw, err := fs.ReadFile(fsys, imagesFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", imagesFile, err)
	}
	var images []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			images = append(images, line)
		}
	}
	return images, scanner.Err()
}
