package proxy

import (
	"encoding/json"
	"regexp"
	"time"
)

const (
	maxSubscriptions      = 64
	maxConnectionsPerUser = 8
	sendQueue             = 1024
	pingInterval          = 25 * time.Second
	pongWait              = 60 * time.Second
	writeWait             = 10 * time.Second
	clientFrameLimit      = 16 << 10
	readHardLimit         = 1 << 20
	defaultWatchTimeout   = 30 * time.Minute
	defaultSessionCheck   = 30 * time.Second
	healthyWatchAge       = 5 * time.Second

	CloseSessionEnded = 4401
	CloseProtocol     = 4400
	CloseOverloaded   = 1013
	CloseGoingAway    = 1001
)

var (
	defaultBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	namePattern    = regexp.MustCompile(`^[a-z0-9]{1,63}$`)
	nsPattern      = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?)?$`)
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

type ClientFrame struct {
	Op              string `json:"op"`
	ID              string `json:"id"`
	Group           string `json:"group"`
	Version         string `json:"version"`
	Resource        string `json:"resource"`
	Namespace       string `json:"namespace"`
	LabelSelector   string `json:"labelSelector"`
	FieldSelector   string `json:"fieldSelector"`
	ResourceVersion string `json:"resourceVersion"`
}

type StreamLimits struct {
	MaxSubscriptions int `json:"maxSubscriptions"`
}

type ServerFrame struct {
	Type            string          `json:"type"`
	ID              string          `json:"id,omitempty"`
	Event           string          `json:"event,omitempty"`
	ResourceVersion string          `json:"resourceVersion,omitempty"`
	Object          json.RawMessage `json:"object,omitempty"`
	Code            string          `json:"code,omitempty"`
	Message         string          `json:"message,omitempty"`
	ExpiresAt       string          `json:"expiresAt,omitempty"`
	Limits          *StreamLimits   `json:"limits,omitempty"`
}

func errorFrame(id, code, message string) ServerFrame {
	return ServerFrame{Type: "error", ID: id, Code: code, Message: message}
}

func validSubscription(f ClientFrame) bool {
	return idPattern.MatchString(f.ID) && versionPattern.MatchString(f.Version) && namePattern.MatchString(f.Resource) &&
		nsPattern.MatchString(f.Namespace) && versionPattern.MatchString(f.ResourceVersion) &&
		len(f.LabelSelector) <= 1024 && len(f.FieldSelector) <= 1024
}

func subscriptionKey(f ClientFrame) string {
	return f.Group + "|" + f.Version + "|" + f.Resource + "|" + f.Namespace + "|" + f.LabelSelector + "|" + f.FieldSelector
}
