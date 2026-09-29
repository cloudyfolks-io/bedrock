package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

const tokenUsage = "usage: bedrock token create --roles a,b [--expiry 1h] [--kubeconfig PATH] [--k0s-bin PATH]"

func tokenCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(stderr, tokenUsage)
		return 2
	}
	flags := flag.NewFlagSet("token create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	roleList := flags.String("roles", "", "comma separated roles for the joining node")
	expiry := flags.String("expiry", "1h", "token validity")
	kubeconfig := flags.String("kubeconfig", "/var/lib/k0s/pki/admin.conf", "admin kubeconfig")
	k0sBin := flags.String("k0s-bin", k0s.DefaultBinary, "k0s binary path")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *roleList == "" {
		fmt.Fprintln(stderr, "token create: --roles is required")
		return 2
	}
	nodeRoles := strings.Split(*roleList, ",")
	for _, role := range nodeRoles {
		if !v1alpha1.ValidRole(role) {
			return fail(stderr, fmt.Errorf("unknown role %q", role))
		}
	}
	c, err := newClusterClient(*kubeconfig)
	if err != nil {
		return fail(stderr, err)
	}
	token, err := buildToken(context.Background(), c, k0s.Client{Exec: host.RealExec{}, Binary: *k0sBin}, nodeRoles, *expiry, "/etc/k0s/k0s.yaml", apiserver.Dir)
	if err != nil {
		return fail(stderr, err)
	}
	encoded, err := k0s.EncodeToken(token)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, encoded)
	return 0
}

func buildToken(ctx context.Context, c client.Client, k0sClient k0s.Client, nodeRoles []string, expiry, k0sConfigPath, authnDir string) (k0s.Token, error) {
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return k0s.Token{}, err
	}
	if cluster.Status.Version == "" {
		return k0s.Token{}, fmt.Errorf("cluster has no installed version yet")
	}
	var rel v1alpha1.Release
	if err := c.Get(ctx, client.ObjectKey{Name: cluster.Status.Version}, &rel); err != nil {
		return k0s.Token{}, err
	}
	k0sRole := "worker"
	var k0sConfig []byte
	var authnFiles map[string][]byte
	if hasRole(nodeRoles, v1alpha1.RoleControlPlane) {
		k0sRole = "controller"
		raw, err := os.ReadFile(k0sConfigPath)
		if err != nil {
			return k0s.Token{}, err
		}
		k0sConfig = raw
		files, err := readAuthnFiles(authnDir)
		if err != nil {
			return k0s.Token{}, err
		}
		authnFiles = files
	}
	k0sToken, err := k0sClient.CreateToken(ctx, k0sRole, expiry)
	if err != nil {
		return k0s.Token{}, err
	}
	return k0s.Token{Version: rel.Spec.Version, Roles: nodeRoles, K0sToken: k0sToken, K0sConfig: k0sConfig, AuthnFiles: authnFiles, VIP: cluster.Spec.API.VIP, Image: rel.Spec.Image, K0sVersion: rel.Spec.K0sVersion, K0sChecksums: rel.Spec.K0sChecksums, SupportedOS: rel.Spec.SupportedOS, Mirror: cluster.Spec.Registry.Mirror}, nil
}

func readAuthnFiles(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, name := range []string{apiserver.AuthenticationFile, apiserver.WebhookFile} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("controller token needs %s: %w", name, err)
		}
		files[name] = raw
	}
	return files, nil
}
