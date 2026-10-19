package database

import (
	"context"
	"net"
	"net/url"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/credential"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	SecretSuperuser = "superuser"
	SecretApp       = "app"
	SuffixAdmin     = "admin"

	annotationPasswordSynced = "bedrock.cloudyfolks.io/password-synced"
	annotationResetApplied   = "bedrock.cloudyfolks.io/reset-applied"
	postgresSuperuser        = "postgres"
	passwordKey              = "password"
	synced                   = "true"
	unsynced                 = "false"
)

func restored(db v1alpha1.Database) bool { return db.Spec.Restore != nil }

func EngineSecret(name string, db v1alpha1.Database, project Project, data map[string]string, extra map[string]string) *corev1.Secret {
	labels, annotations := OwnedMetadata(db, project)
	for key, value := range extra {
		annotations[key] = value
	}
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: PlatformNamespace, Labels: labels, Annotations: annotations},
		Type:       corev1.SecretTypeBasicAuth,
		StringData: data,
	}
}

func EnsureEngineSecrets(ctx context.Context, c client.Client, db v1alpha1.Database, project Project) error {
	if err := createIfAbsent(ctx, c, Suffixed(db.UID, SecretSuperuser), func(password string) *corev1.Secret {
		return EngineSecret(Suffixed(db.UID, SecretSuperuser), db, project, map[string]string{"username": postgresSuperuser, passwordKey: password}, nil)
	}); err != nil {
		return err
	}
	state := synced
	if restored(db) {
		state = unsynced
	}
	return createIfAbsent(ctx, c, Suffixed(db.UID, SecretApp), func(password string) *corev1.Secret {
		return EngineSecret(Suffixed(db.UID, SecretApp), db, project, map[string]string{"username": engine.AdminUser, passwordKey: password}, map[string]string{
			annotationPasswordSynced: state,
			annotationResetApplied:   db.Annotations[v1alpha1.AnnotationResetPassword],
		})
	})
}

func createIfAbsent(ctx context.Context, c client.Client, name string, build func(password string) *corev1.Secret) error {
	var existing corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: name}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	password, err := credential.Password()
	if err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(c.Create(ctx, build(password), client.FieldOwner(v1alpha1.DatabaseFieldManager)))
}

type AdminState struct {
	Password string
	Synced   bool
	Applied  string
}

func ReadAdmin(ctx context.Context, c client.Reader, db v1alpha1.Database) (AdminState, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: Suffixed(db.UID, SecretApp)}, &secret); err != nil {
		return AdminState{}, err
	}
	return AdminState{
		Password: string(secret.Data[passwordKey]),
		Synced:   secret.Annotations[annotationPasswordSynced] == synced,
		Applied:  secret.Annotations[annotationResetApplied],
	}, nil
}

func patchAdminSecret(ctx context.Context, c client.Client, db v1alpha1.Database, change func(*corev1.Secret)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var secret corev1.Secret
		if err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: Suffixed(db.UID, SecretApp)}, &secret); err != nil {
			return err
		}
		before := secret.DeepCopy()
		change(&secret)
		return c.Patch(ctx, &secret, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func MarkAdminSynced(ctx context.Context, c client.Client, db v1alpha1.Database) error {
	return patchAdminSecret(ctx, c, db, func(secret *corev1.Secret) {
		secret.Annotations[annotationPasswordSynced] = synced
	})
}

func ResetAdminPassword(ctx context.Context, c client.Client, db v1alpha1.Database, token string) (string, error) {
	password, err := credential.Password()
	if err != nil {
		return "", err
	}
	err = patchAdminSecret(ctx, c, db, func(secret *corev1.Secret) {
		secret.Data = map[string][]byte{"username": []byte(engine.AdminUser), passwordKey: []byte(password)}
		secret.Annotations[annotationPasswordSynced] = unsynced
		secret.Annotations[annotationResetApplied] = token
	})
	return password, err
}

func AdminURI(password, host string, port int32) string {
	userinfo := url.UserPassword(engine.AdminUser, password)
	return (&url.URL{Scheme: "postgresql", User: userinfo, Host: net.JoinHostPort(host, strconv.Itoa(int(port))), Path: "/" + engine.DefaultDatabase}).String()
}

func AdminCopyName(db v1alpha1.Database) string { return db.Name + "-" + SuffixAdmin }

func AdminCopy(db v1alpha1.Database, project Project, password, host string, port int32) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name: AdminCopyName(db), Namespace: db.Namespace,
			Labels:          v1alpha1.TenantLabels(Kind, db.Name, project.Tenant, project.Name),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&db, v1alpha1.GroupVersion.WithKind(Kind))},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"username":  engine.AdminUser,
			passwordKey: password,
			"host":      host,
			"port":      strconv.Itoa(int(port)),
			"database":  engine.DefaultDatabase,
			"uri":       AdminURI(password, host, port),
		},
	}
}

func EnsureAdminCopy(ctx context.Context, c client.Client, db v1alpha1.Database, project Project, host string, port int32) error {
	state, err := ReadAdmin(ctx, c, db)
	if err != nil {
		return err
	}
	return ssa.Apply(ctx, c, AdminCopy(db, project, state.Password, host, port), v1alpha1.DatabaseFieldManager)
}
