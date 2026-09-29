package login

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
)

func localUser(name string) *v1alpha1.User {
	return &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.UserSpec{Username: name, Methods: []string{v1alpha1.MethodPassword}},
	}
}

func sourcedUser(name, source string) *v1alpha1.User {
	return &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.UserSpec{Username: name, Source: source},
	}
}

func switchedOffUser(user *v1alpha1.User) *v1alpha1.User {
	copied := user.DeepCopy()
	copied.Spec.Disabled = true
	return copied
}

func oidcProvider(name, display string) v1alpha1.IdentityProvider {
	return v1alpha1.IdentityProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.IdentityProviderSpec{
			Type:        v1alpha1.MethodOIDC,
			DisplayName: display,
			OIDC:        &v1alpha1.OIDCProvider{Issuer: "https://" + name + ".example.test", ClientID: "bedrock"},
		},
	}
}

func ldapProvider(name string) v1alpha1.IdentityProvider {
	return v1alpha1.IdentityProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.IdentityProviderSpec{
			Type:        v1alpha1.MethodLDAP,
			DisplayName: "Corporate LDAP",
			LDAP:        &v1alpha1.LDAPProvider{URL: "ldaps://ldap.example.test", BindDN: "cn=reader"},
		},
	}
}

func switchedOffProvider(provider v1alpha1.IdentityProvider) v1alpha1.IdentityProvider {
	copied := provider.DeepCopy()
	copied.Spec.Disabled = true
	return *copied
}

func passed(user *v1alpha1.User, amr ...string) methods.Result {
	return methods.Result{Subject: &methods.Subject{User: *user, AMR: amr}}
}

func failedWith(code string) methods.Result {
	return methods.Result{Failure: code}
}

func afterPassword(t *testing.T, facts Facts) Step {
	t.Helper()
	step := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, facts.User.Spec.Username, facts)
	return AfterMethod(step.State, v1alpha1.MethodPassword, passed(facts.User, "pwd"), facts)
}

func TestStartShowsUsername(t *testing.T) {
	got := Start(Facts{})
	want := Step{
		State:     v1alpha1.LoginState{Step: methods.ChallengeUsername},
		Challenge: methods.Challenge{Type: methods.ChallengeUsername},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStartListsProviders(t *testing.T) {
	facts := Facts{Providers: []v1alpha1.IdentityProvider{
		oidcProvider("dex", "Dadehat SSO"),
		ldapProvider("corp"),
		switchedOffProvider(oidcProvider("old", "Old SSO")),
	}}
	got := Start(facts).Challenge
	want := []methods.ProviderChoice{{Name: "dex", DisplayName: "Dadehat SSO", Type: v1alpha1.MethodOIDC}}
	if got.Type != methods.ChallengeUsername || !reflect.DeepEqual(got.Providers, want) {
		t.Fatalf("challenge %+v, want username with providers %+v", got, want)
	}
}

func TestUnknownUserLooksLikeAKnownOne(t *testing.T) {
	want := methods.Challenge{Type: methods.ChallengePassword, Username: "alice"}
	cases := map[string]Facts{
		"known":    {User: localUser("alice"), Enrolled: []string{v1alpha1.MethodPassword}},
		"unknown":  {},
		"disabled": {User: switchedOffUser(localUser("alice"))},
	}
	for name, facts := range cases {
		step := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "alice", facts)
		if !reflect.DeepEqual(step.Challenge, want) {
			t.Fatalf("%s: challenge %+v, want %+v", name, step.Challenge, want)
		}
		if step.State.Primary != v1alpha1.MethodPassword || step.Begin != "" || step.Complete {
			t.Fatalf("%s: step %+v", name, step)
		}
	}
}

func TestPasswordOnlyWhenNoFactorIsRequired(t *testing.T) {
	step := afterPassword(t, Facts{User: localUser("alice"), Enrolled: []string{v1alpha1.MethodPassword}})
	if !step.Complete || step.Challenge.Type != methods.ChallengeDone {
		t.Fatalf("step %+v, want done", step)
	}
}

func TestLocalUserPasswordThenTOTP(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}}
	step := afterPassword(t, facts)
	if step.Challenge.Type != methods.ChallengeTOTP || step.Complete {
		t.Fatalf("step %+v, want totp", step)
	}
	if !reflect.DeepEqual(step.Challenge.Methods, []string{v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}) {
		t.Fatalf("methods %v", step.Challenge.Methods)
	}
	if !reflect.DeepEqual(step.State.Required, []string{v1alpha1.MethodTOTP}) {
		t.Fatalf("required %v", step.State.Required)
	}
	step = AfterMethod(step.State, v1alpha1.MethodTOTP, passed(alice, "otp"), facts)
	if !step.Complete || step.Challenge.Type != methods.ChallengeDone {
		t.Fatalf("step %+v, want done", step)
	}
	if !reflect.DeepEqual(step.State.Completed, []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP}) || len(step.State.Required) != 0 {
		t.Fatalf("state %+v", step.State)
	}
}

func TestSecondFactorFromGroup(t *testing.T) {
	ops := v1alpha1.Group{ObjectMeta: metav1.ObjectMeta{Name: "ops"}, Spec: v1alpha1.GroupSpec{Members: []string{"alice"}, RequireSecondFactor: true}}
	facts := Facts{User: localUser("alice"), Groups: []v1alpha1.Group{ops}, Enrolled: []string{v1alpha1.MethodPassword}}
	step := afterPassword(t, facts)
	if step.Challenge.Type != methods.ChallengeTOTPEnroll || step.State.Step != methods.ChallengeTOTPEnroll {
		t.Fatalf("step %+v, want totp-enroll", step)
	}
	outsider := Facts{User: localUser("bob"), Groups: []v1alpha1.Group{ops}, Enrolled: []string{v1alpha1.MethodPassword}}
	if step := afterPassword(t, outsider); !step.Complete {
		t.Fatalf("a user outside the group needs no second factor: %+v", step)
	}
}

func TestSecondFactorFromClient(t *testing.T) {
	client := v1alpha1.OAuthClient{Spec: v1alpha1.OAuthClientSpec{ClientID: "bedrock-console", RequireSecondFactor: true}}
	facts := Facts{User: localUser("alice"), Client: client, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP}}
	step := afterPassword(t, facts)
	if step.Challenge.Type != methods.ChallengeTOTP {
		t.Fatalf("step %+v, want totp", step)
	}
	if !reflect.DeepEqual(step.Challenge.Methods, []string{v1alpha1.MethodTOTP}) {
		t.Fatalf("methods %v, want only totp without enrolled recovery codes", step.Challenge.Methods)
	}
}

func TestEnrollInsideLogin(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Settings: policy.Settings{RequireSecondFactor: true}, Enrolled: []string{v1alpha1.MethodPassword}}
	step := afterPassword(t, facts)
	if step.Challenge.Type != methods.ChallengeTOTPEnroll {
		t.Fatalf("step %+v, want totp-enroll", step)
	}
	wrong := AfterMethod(step.State, v1alpha1.MethodTOTP, failedWith(methods.FailureInvalidCode), facts)
	if wrong.Challenge.Type != methods.ChallengeTOTPEnroll || wrong.Challenge.Error == nil || wrong.Challenge.Error.Code != methods.FailureInvalidCode || wrong.State.Step != methods.ChallengeTOTPEnroll {
		t.Fatalf("wrong code: %+v", wrong)
	}
	step = AfterMethod(wrong.State, v1alpha1.MethodTOTP, passed(alice, "otp"), facts)
	if step.Challenge.Type != methods.ChallengeTOTPEnroll || step.State.Step != stepRecoveryCodes || step.Complete || step.Challenge.Error != nil {
		t.Fatalf("step %+v, want the recovery codes step", step)
	}
	enrolled := Facts{User: alice, Settings: facts.Settings, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}}
	step = afterRecoveryCodes(step.State, enrolled)
	if !step.Complete || step.Challenge.Type != methods.ChallengeDone {
		t.Fatalf("step %+v, want done", step)
	}
}

func TestRecoveryInsteadOfTOTP(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}}
	totp := afterPassword(t, facts)
	switched := toRecovery(totp.State)
	if switched.Challenge.Type != methods.ChallengeRecovery || switched.State.Step != methods.ChallengeRecovery {
		t.Fatalf("switch %+v, want recovery", switched)
	}
	step := AfterMethod(switched.State, v1alpha1.MethodRecovery, passed(alice, "otp"), facts)
	if !step.Complete || !reflect.DeepEqual(step.State.Completed, []string{v1alpha1.MethodPassword, v1alpha1.MethodRecovery}) {
		t.Fatalf("step %+v, want done after a recovery code", step)
	}
	direct := AfterMethod(totp.State, v1alpha1.MethodRecovery, passed(alice, "otp"), facts)
	if !direct.Complete {
		t.Fatalf("a recovery code on the totp step must count: %+v", direct)
	}
}

func TestLDAPUserUsesBedrockTOTP(t *testing.T) {
	providers := []v1alpha1.IdentityProvider{ldapProvider("corp")}
	first := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "t.farahani", Facts{Providers: providers, LDAPProviders: []string{"corp"}})
	if first.State.Primary != v1alpha1.MethodLDAP || first.State.Provider != "corp" || first.Challenge.Type != methods.ChallengePassword {
		t.Fatalf("first login %+v, want ldap through corp behind a password challenge", first)
	}
	known := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "t.farahani", Facts{User: sourcedUser("t.farahani", "corp"), Providers: providers})
	if known.State.Primary != v1alpha1.MethodLDAP || known.State.Provider != "corp" {
		t.Fatalf("known ldap user %+v", known)
	}
	provisioned := sourcedUser("t.farahani", "corp")
	facts := Facts{User: provisioned, Providers: providers, Settings: policy.Settings{RequireSecondFactor: true}}
	step := AfterMethod(first.State, v1alpha1.MethodLDAP, passed(provisioned, "pwd"), facts)
	if step.Challenge.Type != methods.ChallengeTOTPEnroll {
		t.Fatalf("step %+v, want Bedrock TOTP enrollment", step)
	}
}

func TestOIDCUpstreamMFASatisfies(t *testing.T) {
	providers := []v1alpha1.IdentityProvider{oidcProvider("dex", "Dadehat SSO")}
	carol := sourcedUser("carol", "dex")
	redirect := afterProvider(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "dex", Facts{Providers: providers})
	if redirect.Begin != v1alpha1.MethodOIDC || redirect.Challenge.Type != methods.ChallengeRedirect || redirect.State.Provider != "dex" || redirect.State.Primary != v1alpha1.MethodOIDC {
		t.Fatalf("redirect %+v", redirect)
	}
	facts := Facts{User: carol, Providers: providers, Settings: policy.Settings{RequireSecondFactor: true}}
	withMFA := AfterMethod(redirect.State, v1alpha1.MethodOIDC, passed(carol, "mfa", "fed"), facts)
	if !withMFA.Complete {
		t.Fatalf("an upstream mfa must satisfy the policy: %+v", withMFA)
	}
	withoutMFA := AfterMethod(redirect.State, v1alpha1.MethodOIDC, passed(carol, "fed"), facts)
	if withoutMFA.Challenge.Type != methods.ChallengeTOTPEnroll {
		t.Fatalf("without an upstream mfa: %+v, want totp-enroll", withoutMFA)
	}
	unknown := afterProvider(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "nope", Facts{Providers: providers})
	if unknown.Challenge.Type != methods.ChallengeProviders || unknown.Challenge.Error == nil || unknown.Challenge.Error.Code != methods.FailureProviderError || unknown.Begin != "" {
		t.Fatalf("unknown provider %+v", unknown)
	}
	broken := AfterMethod(redirect.State, v1alpha1.MethodOIDC, failedWith(methods.FailureProviderError), Facts{Providers: providers})
	if broken.Challenge.Type != methods.ChallengeProviders || broken.Challenge.Error.Code != methods.FailureProviderError || len(broken.Challenge.Providers) != 1 {
		t.Fatalf("upstream failure %+v", broken)
	}
}

func TestDeviceLoginEndsWithConfirm(t *testing.T) {
	device := &v1alpha1.DeviceRequest{Spec: v1alpha1.DeviceRequestSpec{ClientID: "bedrock-cli", Scopes: []string{"openid", "offline_access"}}}
	step := afterPassword(t, Facts{User: localUser("alice"), Enrolled: []string{v1alpha1.MethodPassword}, Device: device})
	want := &methods.DeviceChallenge{ClientID: "bedrock-cli", Scopes: []string{"openid", "offline_access"}}
	if step.Challenge.Type != methods.ChallengeDeviceConfirm || step.Complete || !reflect.DeepEqual(step.Challenge.Device, want) {
		t.Fatalf("step %+v, want device-confirm", step)
	}
	approved := AfterDeviceApprove(step.State)
	if !approved.Complete || approved.Challenge.Type != methods.ChallengeDone {
		t.Fatalf("approve %+v", approved)
	}
	denied := AfterDeviceDeny(step.State)
	if denied.Complete || denied.Challenge.Type != methods.ChallengeDone {
		t.Fatalf("deny %+v", denied)
	}
}

func TestLoginStopsForADisabledUser(t *testing.T) {
	alice := localUser("alice")
	off := switchedOffUser(alice)
	password := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "alice", Facts{User: off})
	cases := map[string]Step{
		"subject disabled": AfterMethod(password.State, v1alpha1.MethodPassword, passed(off, "pwd"), Facts{User: off}),
		"method failure":   AfterMethod(password.State, v1alpha1.MethodPassword, failedWith(methods.FailureDisabled), Facts{}),
		"disabled later": AfterMethod(
			afterPassword(t, Facts{User: alice, Enrolled: []string{v1alpha1.MethodTOTP}}).State,
			v1alpha1.MethodTOTP, passed(alice, "otp"),
			Facts{User: off, Enrolled: []string{v1alpha1.MethodTOTP}},
		),
	}
	for name, step := range cases {
		if step.Complete || step.Challenge.Type != methods.ChallengeErrorType || step.Challenge.Error == nil || step.Challenge.Error.Code != methods.FailureDisabled {
			t.Fatalf("%s: %+v, want the disabled error", name, step)
		}
	}
}

func TestFailureKeepsTheStep(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Enrolled: []string{v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}}
	password := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "alice", facts)
	wrong := AfterMethod(password.State, v1alpha1.MethodPassword, failedWith(methods.FailureInvalidCredentials), facts)
	want := methods.Challenge{Type: methods.ChallengePassword, Username: "alice", Error: &methods.ChallengeError{Code: methods.FailureInvalidCredentials}}
	if !reflect.DeepEqual(wrong.Challenge, want) || wrong.State.Step != methods.ChallengePassword || wrong.State.Error != methods.FailureInvalidCredentials {
		t.Fatalf("wrong password %+v", wrong)
	}
	totp := AfterMethod(wrong.State, v1alpha1.MethodPassword, passed(alice, "pwd"), facts)
	if totp.State.Error != "" {
		t.Fatalf("a passed step must clear the error: %+v", totp.State)
	}
	limited := AfterMethod(totp.State, v1alpha1.MethodTOTP, failedWith(methods.FailureRateLimited), facts)
	if limited.Challenge.Type != methods.ChallengeTOTP || limited.Challenge.Error.Code != methods.FailureRateLimited || len(limited.Challenge.Methods) != 2 {
		t.Fatalf("rate limited %+v", limited)
	}
}

func TestResumeRepeatsTheChallenge(t *testing.T) {
	providers := []v1alpha1.IdentityProvider{oidcProvider("dex", "Dadehat SSO")}
	cases := []struct {
		name  string
		state v1alpha1.LoginState
		facts Facts
		want  string
		begin string
	}{
		{"empty", v1alpha1.LoginState{}, Facts{Providers: providers}, methods.ChallengeUsername, ""},
		{"password", v1alpha1.LoginState{Step: methods.ChallengePassword, Username: "alice"}, Facts{}, methods.ChallengePassword, ""},
		{"redirect", v1alpha1.LoginState{Step: methods.ChallengeRedirect, Primary: v1alpha1.MethodOIDC, Provider: "dex"}, Facts{}, methods.ChallengeRedirect, v1alpha1.MethodOIDC},
		{"recovery codes", v1alpha1.LoginState{Step: stepRecoveryCodes, Username: "alice"}, Facts{}, methods.ChallengeTOTPEnroll, ""},
		{"device without request", v1alpha1.LoginState{Step: methods.ChallengeDeviceConfirm}, Facts{}, methods.ChallengeErrorType, ""},
		{"error", v1alpha1.LoginState{Step: methods.ChallengeErrorType, Error: methods.FailureDisabled}, Facts{}, methods.ChallengeErrorType, ""},
	}
	for _, tc := range cases {
		step := resume(tc.state, tc.facts)
		if step.Challenge.Type != tc.want || step.Begin != tc.begin || step.Complete {
			t.Fatalf("%s: %+v, want %s", tc.name, step, tc.want)
		}
	}
	if got := resume(v1alpha1.LoginState{}, Facts{Providers: providers}).Challenge.Providers; len(got) != 1 {
		t.Fatalf("resumed username challenge lost the providers: %+v", got)
	}
}

func TestStepCannotBeSkipped(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}}
	password := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "alice", facts)
	skipToTOTP := AfterMethod(password.State, v1alpha1.MethodTOTP, passed(alice, "otp"), facts)
	if skipToTOTP.Complete || skipToTOTP.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("answering totp before password must be refused: %+v", skipToTOTP)
	}
	skipToRecovery := AfterMethod(password.State, v1alpha1.MethodRecovery, passed(alice, "otp"), facts)
	if skipToRecovery.Complete || skipToRecovery.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("answering recovery before password must be refused: %+v", skipToRecovery)
	}
}

func TestSecondFactorCannotBeBypassedByAnotherMethod(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Settings: policy.Settings{RequireSecondFactor: true}, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}}
	afterFirstFactor := afterPassword(t, facts)
	if afterFirstFactor.Challenge.Type != methods.ChallengeTOTP {
		t.Fatalf("setup: %+v, want totp required", afterFirstFactor)
	}
	replayPrimary := AfterMethod(afterFirstFactor.State, v1alpha1.MethodPassword, passed(alice, "pwd"), facts)
	if replayPrimary.Complete || replayPrimary.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("re-answering the primary must not satisfy the required second factor: %+v", replayPrimary)
	}

	ldapProviders := []v1alpha1.IdentityProvider{ldapProvider("corp")}
	oidcFacts := Facts{User: alice, Providers: ldapProviders, Settings: policy.Settings{RequireSecondFactor: true}}
	swapToOIDC := AfterMethod(afterFirstFactor.State, v1alpha1.MethodOIDC, passed(alice, "pwd"), oidcFacts)
	if swapToOIDC.Complete || swapToOIDC.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("swapping to an unrelated method must not satisfy the required second factor: %+v", swapToOIDC)
	}

	enrolledOnly := Facts{User: alice, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP}}
	afterFirstFactorEnrolled := afterPassword(t, enrolledOnly)
	if afterFirstFactorEnrolled.Challenge.Type != methods.ChallengeTOTP {
		t.Fatalf("setup: %+v, want totp required because it is enrolled", afterFirstFactorEnrolled)
	}
	swapEnrolled := AfterMethod(afterFirstFactorEnrolled.State, v1alpha1.MethodPassword, passed(alice, "pwd"), enrolledOnly)
	if swapEnrolled.Complete || swapEnrolled.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("an enrolled totp requirement must not be bypassable either: %+v", swapEnrolled)
	}
}

func TestLockedResultKeepsTheStep(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Enrolled: []string{v1alpha1.MethodPassword}}
	password := AfterUsername(v1alpha1.LoginState{Step: methods.ChallengeUsername}, "alice", facts)
	unknownUserLocked := AfterMethod(password.State, v1alpha1.MethodPassword, failedWith(methods.FailureLocked), Facts{})
	knownUserLocked := AfterMethod(password.State, v1alpha1.MethodPassword, failedWith(methods.FailureLocked), facts)
	want := methods.Challenge{Type: methods.ChallengePassword, Username: "alice", Error: &methods.ChallengeError{Code: methods.FailureLocked}}
	if !reflect.DeepEqual(unknownUserLocked.Challenge, want) {
		t.Fatalf("unknown user locked: %+v, want %+v", unknownUserLocked.Challenge, want)
	}
	if !reflect.DeepEqual(knownUserLocked.Challenge, want) {
		t.Fatalf("known user locked: %+v, want %+v", knownUserLocked.Challenge, want)
	}
	if unknownUserLocked.Complete || knownUserLocked.Complete {
		t.Fatalf("a locked result must not complete the login")
	}
}

func TestCompletedLoginCannotBeAdvancedAgain(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Enrolled: []string{v1alpha1.MethodPassword}}
	done := afterPassword(t, facts)
	if !done.Complete || done.State.Step != methods.ChallengeDone {
		t.Fatalf("setup: %+v, want a completed login", done)
	}
	replay := AfterMethod(done.State, v1alpha1.MethodPassword, passed(alice, "pwd"), facts)
	if replay.Complete || replay.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("a completed login must not be advanced again: %+v", replay)
	}
	viaTOTP := AfterMethod(done.State, v1alpha1.MethodTOTP, passed(alice, "otp"), facts)
	if viaTOTP.Complete || viaTOTP.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("a completed login must not accept a further factor: %+v", viaTOTP)
	}
}

func TestRecoveryCannotCompleteAFreshEnrollment(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Settings: policy.Settings{RequireSecondFactor: true}, Enrolled: []string{v1alpha1.MethodPassword, v1alpha1.MethodRecovery}}
	step := afterPassword(t, facts)
	if step.Challenge.Type != methods.ChallengeTOTPEnroll || step.State.Step != methods.ChallengeTOTPEnroll {
		t.Fatalf("setup: %+v, want totp-enroll for a user with no totp credential", step)
	}
	refused := AfterMethod(step.State, v1alpha1.MethodRecovery, passed(alice, "otp"), facts)
	if refused.Complete || refused.Challenge.Type != methods.ChallengeErrorType {
		t.Fatalf("a recovery code must not complete a fresh totp enrollment: %+v", refused)
	}
	stillEnrolling := AfterMethod(step.State, v1alpha1.MethodTOTP, failedWith(methods.FailureInvalidCode), facts)
	if stillEnrolling.Challenge.Type != methods.ChallengeTOTPEnroll || stillEnrolling.State.Step != methods.ChallengeTOTPEnroll {
		t.Fatalf("only a totp answer belongs on the enroll step: %+v", stillEnrolling)
	}
}

func TestErroredLoginCannotAdvance(t *testing.T) {
	alice := localUser("alice")
	facts := Facts{User: alice, Enrolled: []string{v1alpha1.MethodPassword}}
	cases := map[string]v1alpha1.LoginState{
		"disabled error": failed(v1alpha1.LoginState{Step: methods.ChallengePassword, Username: "alice"}, methods.FailureDisabled).State,
		"expired error":  failed(v1alpha1.LoginState{Step: methods.ChallengeDeviceConfirm}, errorExpired).State,
	}
	for name, errored := range cases {
		if errored.Step != methods.ChallengeErrorType {
			t.Fatalf("%s: setup %+v, want the error step", name, errored)
		}
		step := AfterMethod(errored, v1alpha1.MethodPassword, passed(alice, "pwd"), facts)
		if step.Complete || step.Challenge.Type != methods.ChallengeErrorType {
			t.Fatalf("%s: an errored login must not advance: %+v", name, step)
		}
	}
}
