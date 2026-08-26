package web

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestInspectSSORiskParsesGetUserBotFlag(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/auth_mgmt.AuthManagement/GetUser" || request.Method != http.MethodPost {
			http.NotFound(writer, request)
			return
		}
		if !strings.Contains(request.Header.Get("Cookie"), "sso=test-sso") {
			t.Errorf("cookie = %q", request.Header.Get("Cookie"))
		}
		if request.Header.Get("Content-Type") != "application/grpc-web+proto" {
			t.Errorf("content-type = %q", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("x-grpc-web") != "1" {
			t.Errorf("x-grpc-web = %q", request.Header.Get("x-grpc-web"))
		}
		writer.Header().Set("Content-Type", "application/grpc-web+proto")
		_, _ = writer.Write(ssoriskGetUserFrame(t, 1, "event=$login,policy=deny,risk=0.9"))
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
	if risk.Policy != "deny" || risk.Risk != 0.9 {
		t.Fatalf("details = %#v", risk)
	}
}

func TestInspectSSORiskTreatsOmittedBotFlagAsUnknown(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/auth_mgmt.AuthManagement/GetUser" || request.Method != http.MethodPost {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/grpc-web+proto")
		_, _ = writer.Write(ssoriskGetUserFrame(t, 0, ""))
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
	if risk.Inspected || risk.Flagged || risk.Source != 0 || risk.Unauthorized {
		t.Fatalf("risk = %#v", risk)
	}
}

func ssoriskGetUserFrame(t *testing.T, source int, details string) []byte {
	t.Helper()
	message := protowire.AppendString(nil, "user-1")
	message = append(protowire.AppendTag(nil, 1, protowire.BytesType), message...)
	if source != 0 || details != "" {
		var fields []byte
		if source != 0 {
			fields = protowire.AppendVarint(protowire.AppendTag(fields, 45, protowire.VarintType), uint64(source))
		}
		if details != "" {
			fields = protowire.AppendString(protowire.AppendTag(fields, 46, protowire.BytesType), details)
		}
		message = append(message, fields...)
	}
	frame := make([]byte, 5+len(message))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(message)))
	copy(frame[5:], message)
	trailer := []byte("grpc-status: 0\r\n")
	tail := make([]byte, 5+len(trailer))
	tail[0] = 0x80
	binary.BigEndian.PutUint32(tail[1:5], uint32(len(trailer)))
	copy(tail[5:], trailer)
	return append(frame, tail...)
}
