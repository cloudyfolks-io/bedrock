package cli

import (
	"github.com/cloudyfolks-io/bedrock/internal/compute"
	"github.com/cloudyfolks-io/bedrock/internal/images"
	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

func subresourceRegistrars() []func(*proxy.Subresources) error {
	return []func(*proxy.Subresources) error{compute.RegisterSubresources, images.RegisterSubresources}
}
