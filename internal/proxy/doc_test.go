package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyDocNamesEveryRoute(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "proxy.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	names := []string{
		PathLogin, PathCallback, PathLogout, PathSession, PathAuthz, PathProjects, PathMetrics, PathStream,
		PathKubeconfig, PathCA, CookieSession, CookieLogin, HeaderCSRF, StreamSubprotocol, AggregateLabel,
	}
	for query := range queryTable() {
		names = append(names, query)
	}
	for _, name := range names {
		if !strings.Contains(doc, name) {
			t.Errorf("docs/proxy.md does not name %s", name)
		}
	}
}
