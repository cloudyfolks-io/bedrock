package v1alpha1

import "time"

const AgentReportInterval = 60 * time.Second

const (
	EncryptionActionRotate = "rotate"
	EncryptionActionRevert = "revert"

	RotationAddingKey        = "AddingKey"
	RotationPromotingKey     = "PromotingKey"
	RotationRewritingSecrets = "RewritingSecrets"
	RotationRemovingKey      = "RemovingKey"

	ConditionEncryptionStalled = "EncryptionRotationStalled"

	AnnotationEncryptionApply = "bedrock.cloudyfolks.io/encryption-apply"
)
