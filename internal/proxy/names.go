package proxy

const (
	ConsoleClientID   = "bedrock-console"
	ConsoleSecretName = "bedrock-console-client"

	CookieSession = "__Host-bedrock_console"
	CookieLogin   = "__Host-bedrock_oauth"
	HeaderCSRF    = "X-CSRF-Token"

	PathLogin      = "/oauth/login"
	PathCallback   = "/oauth/callback"
	PathLogout     = "/oauth/logout"
	PathSession    = "/proxy/v1/session"
	PathAuthz      = "/proxy/v1/authz"
	PathProjects   = "/proxy/v1/projects"
	PathMetrics    = "/proxy/v1/metrics/range"
	PathStream     = "/proxy/v1/stream"
	PathKubeconfig = "/kubeconfig"
	PathCA         = "/ca.crt"

	StreamSubprotocol = "bedrock.stream.v1"
	AggregateLabel    = "bedrock.cloudyfolks.io/aggregate-to-proxy"
)
