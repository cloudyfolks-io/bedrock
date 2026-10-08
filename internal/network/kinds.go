package network

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

type KindProbe func(schema.GroupKind) bool

func KindsFromMapper(m meta.RESTMapper) KindProbe {
	return func(gk schema.GroupKind) bool {
		_, err := m.RESTMapping(gk)
		return err == nil
	}
}

func UnsupportedKind(kind string) *Violation {
	return &Violation{Reason: ReasonUnsupported, Message: fmt.Sprintf("the Fabric kind %s is not installed; upgrade the fabric component", kind)}
}

func fabricKind(kind string) schema.GroupKind {
	return schema.GroupKind{Group: fabric.Group, Kind: kind}
}

func missingKind(known KindProbe, kind string) *Violation {
	if known == nil || known(fabricKind(kind)) {
		return nil
	}
	return UnsupportedKind(kind)
}
