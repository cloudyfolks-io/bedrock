package release

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type Resolver func(ctx context.Context, ref string) (string, error)

const (
	digestWorkers  = 8
	digestTimeout  = 30 * time.Second
	digestsTimeout = 3 * time.Minute
)

func RemoteDigest(ctx context.Context, ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	desc, err := remote.Get(parsed, remote.WithContext(ctx))
	if err != nil {
		return "", err
	}
	return desc.Digest.String(), nil
}

func ResolveDigests(ctx context.Context, images []string, skipPrefix string, resolve Resolver) (map[string]string, error) {
	refs := make([]string, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		if strings.Contains(image, "@sha256:") || (skipPrefix != "" && strings.HasPrefix(image, skipPrefix)) {
			continue
		}
		if _, found := seen[image]; !found {
			refs = append(refs, image)
			seen[image] = struct{}{}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, digestsTimeout)
	defer cancel()
	type resolution struct {
		digest string
		err    error
	}
	jobs := make(chan int, len(refs))
	results := make([]resolution, len(refs))
	for i := range refs {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(digestWorkers, len(refs)) {
		workers.Go(func() {
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					results[i].err = err
					continue
				}
				refCtx, cancel := context.WithTimeout(ctx, digestTimeout)
				results[i].digest, results[i].err = resolve(refCtx, refs[i])
				cancel()
			}
		})
	}
	workers.Wait()
	pins := make(map[string]string, len(refs))
	for i, result := range results {
		if result.err != nil {
			return nil, fmt.Errorf("resolve %s: %w", refs[i], result.err)
		}
		pins[refs[i]] = pinnedReference(refs[i], result.digest)
	}
	return pins, nil
}

func pinnedReference(image, digest string) string {
	base, _, _ := strings.Cut(image, "@")
	return base + "@" + digest
}

func PinImages(files []rendered, pins map[string]string, skip map[string]struct{}) []rendered {
	toPin := make([]rendered, 0, len(files))
	unchanged := make([]rendered, 0, len(files))
	for _, file := range files {
		if _, found := skip[file.group]; found {
			unchanged = append(unchanged, file)
			continue
		}
		toPin = append(toPin, file)
	}
	pinnedFiles := rewriteImageRefs(toPin, func(current string) (string, bool) {
		pinned, found := pins[current]
		return pinned, found
	})
	return append(pinnedFiles, unchanged...)
}
