package gateway

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	clientkeyapp "github.com/chenyme/grok2api/backend/internal/application/clientkey"
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
	"github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
)

const guardedImagePublicModel = "grok-imagine-image-2.0-web"

func TestFastWebImage20MarksRiskAndFailsOver(t *testing.T) {
	tests := []struct {
		name       string
		capability modeldomain.Capability
		upstream   string
		streaming  bool
		execute    func(context.Context, *Service, clientkey.Key) (*Result, error)
	}{
		{
			name: "generation_stream", capability: modeldomain.CapabilityImage,
			upstream: "grok-imagine-image-2.0", streaming: true,
			execute: func(ctx context.Context, service *Service, key clientkey.Key) (*Result, error) {
				return service.GenerateImage(ctx, ImageGenerationInput{
					RequestID: "image-risk-generation", ClientKey: key, PublicModel: guardedImagePublicModel,
					Prompt: "draw", Count: 1, ResponseFormat: "url", Streaming: true,
				})
			},
		},
		{
			name: "edit", capability: modeldomain.CapabilityImageEdit,
			upstream: "imagine-image-edit",
			execute: func(ctx context.Context, service *Service, key clientkey.Key) (*Result, error) {
				return service.EditImage(ctx, ImageEditInput{
					RequestID: "image-risk-edit", ClientKey: key, PublicModel: guardedImagePublicModel,
					Prompt: "edit", ImageURLs: []string{"data:image/png;base64,a"}, Count: 1, ResponseFormat: "url",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "image-risk.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := database.InitializeSchema(ctx); err != nil {
				t.Fatal(err)
			}

			accountRepo := relational.NewAccountRepository(database)
			modelRepo := relational.NewModelRepository(database)
			auditRepo := relational.NewAuditRepository(database)
			responseRepo := relational.NewResponseRepository(database)
			keyRepo := relational.NewClientKeyRepository(database)
			credentials := make([]account.Credential, 0, 2)
			for index, name := range []string{"risk", "healthy"} {
				credential, _, createErr := accountRepo.UpsertByIdentity(ctx, account.Credential{
					Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, WebTier: account.WebTierSuper,
					Name: name, SourceKey: name, EncryptedAccessToken: "token-" + name,
					Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 200 - index*100, MaxConcurrent: 1,
				})
				if createErr != nil {
					t.Fatal(createErr)
				}
				credentials = append(credentials, credential)
			}
			if err := modelRepo.UpsertRoutes(ctx, []modeldomain.Route{{
				PublicID: guardedImagePublicModel, Provider: account.ProviderWeb, UpstreamModel: test.upstream,
				Capability: test.capability, Enabled: true,
			}}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			for _, credential := range credentials {
				if err := modelRepo.ReplaceAccountCapabilities(ctx, credential.ID, []string{test.upstream}, now); err != nil {
					t.Fatal(err)
				}
			}
			key, err := keyRepo.Create(ctx, clientkey.Key{
				Name: "image-risk-key", Prefix: "image-risk", SecretHash: strings.Repeat("a", 64),
				EncryptedSecret: "encrypted", Enabled: true, RPMLimit: 60, MaxConcurrent: 4,
			})
			if err != nil {
				t.Fatal(err)
			}

			const riskWindow = 200 * time.Millisecond
			adapter := &imageRiskAdapter{safeDelay: 300 * time.Millisecond}
			registry := provider.NewRegistry(adapter)
			sticky := memory.NewStickyStore()
			accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), sticky, registry, testCipher(t), nil)
			selector := NewSelector(accountRepo, memory.NewConcurrencyLimiter(), sticky, registry, time.Hour, time.Second, time.Minute)
			service := NewService(modelRepo, auditRepo, accountService, clientkeyapp.NewService(keyRepo, nil, nil, 60, 4, nil), registry, selector, responseRepo, 2)
			service.imageRiskReadyWithin = riskWindow

			result, err := test.execute(ctx, service, key)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(result.Body)
			if err != nil {
				t.Fatal(err)
			}
			result.Finalize(Usage{}, "", "")
			_ = result.Body.Close()
			if !strings.Contains(string(body), "healthy") || strings.Contains(string(body), `"risk"`) {
				t.Fatalf("delivered body = %q", body)
			}
			if attempts := adapter.Attempts(test.capability); len(attempts) != 2 || attempts[0] != credentials[0].ID || attempts[1] != credentials[1].ID {
				t.Fatalf("attempts = %#v, want risk then healthy", attempts)
			}

			flagged, err := accountRepo.Get(ctx, credentials[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if flagged.SSOBotFlagSource != 1 || !flagged.SSOBotRiskSet || flagged.SSOBotRisk != 1 || !flagged.SSOBotRiskEver || flagged.SSOBotDetails != "image_scenery" {
				t.Fatalf("flagged account = %#v", flagged)
			}
			healthy, err := accountRepo.Get(ctx, credentials[1].ID)
			if err != nil {
				t.Fatal(err)
			}
			if healthy.SSOBotFlagSource != 0 || healthy.SSOBotRiskSet || healthy.SSOBotRiskEver {
				t.Fatalf("healthy account = %#v", healthy)
			}
		})
	}
}

func TestFastWebImage20RouteScope(t *testing.T) {
	tests := []struct {
		name      string
		route     modeldomain.Route
		operation audit.Operation
		want      bool
	}{
		{name: "generation", route: modeldomain.Route{Provider: account.ProviderWeb, PublicID: "Web/grok-imagine-image-2.0-web"}, operation: audit.OperationImage, want: true},
		{name: "edit", route: modeldomain.Route{Provider: account.ProviderWeb, PublicID: "Web/grok-imagine-image-2.0-web"}, operation: audit.OperationImageEdit, want: true},
		{name: "base_model", route: modeldomain.Route{Provider: account.ProviderWeb, PublicID: "Web/grok-imagine-image-2.0"}, operation: audit.OperationImage},
		{name: "console", route: modeldomain.Route{Provider: account.ProviderConsole, PublicID: "Console/grok-imagine-image-2.0-web"}, operation: audit.OperationImage},
		{name: "layers", route: modeldomain.Route{Provider: account.ProviderWeb, PublicID: "Web/grok-imagine-image-2.0-web"}, operation: audit.OperationImageLayer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isFastWebImage20Route(test.route, test.operation); got != test.want {
				t.Fatalf("isFastWebImage20Route() = %t, want %t", got, test.want)
			}
		})
	}
}

type imageRiskAdapter struct {
	mu        sync.Mutex
	safeDelay time.Duration
	attempts  map[modeldomain.Capability][]uint64
}

func (a *imageRiskAdapter) Provider() account.Provider { return account.ProviderWeb }

func (a *imageRiskAdapter) Definition() provider.Definition {
	definition := testConversationDefinition(account.ProviderWeb)
	definition.Media.ImageGeneration = true
	definition.Media.ImageEdit = true
	return definition
}

func (a *imageRiskAdapter) GenerateImage(ctx context.Context, request provider.ImageGenerationRequest) (*provider.Response, error) {
	a.record(modeldomain.CapabilityImage, request.Credential.ID)
	body := "event: image_generation.completed\ndata: {\"account\":\"" + request.Credential.Name + "\"}\n\n"
	return a.response(ctx, request.Credential.Name, request.Streaming, body)
}

func (a *imageRiskAdapter) EditImage(ctx context.Context, request provider.ImageEditRequest) (*provider.Response, error) {
	a.record(modeldomain.CapabilityImageEdit, request.Credential.ID)
	body := `{"data":[{"account":"` + request.Credential.Name + `"}]}`
	return a.response(ctx, request.Credential.Name, false, body)
}

func (a *imageRiskAdapter) response(ctx context.Context, accountName string, streaming bool, body string) (*provider.Response, error) {
	delay := time.Duration(0)
	if accountName == "healthy" {
		delay = a.safeDelay
	}
	header := http.Header{"Content-Type": {"application/json"}}
	if streaming {
		header.Set("Content-Type", "text/event-stream")
		if delay > 0 {
			reader, writer := io.Pipe()
			go func() {
				_, _ = io.WriteString(writer, "event: image_generation.partial_image\ndata: {\"account\":\"healthy\"}\n\n")
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					_ = writer.CloseWithError(ctx.Err())
				case <-timer.C:
					_, _ = io.WriteString(writer, body)
					_ = writer.Close()
				}
			}()
			return &provider.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: header, Body: reader, QuotaUnits: 1}, nil
		}
		return &provider.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: header, Body: io.NopCloser(strings.NewReader(body)), QuotaUnits: 1}, nil
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return &provider.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: header, Body: io.NopCloser(strings.NewReader(body)), QuotaUnits: 1}, nil
}

func (a *imageRiskAdapter) record(capability modeldomain.Capability, accountID uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.attempts == nil {
		a.attempts = make(map[modeldomain.Capability][]uint64)
	}
	a.attempts[capability] = append(a.attempts[capability], accountID)
}

func (a *imageRiskAdapter) Attempts(capability modeldomain.Capability) []uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]uint64(nil), a.attempts[capability]...)
}
