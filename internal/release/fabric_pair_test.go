package release

import (
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestFabricImageFromValuesRequiresAnExplicitReleasePin(t *testing.T) {
	const tag = "v1.2.7@" + ownDigest
	const values = `crds:
  enabled: true
global:
  registry:
    address: ghcr.io/cloudyfolks-io
  images:
    fabric:
      repository: fabric
      tag: ` + tag + "\n"
	const image = "ghcr.io/cloudyfolks-io/fabric:" + tag
	if got, err := FabricImageFromValues([]byte(values)); err != nil || got != image {
		t.Fatalf("image = %q, error = %v, want %q", got, err, image)
	}
	for _, test := range []struct{ name, values string }{
		{"chart defaults", "crds:\n  enabled: true\n"},
		{"missing registry", strings.Replace(values, "    address: ghcr.io/cloudyfolks-io\n", "", 1)},
		{"missing repository", strings.Replace(values, "      repository: fabric\n", "", 1)},
		{"mutable tag", strings.Replace(values, tag, "v1.2.6", 1)},
		{"release without ACL receipt", strings.Replace(values, tag, "v1.2.6@"+ownDigest, 1)},
		{"nonrelease tag", strings.Replace(values, tag, "dev@"+ownDigest, 1)},
		{"malformed digest", strings.Replace(values, tag, "v1.2.7@sha256:01234", 1)},
		{"malformed registry", strings.Replace(values, "ghcr.io/cloudyfolks-io", "https://ghcr.io/cloudyfolks-io", 1)},
		{"duplicate key", values + "      tag: " + tag + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := FabricImageFromValues([]byte(test.values)); err == nil || got != "" {
				t.Fatalf("image = %q, error = %v, want a rejected pin", got, err)
			}
		})
	}
}

func TestVerifyFabricPairChecksTheBuiltSchemaAndPinnedImage(t *testing.T) {
	const image = "localhost:5001/fabric:test@" + ownDigest
	const oldImage = "ghcr.io/cloudyfolks-io/fabric:v1.2.6@" + ownDigest
	const schema = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: security-groups.fabric.cloudyfolks.io
spec:
  versions:
    - name: v1
      served: true
      storage: true
      subresources:
        status: {}
      schema:
        openAPIV3Schema:
          properties:
            status:
              properties:
                observedGeneration:
                  type: integer
                ingressLastSyncSuccess:
                  type: boolean
                egressLastSyncSuccess:
                  type: boolean
`
	const controller = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: fabric-controller
  namespace: kube-system
spec:
  template:
    spec:
      containers:
        - name: fabric-controller
          image: `
	for _, test := range []struct {
		name            string
		crd             string
		deployedImage   string
		expectedImage   string
		inventory       []string
		controllerOrder int
		wantError       string
	}{
		{"paired release", schema, image, image, []string{image}, 20, ""},
		{"old Fabric binary", schema, oldImage, image, []string{image, oldImage}, 20, "does not use the expected image"},
		{"old Fabric schema", strings.Replace(schema, "observedGeneration", "olderStatus", 1), image, image, []string{image}, 20, "lacks the v1 ACL generation receipt"},
		{"unlisted image", schema, image, image, nil, 20, "inventories do not contain"},
		{"mutable selected image", schema, image, "localhost:5001/fabric:test", []string{image}, 20, "SHA-256 digest"},
		{"controller before schema", schema, image, image, []string{image}, 0, "CRD before the controller"},
	} {
		t.Run(test.name, func(t *testing.T) {
			crds, err := splitDocuments([]byte(test.crd))
			if err != nil {
				t.Fatal(err)
			}
			deployments, err := splitDocuments([]byte(controller + test.deployedImage + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			bundle := Bundle{
				Spec:   v1alpha1.ReleaseSpec{Images: []string{image}},
				Images: test.inventory,
				Groups: []Group{{Name: "crds", Objects: crds}, {Name: "fabric", Order: test.controllerOrder, Objects: deployments}},
			}
			err = VerifyFabricPair(bundle, test.expectedImage)
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("verify error = %v, want %q", err, test.wantError)
			}
		})
	}
}
