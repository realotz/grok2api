package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

func TestInspectSSORiskParsesHomepageBotFlag(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		if !strings.Contains(request.Header.Get("Cookie"), "sso=test-sso") {
			t.Errorf("cookie = %q", request.Header.Get("Cookie"))
		}
		if request.Header.Get("Accept") != "text/html,application/xhtml+xml" {
			t.Errorf("accept = %q", request.Header.Get("Accept"))
		}
		_, _ = writer.Write([]byte(`{"botFlagSource":1,"botFlagDetails":"event=$login,policy=deny,risk=0.9"}`))
	}))
	t.Cleanup(server.Close)
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := cipher.Encrypt("test-sso")
	adapter := NewAdapter(Config{BaseURL: server.URL}, infraegress.NewManager(egressRepositoryStub{}, cipher), cipher, nil, nil)
	risk, err := adapter.InspectSSORisk(context.Background(), account.Credential{
		ID: 1, Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, EncryptedAccessToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !risk.Inspected || !risk.Flagged || risk.Source != 1 || risk.Unauthorized {
		t.Fatalf("risk = %#v", risk)
	}
}
