package audit

const (
	Dir           = "/etc/bedrock/audit"
	PolicyFile    = "policy.yaml"
	ConfigMapName = "bedrock-audit-policy"
	LogDir        = "/var/log/bedrock/audit"
	LogFile       = "audit.log"
)

func Args() map[string]string {
	return map[string]string{
		"audit-policy-file":   Dir + "/" + PolicyFile,
		"audit-log-path":      LogDir + "/" + LogFile,
		"audit-log-maxsize":   "100",
		"audit-log-maxbackup": "10",
	}
}
