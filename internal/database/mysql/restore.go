package mysql

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const succeededState = "Succeeded"

var (
	ErrNoBackup = errors.New("the source has no completed MySQL backup")

	backupDirectory = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}-\d{2}:\d{2}:\d{2})-full$`)
)

type DirectoryLister interface {
	ListDirectories(ctx context.Context, bucket, prefix string) ([]string, error)
}

func NewestDestination(backups []unstructured.Unstructured) (string, bool) {
	var newest string
	var newestAt time.Time
	for _, backup := range backups {
		state, _, _ := unstructured.NestedString(backup.Object, "status", "state")
		destination, _, _ := unstructured.NestedString(backup.Object, "status", "destination")
		if state != succeededState || destination == "" {
			continue
		}
		completed, _, _ := unstructured.NestedString(backup.Object, "status", "completed")
		at, err := time.Parse(time.RFC3339, completed)
		if err != nil {
			at = backup.GetCreationTimestamp().Time
		}
		if newest == "" || at.After(newestAt) {
			newest, newestAt = destination, at
		}
	}
	return newest, newest != ""
}

func newestDirectory(directories []string) (string, bool) {
	type candidate struct{ name, stamp string }
	var found []candidate
	for _, directory := range directories {
		name := strings.Trim(directory, "/")
		if name = name[strings.LastIndex(name, "/")+1:]; backupDirectory.MatchString(name) {
			found = append(found, candidate{name: name, stamp: backupDirectory.FindStringSubmatch(name)[1]})
		}
	}
	if len(found) == 0 {
		return "", false
	}
	sort.Slice(found, func(i, j int) bool { return found[i].stamp > found[j].stamp })
	return found[0].name, true
}

func DiscoverDestination(ctx context.Context, lister DirectoryLister, bucket, sourceUID string) (string, error) {
	directories, err := lister.ListDirectories(ctx, bucket, sourceUID+"/")
	if err != nil {
		return "", err
	}
	name, ok := newestDirectory(directories)
	if !ok {
		return "", ErrNoBackup
	}
	return fmt.Sprintf("s3://%s/%s/%s", bucket, sourceUID, name), nil
}

func ResolveDestination(ctx context.Context, backups []unstructured.Unstructured, lister DirectoryLister, bucket, sourceUID string) (string, error) {
	if destination, ok := NewestDestination(backups); ok {
		return destination, nil
	}
	return DiscoverDestination(ctx, lister, bucket, sourceUID)
}
