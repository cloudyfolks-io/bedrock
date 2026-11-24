package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/saxess"
)

const saxessUsage = `usage: bedrock authn saxess invite <username> --state-secret NAME [--kubeconfig PATH]
       bedrock authn saxess approve-device <username> --state-secret NAME --device-id ID --public-key KEY [--kubeconfig PATH]
       bedrock authn saxess revoke-device <username> --state-secret NAME --device-id ID [--kubeconfig PATH]`

func validSAxessSecretName(name string) bool {
	return len(validation.IsDNS1123Subdomain(name)) == 0
}

func newSAxessBackend(c client.Client, name string) *saxess.Backend {
	store := saxess.NewSecretStore(c, c, client.ObjectKey{Namespace: release.SystemNamespace, Name: name})
	return saxess.NewBackend(store, saxess.VerifyCardProof)
}

func runSAxess(ctx context.Context, args []string, newClient func(string) (client.Client, error), stdout, stderr io.Writer) int {
	if len(args) < 2 || (args[0] != "invite" && args[0] != "approve-device" && args[0] != "revoke-device") {
		fmt.Fprintln(stderr, saxessUsage)
		return 2
	}
	op := args[0]
	flags := flag.NewFlagSet("authn saxess "+op, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprintln(stderr, saxessUsage) }
	kubeconfig := flags.String("kubeconfig", "/var/lib/k0s/pki/admin.conf", "admin kubeconfig")
	stateSecret := flags.String("state-secret", "", "pre-provisioned state Secret in bedrock-system")
	var deviceID, publicKey string
	if op != "invite" {
		flags.StringVar(&deviceID, "device-id", "", "exact known device ID")
	}
	if op == "approve-device" {
		flags.StringVar(&publicKey, "public-key", "", "exact known card public key")
	}
	if err := flags.Parse(args[2:]); err != nil {
		return 2
	}
	username, err := v1alpha1.NormalizeUsername(args[1])
	if err != nil || flags.NArg() != 0 || !validSAxessSecretName(*stateSecret) ||
		(op != "invite" && deviceID == "") || (op == "approve-device" && publicKey == "") {
		flags.Usage()
		return 2
	}

	c, err := newClient(*kubeconfig)
	if err != nil {
		return fail(stderr, err)
	}
	backend := newSAxessBackend(c, *stateSecret)
	key := client.ObjectKey{Namespace: release.SystemNamespace, Name: v1alpha1.UserObjectName(username)}
	if op == "invite" {
		invite, err := backend.CreateBedrockInvite(ctx, c, key, 10*time.Minute)
		if err != nil {
			return fail(stderr, err)
		}
		if err := json.NewEncoder(stdout).Encode(invite); err != nil {
			return fail(stderr, fmt.Errorf("write sAxess invite: %w", err))
		}
		return 0
	}

	var user v1alpha1.User
	if err := c.Get(ctx, key, &user); err != nil {
		return fail(stderr, err)
	}
	if user.UID == "" || (op == "approve-device" && (user.Spec.Disabled || !user.DeletionTimestamp.IsZero())) {
		return fail(stderr, saxess.ErrEnrollmentRejected)
	}
	if op == "approve-device" {
		err = backend.ApproveDevice(ctx, string(user.UID), deviceID, publicKey)
	} else {
		err = backend.RevokeDevice(ctx, string(user.UID), deviceID)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}
