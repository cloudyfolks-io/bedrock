package release

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const bedrockImagePrefix = "ghcr.io/cloudyfolks-labs/bedrock:"

type ComponentConfig struct {
	Name        string   `json:"name"`
	Order       int      `json:"order"`
	Dirs        []string `json:"dirs,omitempty"`
	Chart       string   `json:"chart,omitempty"`
	Version     string   `json:"version,omitempty"`
	Namespace   string   `json:"namespace,omitempty"`
	Values      string   `json:"values,omitempty"`
	CRDsToGroup string   `json:"crdsToGroup,omitempty"`
	Image       string   `json:"image,omitempty"`
	ExtraImages []string `json:"extraImages,omitempty"`
	KeepTags    bool     `json:"keepTags,omitempty"`
}

type BuildConfig struct {
	K0sVersion  string            `json:"k0sVersion"`
	SupportedOS []string          `json:"supportedOS"`
	UpgradeFrom []string          `json:"upgradeFrom"`
	Components  []ComponentConfig `json:"components"`
}

type BuildOptions struct {
	Version     string
	Image       string
	Out         string
	Helm        string
	Root        string
	K0sBaseURL  string
	CacheDir    string
	PinDigests  bool
	Resolve     Resolver
	K0sVersion  string
	UpgradeFrom []string
	Binaries    string
}

var releaseArches = []string{"amd64", "arm64"}

func LoadBuildConfig(path string) (BuildConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return BuildConfig{}, err
	}
	var cfg BuildConfig
	if err := sigyaml.UnmarshalStrict(raw, &cfg); err != nil {
		return BuildConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, component := range cfg.Components {
		if !GroupDirPattern.MatchString(fmt.Sprintf("%02d-%s", component.Order, component.Name)) {
			return BuildConfig{}, fmt.Errorf("component %q has an invalid name or order", component.Name)
		}
	}
	return cfg, nil
}

type rendered struct {
	group   string
	file    string
	objects []*unstructured.Unstructured
}

func groupDir(component ComponentConfig) string {
	return fmt.Sprintf("%02d-%s", component.Order, component.Name)
}

func groupDirs(cfg BuildConfig) map[string]string {
	dirs := map[string]string{}
	for _, component := range cfg.Components {
		dirs[component.Name] = groupDir(component)
	}
	return dirs
}

func validateOut(out string) error {
	if out == "" {
		return fmt.Errorf("out must not be empty")
	}
	if filepath.Clean(out) == "/" {
		return fmt.Errorf("out %q is not a safe output directory", out)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if abs == cwd {
		return fmt.Errorf("out %q is not a safe output directory", out)
	}
	return nil
}

func Build(cfg BuildConfig, opts BuildOptions) error {
	if err := validateOut(opts.Out); err != nil {
		return err
	}
	cfg = withOverrides(cfg, opts)
	dirs := groupDirs(cfg)
	var files []rendered
	for _, component := range cfg.Components {
		out, err := renderComponent(component, dirs, opts)
		if err != nil {
			return fmt.Errorf("component %s: %w", component.Name, err)
		}
		files = append(files, out...)
	}
	files = rewriteBedrockImage(files, opts.Image)
	if opts.PinDigests {
		resolve := opts.Resolve
		if resolve == nil {
			resolve = RemoteDigest
		}
		provisional := loadRenderedGroups(files)
		pins, err := ResolveDigests(context.Background(), mergeImages(ImagesOf(provisional), extraImagesOf(cfg)), opts.Image, resolve)
		if err != nil {
			return err
		}
		files = PinImages(files, pins, keepTagGroups(cfg))
		cfg = pinExtraImages(cfg, pins)
	}
	if err := writeManifests(cfg, files, opts.Out); err != nil {
		return err
	}
	groups, err := loadGroups(os.DirFS(opts.Out))
	if err != nil {
		return err
	}
	images := sortedImages(mergeImages(ImagesOf(groups), extraImagesOf(cfg)))
	spec := v1alpha1.ReleaseSpec{
		Version:     opts.Version,
		Image:       opts.Image,
		K0sVersion:  cfg.K0sVersion,
		UpgradeFrom: cfg.UpgradeFrom,
		SupportedOS: cfg.SupportedOS,
		Components:  componentSpecs(cfg, groups, opts.Version),
		Images:      images,
	}
	if opts.Binaries != "" {
		sums, err := binaryChecksums(opts.Binaries, opts.Version, releaseArches)
		if err != nil {
			return err
		}
		spec.BedrockChecksums = sums
	}
	if opts.K0sBaseURL != "" {
		sums, err := K0sChecksums(context.Background(), opts.K0sBaseURL, cfg.K0sVersion, releaseArches, opts.CacheDir)
		if err != nil {
			return err
		}
		spec.K0sChecksums = sums
	}
	return writeMetadata(opts.Out, spec, images)
}

func withOverrides(cfg BuildConfig, opts BuildOptions) BuildConfig {
	out := cfg
	if opts.K0sVersion != "" {
		out.K0sVersion = opts.K0sVersion
	}
	if opts.UpgradeFrom != nil {
		out.UpgradeFrom = append([]string(nil), opts.UpgradeFrom...)
	}
	return out
}

func BinaryName(version, arch string) string {
	return fmt.Sprintf("bedrock-%s-linux-%s", version, arch)
}

func binaryChecksums(dir, version string, arches []string) (map[string]string, error) {
	sums := make(map[string]string, len(arches))
	for _, arch := range arches {
		sum, err := FileSHA256(filepath.Join(dir, BinaryName(version, arch)))
		if err != nil {
			return nil, fmt.Errorf("bedrock binary: %w", err)
		}
		sums[arch] = sum
	}
	return sums, nil
}

func sortedImages(images []string) []string {
	sorted := append([]string(nil), images...)
	sort.Strings(sorted)
	return sorted
}

func extraImagesOf(cfg BuildConfig) []string {
	var images []string
	for _, component := range cfg.Components {
		if component.Image != "" {
			images = append(images, component.Image)
		}
		images = append(images, component.ExtraImages...)
	}
	return images
}

func keepTagGroups(cfg BuildConfig) map[string]struct{} {
	groups := map[string]struct{}{}
	for _, component := range cfg.Components {
		if component.KeepTags {
			groups[groupDir(component)] = struct{}{}
		}
	}
	return groups
}

func loadRenderedGroups(files []rendered) []Group {
	byGroup := map[string][]*unstructured.Unstructured{}
	for _, file := range files {
		byGroup[file.group] = append(byGroup[file.group], file.objects...)
	}
	groups := make([]Group, 0, len(byGroup))
	for name, objects := range byGroup {
		groups = append(groups, Group{Name: name, Objects: objects})
	}
	return groups
}

func pinExtraImages(cfg BuildConfig, pins map[string]string) BuildConfig {
	components := make([]ComponentConfig, 0, len(cfg.Components))
	for _, component := range cfg.Components {
		extra := make([]string, 0, len(component.ExtraImages))
		for _, image := range component.ExtraImages {
			extra = append(extra, pinned(image, pins))
		}
		component.ExtraImages = extra
		component.Image = pinned(component.Image, pins)
		components = append(components, component)
	}
	cfg.Components = components
	return cfg
}

func pinned(image string, pins map[string]string) string {
	if replacement, ok := pins[image]; ok {
		return replacement
	}
	return image
}

func mergeImages(images, extra []string) []string {
	seen := map[string]struct{}{}
	merged := make([]string, 0, len(images)+len(extra))
	for _, image := range append(append([]string{}, images...), extra...) {
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		merged = append(merged, image)
	}
	return merged
}

func renderComponent(component ComponentConfig, dirs map[string]string, opts BuildOptions) ([]rendered, error) {
	var objects []*unstructured.Unstructured
	for _, dir := range component.Dirs {
		loaded, err := loadObjects(os.DirFS(opts.Root), dir, component.Name)
		if err != nil {
			return nil, err
		}
		objects = append(objects, stripComponentLabel(loaded)...)
	}
	if component.Chart != "" {
		templated, err := helmTemplate(component, opts)
		if err != nil {
			return nil, err
		}
		if component.Namespace != "" {
			templated, err = defaultNamespaces(templated, component.Namespace)
			if err != nil {
				return nil, err
			}
		}
		if component.Namespace != "" && component.Namespace != "kube-system" {
			templated = append([]*unstructured.Unstructured{namespaceObject(component.Namespace)}, templated...)
		}
		objects = append(objects, templated...)
	}
	return placeCRDs(component, dirs, objects)
}

func placeCRDs(component ComponentConfig, dirs map[string]string, objects []*unstructured.Unstructured) ([]rendered, error) {
	group := groupDir(component)
	file := component.Name + ".yaml"
	if component.CRDsToGroup == "" {
		return []rendered{{group: group, file: file, objects: objects}}, nil
	}
	target, ok := dirs[component.CRDsToGroup]
	if !ok {
		return nil, fmt.Errorf("crdsToGroup %q is not a component", component.CRDsToGroup)
	}
	crds, rest := partitionCRDs(objects)
	out := []rendered{{group: group, file: file, objects: rest}}
	if len(crds) > 0 {
		out = append([]rendered{{group: target, file: component.Name + "-crds.yaml", objects: crds}}, out...)
	}
	return out, nil
}

func helmTemplate(component ComponentConfig, opts BuildOptions) ([]*unstructured.Unstructured, error) {
	args := []string{"template", component.Name, filepath.Join(opts.Root, component.Chart), "--include-crds"}
	if component.Namespace != "" {
		args = append(args, "--namespace", component.Namespace)
	}
	if component.Values != "" {
		args = append(args, "-f", filepath.Join(opts.Root, component.Values))
	}
	cmd := exec.Command(opts.Helm, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("helm template: %w: %s", err, stderr.String())
	}
	return splitDocuments(stdout.Bytes())
}

func partitionCRDs(objects []*unstructured.Unstructured) ([]*unstructured.Unstructured, []*unstructured.Unstructured) {
	var crds, rest []*unstructured.Unstructured
	for _, obj := range objects {
		if obj.GetKind() == "CustomResourceDefinition" {
			crds = append(crds, obj)
			continue
		}
		rest = append(rest, obj)
	}
	return crds, rest
}

func namespaceObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name}}}
}

func stripComponentLabel(objects []*unstructured.Unstructured) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(objects))
	for _, obj := range objects {
		copy := obj.DeepCopy()
		labels := copy.GetLabels()
		delete(labels, ComponentLabel)
		if len(labels) == 0 {
			labels = nil
		}
		copy.SetLabels(labels)
		out = append(out, copy)
	}
	return out
}

func rewriteBedrockImage(files []rendered, image string) []rendered {
	return rewriteImageRefs(files, func(current string) (string, bool) {
		if strings.HasPrefix(current, bedrockImagePrefix) {
			return image, true
		}
		return "", false
	})
}

func rewriteImageRefs(files []rendered, decide func(current string) (string, bool)) []rendered {
	out := make([]rendered, 0, len(files))
	for _, file := range files {
		objects := make([]*unstructured.Unstructured, 0, len(file.objects))
		for _, obj := range file.objects {
			copy := obj.DeepCopy()
			walkImageFields(copy.Object, decide)
			objects = append(objects, copy)
		}
		out = append(out, rendered{group: file.group, file: file.file, objects: objects})
	}
	return out
}

func walkImageFields(node any, decide func(current string) (string, bool)) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "image" {
				if current, ok := child.(string); ok {
					if replacement, found := decide(current); found {
						value[key] = replacement
					}
					continue
				}
			}
			walkImageFields(child, decide)
		}
	case []any:
		for _, child := range value {
			walkImageFields(child, decide)
		}
	}
}

func writeManifests(cfg BuildConfig, files []rendered, out string) error {
	if err := os.RemoveAll(filepath.Join(out, manifestsDir)); err != nil {
		return err
	}
	for _, component := range cfg.Components {
		if err := os.MkdirAll(filepath.Join(out, manifestsDir, fmt.Sprintf("%02d-%s", component.Order, component.Name)), 0o755); err != nil {
			return err
		}
	}
	for _, file := range mergeRendered(files) {
		if len(file.objects) == 0 {
			continue
		}
		var buf bytes.Buffer
		for _, obj := range file.objects {
			raw, err := sigyaml.Marshal(obj.Object)
			if err != nil {
				return err
			}
			buf.WriteString("---\n")
			buf.Write(raw)
		}
		if err := os.WriteFile(filepath.Join(out, manifestsDir, file.group, file.file), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type renderedKey struct {
	group string
	file  string
}

func mergeRendered(files []rendered) []rendered {
	order := make([]renderedKey, 0, len(files))
	objectsByKey := map[renderedKey][]*unstructured.Unstructured{}
	for _, file := range files {
		key := renderedKey{group: file.group, file: file.file}
		if _, seen := objectsByKey[key]; !seen {
			order = append(order, key)
		}
		objectsByKey[key] = append(objectsByKey[key], file.objects...)
	}
	merged := make([]rendered, 0, len(order))
	for _, key := range order {
		merged = append(merged, rendered{group: key.group, file: key.file, objects: objectsByKey[key]})
	}
	return merged
}

func componentSpecs(cfg BuildConfig, groups []Group, version string) []v1alpha1.ReleaseComponent {
	byName := map[string]Group{}
	for _, group := range groups {
		byName[group.Name] = group
	}
	specs := make([]v1alpha1.ReleaseComponent, 0, len(cfg.Components))
	for _, component := range cfg.Components {
		componentVersion := component.Version
		if componentVersion == "" {
			componentVersion = version
		}
		specs = append(specs, v1alpha1.ReleaseComponent{Name: component.Name, Version: componentVersion, Image: primaryImage(component, ImagesOf([]Group{byName[component.Name]}))})
	}
	return specs
}

func primaryImage(component ComponentConfig, rendered []string) string {
	switch {
	case component.Image != "":
		return component.Image
	case len(rendered) > 0:
		return rendered[0]
	case len(component.ExtraImages) > 0:
		return component.ExtraImages[0]
	}
	return ""
}

func writeMetadata(out string, spec v1alpha1.ReleaseSpec, images []string) error {
	raw, err := sigyaml.Marshal(spec)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, releaseFile), raw, 0o644); err != nil {
		return err
	}
	sorted := append([]string(nil), images...)
	sort.Strings(sorted)
	return os.WriteFile(filepath.Join(out, imagesFile), []byte(strings.Join(sorted, "\n")+"\n"), 0o644)
}
