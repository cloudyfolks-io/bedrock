package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/credential"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
)

const UserKind = "DatabaseUser"

func statusEqual(current, desired client.Object) bool {
	switch typed := current.(type) {
	case *v1alpha1.DatabaseUser:
		return equality.Semantic.DeepEqual(typed.Status, desired.(*v1alpha1.DatabaseUser).Status)
	case *v1alpha1.DatabaseSchema:
		return equality.Semantic.DeepEqual(typed.Status, desired.(*v1alpha1.DatabaseSchema).Status)
	}
	return false
}

type UserReconciler struct {
	Client client.Client
	Engine EngineFactory
}

func UsernameOf(user v1alpha1.DatabaseUser) string {
	if user.Spec.Username != "" {
		return user.Spec.Username
	}
	return user.Name
}

func UserSecretName(user v1alpha1.DatabaseUser) string { return user.Name + "-db" }

func engineGrants(grants []v1alpha1.DatabaseGrant) []postgres.Grant {
	out := make([]postgres.Grant, len(grants))
	for i, grant := range grants {
		out[i] = postgres.Grant{Database: grant.Database, Level: grant.Level}
	}
	return out
}

func GrantsHash(username string, grants []v1alpha1.DatabaseGrant) string {
	entries := make([]string, len(grants))
	for i, grant := range grants {
		entries[i] = grant.Database + ":" + grant.Level
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(username + "|" + strings.Join(entries, ",")))
	return hex.EncodeToString(sum[:])[:16]
}

func validateUser(user v1alpha1.DatabaseUser) string {
	username := UsernameOf(user)
	if !postgres.ValidIdentifier(username) {
		return "the user name " + username + " must match ^[a-z][a-z0-9_]{1,62}$; set spec.username"
	}
	if err := postgres.ReservedRole(username); err != nil {
		return err.Error()
	}
	return ""
}

func (r *UserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var user v1alpha1.DatabaseUser
	if err := r.Client.Get(ctx, req.NamespacedName, &user); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !user.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, user)
	}
	outcome, err := r.converge(ctx, &user)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, user, outcome); err != nil {
		return ctrl.Result{}, err
	}
	return result(outcome), nil
}

func (r *UserReconciler) writeStatus(ctx context.Context, user v1alpha1.DatabaseUser, outcome accessOutcome) error {
	return updateStatus(ctx, r.Client, client.ObjectKeyFromObject(&user), func() *v1alpha1.DatabaseUser { return &v1alpha1.DatabaseUser{} }, func(current *v1alpha1.DatabaseUser) {
		v1alpha1.SetCondition(&current.Status.Conditions, readyCondition(outcome, current.Generation))
		current.Status.ObservedGeneration = current.Generation
		current.Status.Username = UsernameOf(*current)
		if outcome.ready() {
			current.Status.CredentialsSecret = UserSecretName(*current)
		}
	})
}

func (r *UserReconciler) converge(ctx context.Context, user *v1alpha1.DatabaseUser) (accessOutcome, error) {
	if problem := validateUser(*user); problem != "" {
		return accessOutcome{reason: ReasonInvalidSpec, message: problem, requeue: accessBackoff}, nil
	}
	db, blocked, err := resolveDatabase(ctx, r.Client, user.Namespace, user.Spec.DatabaseRef)
	if err != nil || blocked != nil {
		return orBlocked(blocked), err
	}
	endpoint, found := endpointOf(db)
	if !found {
		return accessOutcome{reason: ReasonEndpointPending, message: "the database has no address yet", requeue: accessPending}, nil
	}
	if missing, err := r.missingGrantDatabase(ctx, *user); err != nil || missing != "" {
		if err != nil {
			return accessOutcome{}, err
		}
		return accessOutcome{reason: ReasonGrantDatabaseNotFound, message: "the database " + missing + " is neither app nor a ready DatabaseSchema of " + db.Name, requeue: accessPending}, nil
	}
	if !controllerutil.ContainsFinalizer(user, finalizerUser) {
		if err := patchUserFinalizer(ctx, r.Client, user, controllerutil.AddFinalizer); err != nil {
			return accessOutcome{}, err
		}
	}
	engineAccess, err := r.Engine(ctx, db)
	if err != nil {
		return accessOutcome{}, err
	}
	state, err := ensureUserSecret(ctx, r.Client, *user, project(db), endpoint)
	if err != nil {
		return accessOutcome{}, err
	}
	outcome, err := r.syncEngine(ctx, engineAccess, user, state)
	if err != nil {
		if failure := engineFailure(err); failure != nil {
			return *failure, nil
		}
		return accessOutcome{}, err
	}
	return outcome, nil
}

func orBlocked(blocked *accessOutcome) accessOutcome {
	if blocked == nil {
		return accessOutcome{}
	}
	return *blocked
}

func project(db v1alpha1.Database) Project {
	return Project{Namespace: db.Namespace, Tenant: db.Labels[v1alpha1.LabelTenant], Name: db.Labels[v1alpha1.LabelProject]}
}

type Endpoint struct {
	Host string
	Port int32
}

func endpointOf(db v1alpha1.Database) (Endpoint, bool) {
	if len(db.Status.Addresses) == 0 || db.Status.Addresses[0].IP == "" {
		return Endpoint{}, false
	}
	return Endpoint{Host: db.Status.Addresses[0].IP, Port: db.Status.Addresses[0].Port}, true
}

func (r *UserReconciler) missingGrantDatabase(ctx context.Context, user v1alpha1.DatabaseUser) (string, error) {
	var schemas v1alpha1.DatabaseSchemaList
	if err := r.Client.List(ctx, &schemas, client.InNamespace(user.Namespace)); err != nil {
		return "", err
	}
	ready := map[string]bool{"app": true}
	for _, schema := range schemas.Items {
		if schema.Spec.DatabaseRef == user.Spec.DatabaseRef && schema.DeletionTimestamp.IsZero() && schema.Status.Name != "" && conditionTrue(schema.Status.Conditions) {
			ready[schema.Status.Name] = true
		}
	}
	for _, grant := range user.Spec.Grants {
		if !ready[grant.Database] {
			return grant.Database, nil
		}
	}
	return "", nil
}

func conditionTrue(conditions []metav1.Condition) bool {
	return v1alpha1.IsConditionTrue(conditions, v1alpha1.ConditionReady)
}

type userSecretState struct {
	Password string
	Synced   bool
}

func userURI(username, password string, endpoint Endpoint, database string) string {
	userinfo := url.UserPassword(username, password)
	return (&url.URL{Scheme: "postgresql", User: userinfo, Host: net.JoinHostPort(endpoint.Host, strconv.Itoa(int(endpoint.Port))), Path: "/" + database}).String()
}

func connectionDatabase(user v1alpha1.DatabaseUser) string {
	if len(user.Spec.Grants) == 0 {
		return engine.DefaultDatabase
	}
	names := make([]string, len(user.Spec.Grants))
	for i, grant := range user.Spec.Grants {
		names[i] = grant.Database
	}
	slices.Sort(names)
	return names[0]
}

func userSecretData(user v1alpha1.DatabaseUser, password string, endpoint Endpoint) map[string][]byte {
	username := UsernameOf(user)
	return map[string][]byte{
		"username": []byte(username),
		"password": []byte(password),
		"host":     []byte(endpoint.Host),
		"port":     []byte(strconv.Itoa(int(endpoint.Port))),
		"uri":      []byte(userURI(username, password, endpoint, connectionDatabase(user))),
	}
}

func ensureUserSecret(ctx context.Context, c client.Client, user v1alpha1.DatabaseUser, project Project, endpoint Endpoint) (userSecretState, error) {
	key := client.ObjectKey{Namespace: user.Namespace, Name: UserSecretName(user)}
	token := user.Annotations[v1alpha1.AnnotationResetPassword]
	var state userSecretState
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var secret corev1.Secret
		err := c.Get(ctx, key, &secret)
		if apierrors.IsNotFound(err) {
			password, err := credential.Password()
			if err != nil {
				return err
			}
			created := userSecret(user, project, userSecretData(user, password, endpoint), map[string]string{annotationPasswordSynced: unsynced, annotationResetApplied: token})
			state = userSecretState{Password: password}
			return c.Create(ctx, created, client.FieldOwner(v1alpha1.DatabaseFieldManager))
		}
		if err != nil {
			return err
		}
		before := secret.DeepCopy()
		password := string(secret.Data["password"])
		if token != "" && token != secret.Annotations[annotationResetApplied] {
			if password, err = credential.Password(); err != nil {
				return err
			}
			secret.Annotations[annotationPasswordSynced] = unsynced
			secret.Annotations[annotationResetApplied] = token
		}
		secret.Data = userSecretData(user, password, endpoint)
		state = userSecretState{Password: password, Synced: secret.Annotations[annotationPasswordSynced] == synced}
		if equality.Semantic.DeepEqual(before.Data, secret.Data) && equality.Semantic.DeepEqual(before.Annotations, secret.Annotations) {
			return nil
		}
		return c.Patch(ctx, &secret, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
	return state, err
}

func userSecret(user v1alpha1.DatabaseUser, project Project, data map[string][]byte, annotations map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: UserSecretName(user), Namespace: user.Namespace,
			Labels:          v1alpha1.TenantLabels(UserKind, user.Name, project.Tenant, project.Name),
			Annotations:     annotations,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&user, v1alpha1.GroupVersion.WithKind(UserKind))},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func markUserPasswordSynced(ctx context.Context, c client.Client, user v1alpha1.DatabaseUser) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var secret corev1.Secret
		if err := c.Get(ctx, client.ObjectKey{Namespace: user.Namespace, Name: UserSecretName(user)}, &secret); err != nil {
			return err
		}
		before := secret.DeepCopy()
		secret.Annotations[annotationPasswordSynced] = synced
		return c.Patch(ctx, &secret, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func (r *UserReconciler) syncEngine(ctx context.Context, engineAccess Engine, user *v1alpha1.DatabaseUser, state userSecretState) (accessOutcome, error) {
	username := UsernameOf(*user)
	desired := GrantsHash(username, user.Spec.Grants)
	grants := engineGrants(user.Spec.Grants)
	timeout := engineAccess.Timeout
	if user.Status.AppliedGrants == "" {
		if err := r.createRole(ctx, engineAccess, username, state.Password, grants, timeout); err != nil {
			return accessOutcome{}, err
		}
		if err := markUserPasswordSynced(ctx, r.Client, *user); err != nil {
			return accessOutcome{}, err
		}
		return readyOutcome(), r.recordGrants(ctx, user, desired)
	}
	if !state.Synced {
		if err := resetUserPassword(ctx, engineAccess, username, state.Password, timeout); err != nil {
			return accessOutcome{}, err
		}
		if err := markUserPasswordSynced(ctx, r.Client, *user); err != nil {
			return accessOutcome{}, err
		}
	}
	if user.Status.AppliedGrants != desired {
		if err := setGrants(ctx, engineAccess, username, grants, timeout); err != nil {
			return accessOutcome{}, err
		}
		return readyOutcome(), r.recordGrants(ctx, user, desired)
	}
	return readyOutcome(), nil
}

func (r *UserReconciler) recordGrants(ctx context.Context, user *v1alpha1.DatabaseUser, hash string) error {
	return updateStatus(ctx, r.Client, client.ObjectKeyFromObject(user), func() *v1alpha1.DatabaseUser { return &v1alpha1.DatabaseUser{} }, func(current *v1alpha1.DatabaseUser) {
		current.Status.AppliedGrants = hash
		user.Status.AppliedGrants = hash
	})
}

func (r *UserReconciler) createRole(ctx context.Context, engineAccess Engine, username, password string, grants []postgres.Grant, timeout time.Duration) error {
	verifier, err := credential.ScramSHA256(password)
	if err != nil {
		return err
	}
	script, err := postgres.CreateUserScript(username, verifier, grants, timeout)
	if err != nil {
		return err
	}
	_, err = engineAccess.Run(ctx, script)
	if !exec.IsKind(err, exec.KindExists) {
		return err
	}
	if err := resetUserPassword(ctx, engineAccess, username, password, timeout); err != nil {
		return err
	}
	return setGrants(ctx, engineAccess, username, grants, timeout)
}

func resetUserPassword(ctx context.Context, engineAccess Engine, username, password string, timeout time.Duration) error {
	verifier, err := credential.ScramSHA256(password)
	if err != nil {
		return err
	}
	script, err := postgres.ResetPasswordScript(username, verifier, timeout)
	if err != nil {
		return err
	}
	_, err = engineAccess.Run(ctx, script)
	return err
}

func setGrants(ctx context.Context, engineAccess Engine, username string, grants []postgres.Grant, timeout time.Duration) error {
	databases, err := listDatabases(ctx, engineAccess)
	if err != nil {
		return err
	}
	script, err := postgres.SetGrantsScript(username, databases, grants, timeout)
	if err != nil {
		return err
	}
	_, err = engineAccess.Run(ctx, script)
	return err
}

func patchUserFinalizer(ctx context.Context, c client.Client, user *v1alpha1.DatabaseUser, change func(client.Object, string) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(user), user); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := user.DeepCopy()
		if !change(user, finalizerUser) {
			return nil
		}
		return c.Patch(ctx, user, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func (r *UserReconciler) finalize(ctx context.Context, user v1alpha1.DatabaseUser) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(&user, finalizerUser) {
		return ctrl.Result{}, nil
	}
	release := func() (ctrl.Result, error) {
		return ctrl.Result{}, patchUserFinalizer(ctx, r.Client, &user, controllerutil.RemoveFinalizer)
	}
	if user.Status.AppliedGrants == "" {
		return release()
	}
	var db v1alpha1.Database
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: user.Namespace, Name: user.Spec.DatabaseRef}, &db)
	if apierrors.IsNotFound(err) || err == nil && !db.DeletionTimestamp.IsZero() {
		return release()
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	_, blocked, err := resolveDatabase(ctx, r.Client, user.Namespace, user.Spec.DatabaseRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	if blocked != nil {
		return result(*blocked), r.writeStatus(ctx, user, *blocked)
	}
	outcome, err := r.dropRole(ctx, db, user)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !outcome.ready() {
		return result(outcome), r.writeStatus(ctx, user, outcome)
	}
	return release()
}

func (r *UserReconciler) dropRole(ctx context.Context, db v1alpha1.Database, user v1alpha1.DatabaseUser) (accessOutcome, error) {
	engineAccess, err := r.Engine(ctx, db)
	if err != nil {
		return accessOutcome{}, err
	}
	databases, err := listDatabases(ctx, engineAccess)
	if err == nil {
		var script string
		if script, err = postgres.DeleteUserScript(UsernameOf(user), databases, engineAccess.Timeout); err == nil {
			_, err = engineAccess.Run(ctx, script)
		}
	}
	switch {
	case err == nil, exec.IsKind(err, exec.KindNotFound):
		return readyOutcome(), nil
	case exec.IsKind(err, exec.KindOwnsObjects):
		return accessOutcome{reason: ReasonUserOwnsObjects, message: "the user still owns objects that could not be moved", requeue: accessBackoff}, nil
	}
	if failure := engineFailure(err); failure != nil {
		return *failure, nil
	}
	return accessOutcome{}, err
}
