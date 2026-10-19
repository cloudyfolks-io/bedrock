package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	retiredStamp = "20261005T000000Z"
	olderStamp   = "20261001T100000Z"
	newerStamp   = "20261006T020000Z"
)

func retiredPaths(root string) (key, config string) {
	return filepath.Join(root, "etc/bedrock/backup/retired", retiredStamp+".key"),
		filepath.Join(root, "etc/bedrock/encryption/retired", retiredStamp+".yaml")
}

func newRetiredRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	key, config := retiredPaths(root)
	writeFixtureFile(t, key, "key")
	writeFixtureFile(t, config, "config")
	return root
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestPruneRetiredKeepsFilesWhileAnOlderArchiveExists(t *testing.T) {
	archives := map[string]string{
		"sealed etcd snapshot": "etcd-" + olderStamp + ".tar.gz.age",
		"upgrade backup":       "bedrock-v0.2.0-" + olderStamp + ".tar.gz.age",
		"plain snapshot":       "etcd-" + olderStamp + ".tar.gz",
		"partial snapshot":     "etcd-" + olderStamp + ".tar.gz.age.partial",
		"archive at the stamp": "etcd-" + retiredStamp + ".tar.gz.age",
	}
	for name, archive := range archives {
		root := newRetiredRoot(t)
		writeFixtureFile(t, filepath.Join(root, backupDir, archive), "x")
		writeFixtureFile(t, filepath.Join(root, backupDir, "etcd-"+newerStamp+".tar.gz.age"), "x")
		if err := pruneRetired(root); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		key, config := retiredPaths(root)
		if !exists(key) || !exists(config) {
			t.Errorf("%s: the retired files must stay", name)
		}
	}
}

func TestPruneRetiredDeletesFilesWhenOnlyNewerArchivesRemain(t *testing.T) {
	root := newRetiredRoot(t)
	writeFixtureFile(t, filepath.Join(root, backupDir, "etcd-"+newerStamp+".tar.gz.age"), "x")
	writeFixtureFile(t, filepath.Join(root, backupDir, "bedrock-v0.2.0-"+newerStamp+".tar.gz.age"), "x")
	if err := pruneRetired(root); err != nil {
		t.Fatal(err)
	}
	key, config := retiredPaths(root)
	if exists(key) || exists(config) {
		t.Fatal("no archive needs the retired files any more")
	}
}

func TestPruneRetiredDeletesFilesWhenNoArchiveExists(t *testing.T) {
	root := newRetiredRoot(t)
	if err := pruneRetired(root); err != nil {
		t.Fatal(err)
	}
	key, config := retiredPaths(root)
	if exists(key) || exists(config) {
		t.Fatal("no archive needs the retired files")
	}
}

func TestPruneRetiredLeavesOtherFilesAlone(t *testing.T) {
	root := newRetiredRoot(t)
	keyDir := filepath.Join(root, "etc/bedrock/backup/retired")
	configDir := filepath.Join(root, "etc/bedrock/encryption/retired")
	for _, path := range []string{filepath.Join(keyDir, "notes.txt"), filepath.Join(keyDir, "garbage.key"), filepath.Join(configDir, "notes.txt"), filepath.Join(root, "etc/bedrock/backup/host.key"), filepath.Join(root, "etc/bedrock/encryption/encryption.yaml")} {
		writeFixtureFile(t, path, "keep")
	}
	writeFixtureFile(t, filepath.Join(root, backupDir, "readme-without-a-stamp.tar.gz.age"), "x")
	if err := pruneRetired(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(keyDir, "notes.txt"), filepath.Join(keyDir, "garbage.key"), filepath.Join(configDir, "notes.txt"), filepath.Join(root, "etc/bedrock/backup/host.key"), filepath.Join(root, "etc/bedrock/encryption/encryption.yaml")} {
		if !exists(path) {
			t.Errorf("%s must stay", path)
		}
	}
}

func TestPruneRetiredWithoutDirectoriesIsNotAnError(t *testing.T) {
	if err := pruneRetired(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestTidyBackupDirPrunesRetiredFiles(t *testing.T) {
	deps := newDeps(nil, time.Now())
	deps.Root = newRetiredRoot(t)
	if err := tidyBackupDir(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	key, config := retiredPaths(deps.Root)
	if exists(key) || exists(config) {
		t.Fatal("the tidy step must prune the retired files")
	}
}
