package agent

import (
	"context"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestPruneKeepsTheTarballOfTheOperatorImage(t *testing.T) {
	env, _ := finishEnv(t, nil)
	env.Target.Spec.Image = "quay.io/a/operator:3@sha256:4444444444444444444444444444444444444444444444444444444444444444"
	root := env.Deps.Root
	writeArchived(t, root, "v0.2.0", release.ImageFileName(oldOnly))
	writeArchived(t, root, "v0.3.0", release.ImageFileName(newOnly), release.ImageFileName(env.Target.Spec.Image))
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if !exists(archiveFile(root, "v0.3.0", release.ImageFileName(env.Target.Spec.Image))) {
		t.Fatal("the operator image tarball is part of the release and must stay")
	}
}

func TestPruneRemovesAStaleTarballFromTheKeptVersion(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	writeArchived(t, root, "v0.3.0", release.ImageFileName(newOnly), release.ImageFileName(oldOnly))
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if exists(archiveFile(root, "v0.3.0", release.ImageFileName(oldOnly))) || !exists(archiveFile(root, "v0.3.0", release.ImageFileName(newOnly))) {
		t.Fatal("prune keeps exactly the files the target list names")
	}
}
