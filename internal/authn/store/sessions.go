package store

import (
	"context"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	sessionCookieLength = 43
	lastSeenInterval    = time.Minute
)

func (s *Store) CreateSession(ctx context.Context, subject methods.Subject, userAgent, clientIP string) (string, error) {
	settings, err := s.settings(ctx)
	if err != nil {
		return "", err
	}
	cookie, err := secret.Base62(s.random, sessionCookieLength)
	if err != nil {
		return "", err
	}
	now := s.clock()
	name := secret.SHA256Hex(cookie)
	session := v1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace, Labels: objectLabels("Session", name)},
		Spec: v1alpha1.SessionSpec{
			UserRef:   subject.User.Name,
			AMR:       slices.Clone(subject.AMR),
			AuthTime:  metav1.NewTime(now),
			UserAgent: userAgent,
			ClientIP:  clientIP,
			ExpiresAt: metav1.NewTime(now.Add(settings.SessionTTL)),
		},
	}
	if err := s.client.Create(ctx, &session, authnOwner()); err != nil {
		return "", err
	}
	return cookie, nil
}

func (s *Store) SessionByCookie(ctx context.Context, cookie string) (v1alpha1.Session, error) {
	var session v1alpha1.Session
	if err := s.reader.Get(ctx, objectKey(secret.SHA256Hex(cookie)), &session); err != nil {
		return v1alpha1.Session{}, err
	}
	now := s.clock()
	if due(session.Spec.ExpiresAt, now) {
		return v1alpha1.Session{}, notFoundError{kind: "Session"}
	}
	if _, err := s.Subject(ctx, session.Spec.UserRef); err != nil {
		return v1alpha1.Session{}, err
	}
	if !seenDue(session.Status.LastSeen, now) {
		return session, nil
	}
	seen := session.DeepCopy()
	seen.Status.LastSeen = &metav1.Time{Time: now}
	if err := s.client.Status().Update(ctx, seen, authnOwner()); err != nil && !apierrors.IsConflict(err) {
		return v1alpha1.Session{}, err
	}
	return *seen, nil
}

func seenDue(lastSeen *metav1.Time, now time.Time) bool {
	return lastSeen == nil || !now.Before(lastSeen.Add(lastSeenInterval))
}
