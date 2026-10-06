package login

import (
	"slices"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
)

const (
	stepRecoveryCodes      = "recovery-codes"
	answerProvider         = "providers"
	errorExpired           = "expired"
	maxSecondFactorGuesses = 5
)

type Facts struct {
	User          *v1alpha1.User
	Groups        []v1alpha1.Group
	Client        v1alpha1.OAuthClient
	Settings      policy.Settings
	Enrolled      []string
	Providers     []v1alpha1.IdentityProvider
	LDAPProviders []string
	Device        *v1alpha1.DeviceRequest
}

type Step struct {
	State     v1alpha1.LoginState
	Challenge methods.Challenge
	Begin     string
	Complete  bool
}

func Start(facts Facts) Step {
	return Step{
		State:     v1alpha1.LoginState{Step: methods.ChallengeUsername},
		Challenge: methods.Challenge{Type: methods.ChallengeUsername, Providers: providerChoices(facts.Providers)},
	}
}

func usernameLimited(facts Facts) Step {
	step := Start(facts)
	step.State.Error = methods.FailureRateLimited
	step.Challenge.Error = challengeError(methods.FailureRateLimited)
	return step
}

func AfterUsername(_ v1alpha1.LoginState, username string, facts Facts) Step {
	primary, provider := primaryFor(facts)
	return Step{
		State:     v1alpha1.LoginState{Step: methods.ChallengePassword, Username: username, Primary: primary, Provider: provider},
		Challenge: methods.Challenge{Type: methods.ChallengePassword, Username: username},
	}
}

func AfterMethod(state v1alpha1.LoginState, method string, result methods.Result, facts Facts) Step {
	switch {
	case result.Failure == methods.FailureDisabled:
		return failed(state, methods.FailureDisabled)
	case result.Failure != "" && method == v1alpha1.MethodOIDC:
		return providersStep(facts, result.Failure)
	case result.Failure == methods.FailureInvalidCode && isGuessStep(state.Step):
		return guessed(state, facts)
	case result.Failure != "":
		return retry(state, result.Failure, facts)
	case result.Challenge != nil:
		return Step{State: state, Challenge: *result.Challenge}
	case result.Subject == nil:
		return failed(state, methods.FailureInvalidCredentials)
	case result.Subject.User.Spec.Disabled:
		return failed(state, methods.FailureDisabled)
	case len(state.Completed) == 0 && method == state.Primary:
		return next(primaryDone(state, method, Required(facts, method, result.Subject.AMR)), facts)
	case len(state.Required) == 0 || !isSecondFactor(method):
		return failed(state, methods.FailureInvalidCredentials)
	case state.Step == methods.ChallengeTOTPEnroll && method != v1alpha1.MethodTOTP:
		return failed(state, methods.FailureInvalidCredentials)
	case state.Step == methods.ChallengeTOTPEnroll:
		return recoveryCodesStep(factorDone(state, method))
	}
	return next(factorDone(state, method), facts)
}

func AfterDeviceApprove(state v1alpha1.LoginState) Step {
	return Step{State: at(state, methods.ChallengeDone), Challenge: methods.Challenge{Type: methods.ChallengeDone}, Complete: true}
}

func AfterDeviceDeny(state v1alpha1.LoginState) Step {
	return Step{State: at(state, methods.ChallengeDone), Challenge: methods.Challenge{Type: methods.ChallengeDone}}
}

func Required(facts Facts, primary string, amr []string) []string {
	switch {
	case facts.User == nil:
		return nil
	case primary == v1alpha1.MethodOIDC && policy.SecondFactorSatisfied(amr):
		return nil
	case slices.Contains(facts.Enrolled, v1alpha1.MethodTOTP), policy.SecondFactorRequired(facts.Settings, *facts.User, facts.Groups, facts.Client):
		return []string{v1alpha1.MethodTOTP}
	}
	return nil
}

func afterProvider(_ v1alpha1.LoginState, name string, facts Facts) Step {
	if !slices.ContainsFunc(facts.Providers, enabledProvider(name, v1alpha1.MethodOIDC)) {
		return providersStep(facts, methods.FailureProviderError)
	}
	return Step{
		State:     v1alpha1.LoginState{Step: methods.ChallengeRedirect, Primary: v1alpha1.MethodOIDC, Provider: name},
		Challenge: methods.Challenge{Type: methods.ChallengeRedirect},
		Begin:     v1alpha1.MethodOIDC,
	}
}

func toRecovery(state v1alpha1.LoginState) Step {
	return Step{
		State:     at(state, methods.ChallengeRecovery),
		Challenge: methods.Challenge{Type: methods.ChallengeRecovery, Username: state.Username},
	}
}

func afterRecoveryCodes(state v1alpha1.LoginState, facts Facts) Step {
	return next(state, facts)
}

func resume(state v1alpha1.LoginState, facts Facts) Step {
	switch state.Step {
	case "", methods.ChallengeUsername:
		return Start(facts)
	case methods.ChallengeProviders:
		return providersStep(facts, state.Error)
	case methods.ChallengeRedirect:
		return Step{State: state, Challenge: methods.Challenge{Type: methods.ChallengeRedirect}, Begin: state.Primary}
	case methods.ChallengeDeviceConfirm:
		return resumeDevice(state, facts.Device)
	case methods.ChallengeErrorType:
		return failed(state, state.Error)
	case methods.ChallengeDone:
		return Step{State: state, Challenge: methods.Challenge{Type: methods.ChallengeDone}}
	}
	return ask(state, facts)
}

func resumeDevice(state v1alpha1.LoginState, device *v1alpha1.DeviceRequest) Step {
	if device == nil {
		return failed(state, errorExpired)
	}
	return confirmDevice(state, *device)
}

func failed(state v1alpha1.LoginState, code string) Step {
	return Step{
		State:     v1alpha1.LoginState{Step: methods.ChallengeErrorType, Username: state.Username, Error: code},
		Challenge: methods.Challenge{Type: methods.ChallengeErrorType, Error: challengeError(code)},
	}
}

func retry(state v1alpha1.LoginState, code string, facts Facts) Step {
	current := at(state, state.Step)
	current.Error = code
	challenge := challengeFor(current, facts)
	challenge.Error = challengeError(code)
	return Step{State: current, Challenge: challenge}
}

func guessed(state v1alpha1.LoginState, facts Facts) Step {
	counted := at(state, state.Step)
	counted.Failures = state.Failures + 1
	if counted.Failures >= maxSecondFactorGuesses {
		return failed(counted, errorExpired)
	}
	return retry(counted, methods.FailureInvalidCode, facts)
}

func next(state v1alpha1.LoginState, facts Facts) Step {
	switch {
	case facts.User != nil && facts.User.Spec.Disabled:
		return failed(state, methods.FailureDisabled)
	case len(state.Required) > 0 && slices.Contains(facts.Enrolled, state.Required[0]):
		return ask(at(state, methods.ChallengeTOTP), facts)
	case len(state.Required) > 0:
		return ask(at(state, methods.ChallengeTOTPEnroll), facts)
	case facts.Device != nil:
		return confirmDevice(at(state, methods.ChallengeDeviceConfirm), *facts.Device)
	}
	return Step{State: at(state, methods.ChallengeDone), Challenge: methods.Challenge{Type: methods.ChallengeDone}, Complete: true}
}

func ask(state v1alpha1.LoginState, facts Facts) Step {
	return Step{State: state, Challenge: challengeFor(state, facts)}
}

func challengeFor(state v1alpha1.LoginState, facts Facts) methods.Challenge {
	switch state.Step {
	case methods.ChallengeTOTP:
		return methods.Challenge{Type: methods.ChallengeTOTP, Username: state.Username, Methods: secondFactors(facts.Enrolled)}
	case stepRecoveryCodes:
		return methods.Challenge{Type: methods.ChallengeTOTPEnroll, Username: state.Username}
	}
	return methods.Challenge{Type: state.Step, Username: state.Username}
}

func recoveryCodesStep(state v1alpha1.LoginState) Step {
	return Step{
		State:     at(state, stepRecoveryCodes),
		Challenge: methods.Challenge{Type: methods.ChallengeTOTPEnroll, Username: state.Username},
	}
}

func confirmDevice(state v1alpha1.LoginState, device v1alpha1.DeviceRequest) Step {
	return Step{
		State: state,
		Challenge: methods.Challenge{
			Type:   methods.ChallengeDeviceConfirm,
			Device: &methods.DeviceChallenge{ClientID: device.Spec.ClientID, Scopes: slices.Clone(device.Spec.Scopes)},
		},
	}
}

func providersStep(facts Facts, code string) Step {
	return Step{
		State:     v1alpha1.LoginState{Step: methods.ChallengeProviders, Error: code},
		Challenge: methods.Challenge{Type: methods.ChallengeProviders, Providers: providerChoices(facts.Providers), Error: challengeError(code)},
	}
}

func primaryDone(state v1alpha1.LoginState, method string, required []string) v1alpha1.LoginState {
	return v1alpha1.LoginState{
		Step:      state.Step,
		Username:  state.Username,
		Provider:  state.Provider,
		Primary:   state.Primary,
		Completed: []string{method},
		Required:  required,
		CSRFHash:  state.CSRFHash,
		Failures:  state.Failures,
	}
}

func factorDone(state v1alpha1.LoginState, method string) v1alpha1.LoginState {
	done := at(state, state.Step)
	done.Completed = append(slices.Clone(state.Completed), method)
	done.Required = slices.Clone(state.Required[1:])
	return done
}

func at(state v1alpha1.LoginState, step string) v1alpha1.LoginState {
	return v1alpha1.LoginState{
		Step:      step,
		Username:  state.Username,
		Provider:  state.Provider,
		Primary:   state.Primary,
		Completed: slices.Clone(state.Completed),
		Required:  slices.Clone(state.Required),
		CSRFHash:  state.CSRFHash,
		Failures:  state.Failures,
	}
}

func primaryFor(facts Facts) (string, string) {
	switch {
	case facts.User != nil && slices.ContainsFunc(facts.Providers, enabledProvider(facts.User.Spec.Source, v1alpha1.MethodLDAP)):
		return v1alpha1.MethodLDAP, facts.User.Spec.Source
	case facts.User == nil && len(facts.LDAPProviders) > 0:
		return v1alpha1.MethodLDAP, facts.LDAPProviders[0]
	}
	return v1alpha1.MethodPassword, ""
}

func enabledProvider(name, kind string) func(v1alpha1.IdentityProvider) bool {
	return func(provider v1alpha1.IdentityProvider) bool {
		return provider.Name == name && provider.Spec.Type == kind && !provider.Spec.Disabled
	}
}

func providerChoices(providers []v1alpha1.IdentityProvider) []methods.ProviderChoice {
	var choices []methods.ProviderChoice
	for _, provider := range providers {
		if enabledProvider(provider.Name, v1alpha1.MethodOIDC)(provider) {
			choices = append(choices, methods.ProviderChoice{Name: provider.Name, DisplayName: displayName(provider), Type: provider.Spec.Type})
		}
	}
	return choices
}

func displayName(provider v1alpha1.IdentityProvider) string {
	if provider.Spec.DisplayName != "" {
		return provider.Spec.DisplayName
	}
	return provider.Name
}

func secondFactors(enrolled []string) []string {
	if slices.Contains(enrolled, v1alpha1.MethodRecovery) {
		return []string{v1alpha1.MethodTOTP, v1alpha1.MethodRecovery}
	}
	return []string{v1alpha1.MethodTOTP}
}

func isGuessStep(step string) bool {
	return step == methods.ChallengeTOTP || step == methods.ChallengeRecovery
}

func isSecondFactor(method string) bool {
	return method == v1alpha1.MethodTOTP || method == v1alpha1.MethodRecovery
}

func challengeError(code string) *methods.ChallengeError {
	if code == "" {
		return nil
	}
	return &methods.ChallengeError{Code: code}
}
