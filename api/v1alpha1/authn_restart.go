package v1alpha1

import "time"

const (
	AnnotationAuthnRestart = "bedrock.cloudyfolks.io/authn-restart"

	AuthnRestartGrantLifetime = 15 * time.Minute
)

func AuthnRestartGrantTime(host Host) (time.Time, bool) {
	granted, err := time.Parse(time.RFC3339, host.Annotations[AnnotationAuthnRestart])
	return granted, err == nil
}

func AuthnRestartGrantFresh(host Host, now time.Time) bool {
	granted, ok := AuthnRestartGrantTime(host)
	return ok && now.Before(granted.Add(AuthnRestartGrantLifetime))
}
