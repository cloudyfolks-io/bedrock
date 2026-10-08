package addons

import "time"

const (
	Kata                 = "kata"
	Loki                 = "loki"
	ConditionPreflight   = "Preflight"
	LabelKVM             = "bedrock.cloudyfolks.io/kvm"
	AnnotationConfigHash = "bedrock.cloudyfolks.io/config-hash"
	Finalizer            = "bedrock.cloudyfolks.io/addon"
	DefaultInterval      = 30 * time.Second
	DefaultReadyInterval = 5 * time.Minute
)
