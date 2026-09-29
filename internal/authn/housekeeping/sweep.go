package housekeeping

import (
	"context"
	"errors"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const pendingCredentialLifetime = time.Hour

const orphanSigningKeyAge = 10 * time.Minute

type Swept struct {
	AuthRequests, AuthCodes, RefreshTokens, Sessions, DeviceRequests, APITokens, SigningKeys, PendingCredentials int
}

func Sweep(ctx context.Context, c client.Client, now time.Time) (Swept, error) {
	var swept Swept
	var errs error
	steps := []struct {
		count *int
		list  client.ObjectList
		match func(client.Object) bool
	}{
		{&swept.AuthRequests, &v1alpha1.AuthRequestList{}, func(o client.Object) bool { return due(o.(*v1alpha1.AuthRequest).Spec.ExpiresAt, now) }},
		{&swept.AuthCodes, &v1alpha1.AuthCodeList{}, func(o client.Object) bool { return due(o.(*v1alpha1.AuthCode).Spec.ExpiresAt, now) }},
		{&swept.RefreshTokens, &v1alpha1.RefreshTokenList{}, func(o client.Object) bool { return due(o.(*v1alpha1.RefreshToken).Spec.ExpiresAt, now) }},
		{&swept.Sessions, &v1alpha1.SessionList{}, func(o client.Object) bool { return due(o.(*v1alpha1.Session).Spec.ExpiresAt, now) }},
		{&swept.DeviceRequests, &v1alpha1.DeviceRequestList{}, func(o client.Object) bool { return due(o.(*v1alpha1.DeviceRequest).Spec.ExpiresAt, now) }},
		{&swept.APITokens, &v1alpha1.APITokenList{}, func(o client.Object) bool { return apiTokenExpired(*o.(*v1alpha1.APIToken), now) }},
		{&swept.PendingCredentials, &v1alpha1.CredentialList{}, func(o client.Object) bool { return pendingTooLong(*o.(*v1alpha1.Credential), now) }},
	}
	for _, step := range steps {
		count, err := deleteMatching(ctx, c, step.list, step.match)
		*step.count = count
		errs = errors.Join(errs, err)
	}
	signingKeys, err := sweepSigningKeys(ctx, c, now)
	swept.SigningKeys = signingKeys
	errs = errors.Join(errs, err)
	return swept, errs
}

func Rotate(ctx context.Context, c client.Client, random io.Reader, now time.Time) error {
	loaded, err := keys.Load(ctx, c)
	if err != nil {
		return err
	}
	notBefore, needed := keys.NextNotBefore(loaded, now)
	if !needed {
		return nil
	}
	key, err := keys.Generate(random, notBefore)
	if err != nil {
		return err
	}
	err = keys.Save(ctx, c, key)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func sweepSigningKeys(ctx context.Context, c client.Client, now time.Time) (int, error) {
	var errs error
	count := 0
	loaded, err := keys.Load(ctx, c)
	if err != nil {
		errs = errors.Join(errs, err)
	}
	for _, key := range keys.Expired(loaded, now) {
		if err := keys.Delete(ctx, c, key); err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		count++
	}
	var list v1alpha1.SigningKeyList
	if err := c.List(ctx, &list, client.InNamespace(release.SystemNamespace)); err != nil {
		return count, errors.Join(errs, err)
	}
	for _, item := range list.Items {
		if now.Before(item.CreationTimestamp.Add(orphanSigningKeyAge)) {
			continue
		}
		usable, err := keys.Usable(ctx, c, item)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if usable {
			continue
		}
		uid := item.GetUID()
		if err := c.Delete(ctx, &item, client.Preconditions{UID: &uid}); err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
				continue
			}
			errs = errors.Join(errs, err)
			continue
		}
		count++
	}
	return count, errs
}

func deleteMatching(ctx context.Context, c client.Client, list client.ObjectList, match func(client.Object) bool) (int, error) {
	if err := c.List(ctx, list, client.InNamespace(release.SystemNamespace)); err != nil {
		return 0, err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok || !match(obj) {
			continue
		}
		uid := obj.GetUID()
		err := c.Delete(ctx, obj, client.Preconditions{UID: &uid})
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func due(expiresAt metav1.Time, now time.Time) bool {
	return !now.Before(expiresAt.Time)
}

func apiTokenExpired(token v1alpha1.APIToken, now time.Time) bool {
	return token.Spec.ExpiresAt != nil && due(*token.Spec.ExpiresAt, now)
}

func pendingTooLong(credential v1alpha1.Credential, now time.Time) bool {
	return credential.Spec.Method == v1alpha1.MethodTOTP &&
		credential.Status.EnrolledAt == nil &&
		!now.Before(enrollWriteAt(credential).Add(pendingCredentialLifetime))
}

func enrollWriteAt(credential v1alpha1.Credential) time.Time {
	if raw, ok := credential.Annotations[v1alpha1.AnnotationEnrollWriteAt]; ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed
		}
	}
	return credential.CreationTimestamp.Time
}
