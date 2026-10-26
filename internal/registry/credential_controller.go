package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
	"github.com/cloudyfolks-io/bedrock/internal/registry/harbor"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	reasonRegistryMissing  = "RegistryNotFound"
	reasonRegistryNotReady = "RegistryNotReady"
	reasonRobotClaimed     = "RobotClaimed"
	reasonSecretNameTaken  = "SecretNameTaken"
	credentialRequeue      = 10 * time.Minute
	robotSecretLength      = 32
	dockerConfigKey        = ".dockerconfigjson"
	conditionSecretKept    = "SecretKept"
	reasonPasswordKept     = "PasswordKept"
)

type CredentialReconciler struct {
	Client  client.Client
	BaseURL string
	HTTP    *http.Client
	Random  io.Reader
}

func (r *CredentialReconciler) connector() Connector {
	return Connector{Client: r.Client, BaseURL: r.BaseURL, HTTP: r.HTTP}
}

func (r *CredentialReconciler) random() io.Reader {
	if r.Random != nil {
		return r.Random
	}
	return rand.Reader
}

func robotNameOf(credential *v1alpha1.RegistryCredential) string {
	if credential.Spec.RobotName != "" {
		return credential.Spec.RobotName
	}
	return credential.Name
}

func SecretName(credential *v1alpha1.RegistryCredential) string {
	return credential.Name + "-docker"
}

type robotFacts struct {
	robotID     int64
	robotName   string
	username    string
	secretReset string
	secretName  string
	secretKept  bool
}

func (r *CredentialReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var credential v1alpha1.RegistryCredential
	if err := r.Client.Get(ctx, req.NamespacedName, &credential); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !credential.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &credential)
	}
	if err := r.updateFinalizers(ctx, &credential, controllerutil.AddFinalizer); err != nil {
		return ctrl.Result{}, err
	}
	got, out, err := r.reconcileRobot(ctx, &credential)
	if writeErr := r.writeStatus(ctx, &credential, got, out); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: out.requeue}, nil
}

func (r *CredentialReconciler) reconcileRobot(ctx context.Context, credential *v1alpha1.RegistryCredential) (robotFacts, result, error) {
	none := statusFacts(credential)
	var registry v1alpha1.Registry
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: credential.Namespace, Name: credential.Spec.RegistryRef.Name}, &registry)
	if apierrors.IsNotFound(err) {
		return none, result{reason: reasonRegistryMissing, message: "the Registry " + credential.Spec.RegistryRef.Name + " does not exist", requeue: unavailableRequeue}, nil
	}
	if err != nil {
		return none, result{}, err
	}
	project := registry.Status.ProjectName
	if project == "" || !meta.IsStatusConditionTrue(registry.Status.Conditions, conditionReady) {
		return none, result{reason: reasonRegistryNotReady, message: "the Registry " + registry.Name + " is not ready", requeue: unavailableRequeue}, nil
	}
	harborClient, unavailable, err := r.connector().Harbor(ctx)
	if err != nil {
		return none, result{}, err
	}
	if unavailable != "" {
		return none, result{reason: reasonNotAvailable, message: unavailable, requeue: unavailableRequeue}, nil
	}
	got, out, err := r.ensureRobot(ctx, credential, &registry, harborClient, project)
	return got, harborOutcome(out, err), harborError(err)
}

func (r *CredentialReconciler) ensureRobot(ctx context.Context, credential *v1alpha1.RegistryCredential, registry *v1alpha1.Registry, hc *harbor.Client, project string) (robotFacts, result, error) {
	got := robotFacts{robotID: credential.Status.RobotID, robotName: credential.Status.RobotName, username: credential.Status.Username, secretReset: credential.Status.SecretReset, secretName: credential.Status.SecretName, secretKept: meta.IsStatusConditionTrue(credential.Status.Conditions, conditionSecretKept)}
	owned := credential.Status.RobotName == robotNameOf(credential)
	robot, found, err := hc.FindRobot(ctx, project, robotNameOf(credential))
	if err != nil {
		return got, result{}, err
	}
	switch {
	case found && owned && claimed(credential.Status.RobotID, robot.ID, credential.Status.RobotMarkedAt, robot.Created):
		return r.healRobot(ctx, credential, registry, hc, got, robot)
	case found && adopting(credential):
		if holder, err := r.holderOf(ctx, credential, robot.ID); err != nil || holder != "" {
			return got, result{reason: reasonRobotClaimed, message: "the Harbor robot " + robotNameOf(credential) + " is held by the credential " + holder, requeue: credentialRequeue}, err
		}
		return r.takeOver(ctx, credential, registry, hc, got, robot)
	case found:
		return got, result{reason: reasonNameTaken, message: "the Harbor robot " + robotNameOf(credential) + " exists in the project; set the annotation " + AnnotationAdopt + " (administrators only) to adopt it", requeue: credentialRequeue}, nil
	}
	return r.createRobot(ctx, credential, registry, hc, got, project, owned)
}

func (r *CredentialReconciler) createRobot(ctx context.Context, credential *v1alpha1.RegistryCredential, registry *v1alpha1.Registry, hc *harbor.Client, got robotFacts, project string, owned bool) (robotFacts, result, error) {
	if !owned {
		if err := r.recordRobot(ctx, credential, robotNameOf(credential), 0); err != nil {
			return got, result{}, err
		}
	}
	robot, secret, err := hc.CreateRobot(ctx, project, robotNameOf(credential), credential.Spec.Permission)
	if errors.Is(err, harbor.ErrConflict) {
		return got, result{reason: reasonNameTaken, message: "the Harbor robot " + robotNameOf(credential) + " exists in the project", requeue: credentialRequeue}, r.forgetUnlessOwned(ctx, credential, owned)
	}
	if err != nil {
		return got, result{}, errors.Join(err, r.forgetUnlessOwned(ctx, credential, owned))
	}
	if err := r.recordRobot(ctx, credential, robotNameOf(credential), robot.ID); err != nil {
		return got, result{}, err
	}
	got.robotName, got.robotID, got.username = robotNameOf(credential), robot.ID, robot.Name
	got.secretReset = credential.Annotations[AnnotationResetSecret]
	return r.writeSecret(ctx, credential, registry, got, secret)
}

func (r *CredentialReconciler) forgetUnlessOwned(ctx context.Context, credential *v1alpha1.RegistryCredential, owned bool) error {
	if owned {
		return nil
	}
	return r.recordRobot(ctx, credential, "", 0)
}

func (r *CredentialReconciler) holderOf(ctx context.Context, credential *v1alpha1.RegistryCredential, robotID int64) (string, error) {
	var all v1alpha1.RegistryCredentialList
	if err := r.Client.List(ctx, &all); err != nil {
		return "", err
	}
	for i := range all.Items {
		other := &all.Items[i]
		if client.ObjectKeyFromObject(other) != client.ObjectKeyFromObject(credential) && other.Status.RobotID == robotID {
			return other.Namespace + "/" + other.Name, nil
		}
	}
	return "", nil
}

func (r *CredentialReconciler) takeOver(ctx context.Context, credential *v1alpha1.RegistryCredential, registry *v1alpha1.Registry, hc *harbor.Client, got robotFacts, robot harbor.Robot) (robotFacts, result, error) {
	sameRobot := credential.Status.RobotID == robot.ID
	if err := r.recordRobot(ctx, credential, robotNameOf(credential), robot.ID); err != nil {
		return got, result{}, err
	}
	got.robotName, got.robotID, got.username = robotNameOf(credential), robot.ID, robot.Name
	got.secretReset = credential.Annotations[AnnotationResetSecret]
	if sameRobot && r.secretIsValid(ctx, credential, robot.Name, registry.Status.Host) {
		got.secretKept = false
		got.secretName = SecretName(credential)
		return got, readyRobot(credential), nil
	}
	return r.keepSecret(ctx, credential, registry, got)
}

func (r *CredentialReconciler) healRobot(ctx context.Context, credential *v1alpha1.RegistryCredential, registry *v1alpha1.Registry, hc *harbor.Client, got robotFacts, robot harbor.Robot) (robotFacts, result, error) {
	got.robotName, got.robotID, got.username = robotNameOf(credential), robot.ID, robot.Name
	requested := credential.Annotations[AnnotationResetSecret]
	if requested == credential.Status.SecretReset && got.secretKept {
		return r.keepSecret(ctx, credential, registry, got)
	}
	if requested != credential.Status.SecretReset || !r.secretIsValid(ctx, credential, robot.Name, registry.Status.Host) {
		return r.resetRobot(ctx, credential, registry, hc, got, robot)
	}
	return got, readyRobot(credential), nil
}

func (r *CredentialReconciler) resetRobot(ctx context.Context, credential *v1alpha1.RegistryCredential, registry *v1alpha1.Registry, hc *harbor.Client, got robotFacts, robot harbor.Robot) (robotFacts, result, error) {
	secret, err := randomString(r.random(), robotSecretLength, true)
	if err != nil {
		return got, result{}, err
	}
	if err := hc.SetRobotSecret(ctx, robot.ID, secret); err != nil {
		return got, result{}, err
	}
	got.robotName, got.robotID, got.username = robotNameOf(credential), robot.ID, robot.Name
	got.secretReset = credential.Annotations[AnnotationResetSecret]
	return r.writeSecret(ctx, credential, registry, got, secret)
}

func readyRobot(credential *v1alpha1.RegistryCredential) result {
	return result{ready: true, reason: reasonReady, message: "the robot of " + credential.Name + " is ready", requeue: credentialRequeue}
}

func (r *CredentialReconciler) writeSecret(ctx context.Context, credential *v1alpha1.RegistryCredential, registry *v1alpha1.Registry, got robotFacts, password string) (robotFacts, result, error) {
	secret, err := r.dockerSecret(ctx, credential, registry.Status.Host, got.username, password)
	if err != nil {
		return got, result{}, err
	}
	got.secretKept = false
	return r.applySecret(ctx, credential, got, secret)
}

func (r *CredentialReconciler) keepSecret(ctx context.Context, credential *v1alpha1.RegistryCredential, registry *v1alpha1.Registry, got robotFacts) (robotFacts, result, error) {
	secret, err := r.keptSecret(ctx, credential, registry.Status.Host, got.username)
	if err != nil {
		return got, result{}, err
	}
	got.secretKept = true
	return r.applySecret(ctx, credential, got, secret)
}

func (r *CredentialReconciler) applySecret(ctx context.Context, credential *v1alpha1.RegistryCredential, got robotFacts, secret *corev1.Secret) (robotFacts, result, error) {
	foreign, err := r.dropSecretOfOtherType(ctx, credential, secret)
	if err != nil {
		return got, result{}, err
	}
	if foreign {
		return got, result{reason: reasonSecretNameTaken, message: "the Secret " + secret.Name + " exists and is not owned by this credential", requeue: credentialRequeue}, nil
	}
	if err := ssa.Apply(ctx, r.Client, secret, v1alpha1.RegistryFieldManager); err != nil {
		return got, result{}, err
	}
	got.secretName = secret.Name
	return got, readyRobot(credential), nil
}

func (r *CredentialReconciler) dropSecretOfOtherType(ctx context.Context, credential *v1alpha1.RegistryCredential, want *corev1.Secret) (bool, error) {
	var current corev1.Secret
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(want), &current)
	if apierrors.IsNotFound(err) || (err == nil && current.Type == want.Type) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(&current, credential) {
		return true, nil
	}
	return false, client.IgnoreNotFound(r.Client.Delete(ctx, &current))
}

func (r *CredentialReconciler) dockerSecret(ctx context.Context, credential *v1alpha1.RegistryCredential, host, username, password string) (*corev1.Secret, error) {
	config, err := dockerConfig(host, username, password)
	if err != nil {
		return nil, err
	}
	data := map[string][]byte{dockerConfigKey: config, "host": []byte(host), "username": []byte(username), "password": []byte(password)}
	return r.credentialSecret(ctx, credential, corev1.SecretTypeDockerConfigJson, data)
}

func (r *CredentialReconciler) keptSecret(ctx context.Context, credential *v1alpha1.RegistryCredential, host, username string) (*corev1.Secret, error) {
	return r.credentialSecret(ctx, credential, corev1.SecretTypeOpaque, map[string][]byte{"host": []byte(host), "username": []byte(username)})
}

func (r *CredentialReconciler) credentialSecret(ctx context.Context, credential *v1alpha1.RegistryCredential, secretType corev1.SecretType, data map[string][]byte) (*corev1.Secret, error) {
	project, _, err := objectstorage.LoadProject(ctx, r.Client, credential.Namespace)
	if err != nil {
		return nil, err
	}
	owner := metav1.NewControllerRef(credential, v1alpha1.GroupVersion.WithKind("RegistryCredential"))
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            SecretName(credential),
			Namespace:       credential.Namespace,
			Labels:          platform.Labels("RegistryCredential", credential.Name, project.Tenant, project.Name),
			OwnerReferences: []metav1.OwnerReference{*owner},
		},
		Type: secretType,
		Data: data,
	}, nil
}

func dockerConfig(host, username, password string) ([]byte, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"username": username, "password": password, "auth": auth}}})
}

func (r *CredentialReconciler) secretIsValid(ctx context.Context, credential *v1alpha1.RegistryCredential, username, host string) bool {
	var secret corev1.Secret
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: credential.Namespace, Name: SecretName(credential)}, &secret); err != nil {
		return false
	}
	password := string(secret.Data["password"])
	config, err := dockerConfig(host, username, password)
	return err == nil && password != "" && string(secret.Data["username"]) == username && string(secret.Data["host"]) == host && string(secret.Data[dockerConfigKey]) == string(config)
}

func (r *CredentialReconciler) finalize(ctx context.Context, credential *v1alpha1.RegistryCredential) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(credential, platform.Finalizer("RegistryCredential")) {
		return ctrl.Result{}, nil
	}
	if credential.Status.RobotName != robotNameOf(credential) {
		return ctrl.Result{}, r.updateFinalizers(ctx, credential, controllerutil.RemoveFinalizer)
	}
	hc, unavailable, err := r.connector().Harbor(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if unavailable == "" {
		err = r.deleteOwnRobot(ctx, credential, hc)
	}
	if unavailable != "" || err != nil {
		out := harborOutcome(result{reason: reasonNotAvailable, message: unavailable, requeue: unavailableRequeue}, err)
		if err := r.writeStatus(ctx, credential, statusFacts(credential), out); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: out.requeue}, nil
	}
	return ctrl.Result{}, r.updateFinalizers(ctx, credential, controllerutil.RemoveFinalizer)
}

func (r *CredentialReconciler) deleteOwnRobot(ctx context.Context, credential *v1alpha1.RegistryCredential, hc *harbor.Client) error {
	id := credential.Status.RobotID
	if id == 0 {
		var registry v1alpha1.Registry
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: credential.Namespace, Name: credential.Spec.RegistryRef.Name}, &registry)
		if apierrors.IsNotFound(err) || (err == nil && registry.Status.ProjectName == "") {
			return nil
		}
		if err != nil {
			return err
		}
		robot, found, err := hc.FindRobot(ctx, registry.Status.ProjectName, robotNameOf(credential))
		if err != nil && !errors.Is(err, harbor.ErrNotFound) {
			return err
		}
		if !found || !claimed(0, robot.ID, credential.Status.RobotMarkedAt, robot.Created) {
			return nil
		}
		id = robot.ID
	}
	return hc.DeleteRobot(ctx, id)
}

func statusFacts(credential *v1alpha1.RegistryCredential) robotFacts {
	status := credential.Status
	return robotFacts{robotID: status.RobotID, robotName: status.RobotName, username: status.Username, secretReset: status.SecretReset, secretName: status.SecretName, secretKept: meta.IsStatusConditionTrue(status.Conditions, conditionSecretKept)}
}

func (r *CredentialReconciler) recordRobot(ctx context.Context, credential *v1alpha1.RegistryCredential, name string, id int64) error {
	var marked *metav1.Time
	if name != "" {
		now := metav1.Now()
		marked = &now
	}
	mutate := func(status *v1alpha1.RegistryCredentialStatus) {
		status.RobotName, status.RobotID, status.RobotMarkedAt = name, id, marked
	}
	if err := r.mutateStatus(ctx, credential, mutate); err != nil {
		return err
	}
	mutate(&credential.Status)
	return nil
}

func (r *CredentialReconciler) updateFinalizers(ctx context.Context, credential *v1alpha1.RegistryCredential, change func(client.Object, string) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.RegistryCredential
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(credential), &latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !change(&latest, platform.Finalizer("RegistryCredential")) {
			return nil
		}
		if err := r.Client.Update(ctx, &latest, client.FieldOwner(v1alpha1.RegistryFieldManager)); err != nil {
			return err
		}
		credential.ResourceVersion = latest.ResourceVersion
		credential.Finalizers = latest.Finalizers
		return nil
	})
}

func (r *CredentialReconciler) mutateStatus(ctx context.Context, credential *v1alpha1.RegistryCredential, mutate func(*v1alpha1.RegistryCredentialStatus)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.RegistryCredential
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(credential), &latest); err != nil {
			return err
		}
		mutate(&latest.Status)
		return r.Client.Status().Update(ctx, &latest, client.FieldOwner(v1alpha1.RegistryFieldManager))
	})
}

func (r *CredentialReconciler) writeStatus(ctx context.Context, credential *v1alpha1.RegistryCredential, got robotFacts, out result) error {
	status := metav1.ConditionFalse
	if out.ready {
		status = metav1.ConditionTrue
	}
	return r.mutateStatus(ctx, credential, func(current *v1alpha1.RegistryCredentialStatus) {
		current.ObservedGeneration = credential.Generation
		current.RobotID, current.RobotName, current.Username = got.robotID, got.robotName, got.username
		current.SecretReset, current.SecretName = got.secretReset, got.secretName
		setSecretKept(&current.Conditions, got.secretKept, credential.Generation)
		meta.SetStatusCondition(&current.Conditions, metav1.Condition{Type: conditionReady, Status: status, Reason: out.reason, Message: out.message, ObservedGeneration: credential.Generation})
	})
}

func setSecretKept(conditions *[]metav1.Condition, kept bool, generation int64) {
	if !kept {
		meta.RemoveStatusCondition(conditions, conditionSecretKept)
		return
	}
	meta.SetStatusCondition(conditions, metav1.Condition{Type: conditionSecretKept, Status: metav1.ConditionTrue, Reason: reasonPasswordKept, Message: "the robot keeps the password that the customer already has; the Secret has no password key; change the annotation " + AnnotationResetSecret + " to set a new password", ObservedGeneration: generation})
}

func credentialsOfRegistry(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.RegistryCredentialList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.RegistryRef.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
			}
		}
		return requests
	}
}

func (r *CredentialReconciler) SetupWithManager(mgr ctrl.Manager) error {
	changed := predicate.Or[client.Object](predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}, deletionStarted())
	return ctrl.NewControllerManagedBy(mgr).Named("registrycredential").
		For(&v1alpha1.RegistryCredential{}, builder.WithPredicates(changed)).
		Watches(&v1alpha1.Registry{}, handler.EnqueueRequestsFromMapFunc(credentialsOfRegistry(r.Client))).
		Complete(r)
}
