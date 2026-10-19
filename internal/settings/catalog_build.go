package settings

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/cloudyfolks-io/bedrock/internal/dnsname"
)

const (
	DefaultBuildGitImage       = "alpine/git:v2.54.0"
	DefaultBuildBuildKitImage  = "moby/buildkit:v0.31.2"
	DefaultBuildBuilderImage   = "paketobuildpacks/builder-jammy-base:0.4.640"
	DefaultBuildRunImage       = "paketobuildpacks/run-jammy-base:0.1.275"
	DefaultBuildTimeout        = "30m"
	DefaultBuildMaxTimeout     = "2h"
	DefaultBuildJobTTL         = "1h"
	minBuildDuration           = time.Minute
	maxBuildDuration           = 24 * time.Hour
	registryMirrorSeparator    = ","
	registryMirrorAssignment   = "="
	maxRegistryMirrorEntrySize = 512
	missingTag                 = "bedrock-no-tag-given"
)

type RegistryMirror struct {
	Upstream string
	Target   string
}

func buildDefinitions() []Definition {
	return []Definition{
		{"build.git-image", DefaultBuildGitImage, "Image of the git-clone step of a Build. It must hold git and sh, and run as user 1001.", imageReference},
		{"build.buildkit-image", DefaultBuildBuildKitImage, "Image that runs BuildKit for a Build of type dockerfile. It must be the image without the rootless suffix: the build pod is privileged inside a Kata microVM.", imageReference},
		{"build.builder-image", DefaultBuildBuilderImage, "Cloud Native Buildpacks builder image for a Build of type buildpacks. It is not in the offline bundle: serve it from a registry mirror on an air-gapped cluster.", imageReference},
		{"build.run-image", DefaultBuildRunImage, "Cloud Native Buildpacks run image for a Build of type buildpacks. It is not in the offline bundle: serve it from a registry mirror on an air-gapped cluster.", imageReference},
		{"build.default-timeout", DefaultBuildTimeout, "Time limit of a Build that sets no spec.timeout. From 1m to 24h.", wholeSecondsBetween(minBuildDuration, maxBuildDuration)},
		{"build.max-timeout", DefaultBuildMaxTimeout, "Largest spec.timeout that a Build can ask for. A Build above it gets the reason InvalidSpec. From 1m to 24h.", wholeSecondsBetween(minBuildDuration, maxBuildDuration)},
		{"build.job-ttl", DefaultBuildJobTTL, "Time that the Job and the pod of a finished Build stay on the cluster. The logs are available during this time. From 1m to 24h.", wholeSecondsBetween(minBuildDuration, maxBuildDuration)},
		{"build.registry-mirrors", "", "Comma list of <upstream>=<host>/<path> entries. BuildKit reads FROM lines of the upstream registry from the mirror. Empty sets no mirror.", registryMirrorsValue},
	}
}

func imageReference(v string) error {
	if v == "" {
		return nil
	}
	if strings.ContainsFunc(v, unicode.IsSpace) {
		return fmt.Errorf("value is not an image reference")
	}
	ref, err := name.ParseReference(v, name.WithDefaultTag(missingTag))
	if err != nil || ref.Identifier() == missingTag {
		return fmt.Errorf("value is not an image reference with a tag or a digest")
	}
	return nil
}

func registryMirrorsValue(v string) error {
	_, err := ParseRegistryMirrors(v)
	return err
}

func ParseRegistryMirrors(v string) ([]RegistryMirror, error) {
	if v == "" {
		return nil, nil
	}
	entries := strings.Split(v, registryMirrorSeparator)
	mirrors := make([]RegistryMirror, len(entries))
	for i, entry := range entries {
		mirror, err := parseRegistryMirror(entry)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i+1, err)
		}
		mirrors[i] = mirror
	}
	return mirrors, nil
}

func parseRegistryMirror(entry string) (RegistryMirror, error) {
	upstream, target, found := strings.Cut(entry, registryMirrorAssignment)
	if !found || len(entry) > maxRegistryMirrorEntrySize {
		return RegistryMirror{}, fmt.Errorf("must be <upstream>=<host>/<path>")
	}
	if !registryHost(upstream) {
		return RegistryMirror{}, fmt.Errorf("the upstream must be a registry host name")
	}
	host, path, hasPath := strings.Cut(target, "/")
	if !registryHost(host) || !hasPath || path == "" || strings.HasSuffix(path, "/") || strings.ContainsFunc(path, invalidMirrorPathRune) {
		return RegistryMirror{}, fmt.Errorf("the mirror must be <host>/<path> with no scheme")
	}
	return RegistryMirror{Upstream: upstream, Target: target}, nil
}

func registryHost(v string) bool {
	host, port, hasPort := strings.Cut(v, ":")
	if hasPort && (port == "" || strings.ContainsFunc(port, func(r rune) bool { return r < '0' || r > '9' })) {
		return false
	}
	return dnsname.Valid(host)
}

func invalidMirrorPathRune(r rune) bool {
	return !(unicode.IsLower(r) || unicode.IsDigit(r) || r == '/' || r == '.' || r == '_' || r == '-')
}
