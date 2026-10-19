package v1alpha1

const (
	EncryptionActionRotate = "rotate"
	EncryptionActionRevert = "revert"

	RotationAddingKey        = "AddingKey"
	RotationPromotingKey     = "PromotingKey"
	RotationRewritingSecrets = "RewritingSecrets"
	RotationRemovingKey      = "RemovingKey"

	AnnotationEncryptionApply = "bedrock.cloudyfolks.io/encryption-apply"
)
