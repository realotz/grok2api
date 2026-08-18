package account

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

func TestClassifyModelProbeText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want ModelProbeOutcome
	}{
		{name: "object", text: `{"ok":true,"probe":"account-test"}`, want: ModelProbeOutcomeOK},
		{name: "fenced", text: "```json\n{\"ok\":true}\n```", want: ModelProbeOutcomeOK},
		{name: "prose", text: "sorry I cannot help with that", want: ModelProbeOutcomeFlagged},
		{name: "array", text: `["ok"]`, want: ModelProbeOutcomeFlagged},
		{name: "empty", text: "   ", want: ModelProbeOutcomeError},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyModelProbeText(test.text); got != test.want {
				t.Fatalf("classify(%q) = %s, want %s", test.text, got, test.want)
			}
		})
	}
}

func TestExtractModelProbeTextFromResponsesAndChat(t *testing.T) {
	t.Parallel()
	chat := []byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
	if got := extractModelProbeText(chat); got != `{"ok":true}` {
		t.Fatalf("chat text = %q", got)
	}
	responses := []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}]}`)
	if got := extractModelProbeText(responses); got != `{"ok":true}` {
		t.Fatalf("responses text = %q", got)
	}
	if got := extractModelProbeText(nil); got != "" {
		t.Fatalf("empty text = %q", got)
	}
}

func TestParseImagePreviewURL(t *testing.T) {
	t.Parallel()
	if got := parseImagePreviewURL([]byte(`{"data":[{"url":"https://example.com/cat.png"}]}`)); got != "https://example.com/cat.png" {
		t.Fatalf("url = %q", got)
	}
	if got := parseImagePreviewURL([]byte(`{"data":[{"b64_json":"AAAA","mime_type":"image/jpeg"}]}`)); got != "data:image/jpeg;base64,AAAA" {
		t.Fatalf("b64 = %q", got)
	}
	if got := parseImagePreviewURL([]byte(`{"data":[]}`)); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestTestAccountModelFlagsInvalidJSON(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-probe-flag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("live-sso")
	if err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "probe", SourceKey: "probe",
		EncryptedAccessToken: token, Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceProviderRoutes(ctx, accountdomain.ProviderWeb, []modeldomain.Route{{
		PublicID: "grok-3", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-3",
		Capability: modeldomain.CapabilityChat, Origin: modeldomain.OriginCatalog, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceAccountCapabilities(ctx, credential.ID, []string{"grok-3"}, credential.CreatedAt); err != nil {
		t.Fatal(err)
	}
	adapter := &modelProbeResponseAdapter{body: `{"choices":[{"message":{"content":"I will not return JSON"}}]}`}
	service := NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	service.SetModels(models)
	result, err := service.TestAccountModel(ctx, credential.ID, "grok-3", modeldomain.CapabilityChat, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != ModelProbeOutcomeFlagged {
		t.Fatalf("result = %#v", result)
	}
	stored, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SSOBotFlagSource != 1 {
		t.Fatalf("source = %d", stored.SSOBotFlagSource)
	}
}

func TestTestAccountModelEmptyResponseDoesNotFlag(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-probe-empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("live-sso")
	if err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "empty", SourceKey: "empty",
		EncryptedAccessToken: token, Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceProviderRoutes(ctx, accountdomain.ProviderWeb, []modeldomain.Route{{
		PublicID: "grok-3", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-3",
		Capability: modeldomain.CapabilityChat, Origin: modeldomain.OriginCatalog, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceAccountCapabilities(ctx, credential.ID, []string{"grok-3"}, credential.CreatedAt); err != nil {
		t.Fatal(err)
	}
	adapter := &modelProbeResponseAdapter{body: `{"choices":[{"message":{"content":""}}]}`}
	service := NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	service.SetModels(models)
	result, err := service.TestAccountModel(ctx, credential.ID, "grok-3", modeldomain.CapabilityChat, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != ModelProbeOutcomeError {
		t.Fatalf("result = %#v", result)
	}
	stored, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SSOBotFlagSource != 0 {
		t.Fatalf("source = %d", stored.SSOBotFlagSource)
	}
}

func TestTestAccountModelImageDoesNotFlag(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-probe-image.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("live-sso")
	if err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "image", SourceKey: "image",
		EncryptedAccessToken: token, Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceProviderRoutes(ctx, accountdomain.ProviderWeb, []modeldomain.Route{{
		PublicID: "grok-imagine-image", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-imagine-image-quality",
		Capability: modeldomain.CapabilityImage, Origin: modeldomain.OriginCatalog, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	adapter := modelProbeImageAdapter{body: `{"data":[]}`}
	service := NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	service.SetModels(models)
	result, err := service.TestAccountModel(ctx, credential.ID, "grok-imagine-image", modeldomain.CapabilityImage, "小猫在天上")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != ModelProbeOutcomeError {
		t.Fatalf("result = %#v", result)
	}
	stored, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SSOBotFlagSource != 0 {
		t.Fatalf("image test wrote risk source = %d", stored.SSOBotFlagSource)
	}
}

func TestTestAccountModelFastVideoFlagsRiskWithoutPreview(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-probe-video.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("live-sso")
	if err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "video", SourceKey: "video",
		EncryptedAccessToken: token, Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceProviderRoutes(ctx, accountdomain.ProviderWeb, []modeldomain.Route{{
		PublicID: "grok-imagine-video", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-imagine-video",
		Capability: modeldomain.CapabilityVideo, Origin: modeldomain.OriginCatalog, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	adapter := modelProbeVideoAdapter{result: provider.VideoResult{URL: "https://cdn.example/scenery.mp4"}}
	service := NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	service.SetModels(models)
	result, err := service.TestAccountModel(ctx, credential.ID, "grok-imagine-video", modeldomain.CapabilityVideo, "小猫在天上")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != ModelProbeOutcomeFlagged || result.PreviewURL != "" {
		t.Fatalf("result = %#v", result)
	}
	stored, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SSOBotFlagSource != 1 {
		t.Fatalf("source = %d", stored.SSOBotFlagSource)
	}
}

func TestTestAccountModelVideoDecrementsQuota(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-probe-video-quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("live-sso")
	if err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "video-quota", SourceKey: "video-quota",
		EncryptedAccessToken: token, Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := accounts.ReplaceQuotaWindows(ctx, credential.ID, "", now, []accountdomain.QuotaWindow{{
		AccountID: credential.ID, Mode: accountdomain.QuotaModeWebVideo720p, Remaining: 1, Total: 1, UpdatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceProviderRoutes(ctx, accountdomain.ProviderWeb, []modeldomain.Route{{
		PublicID: "grok-imagine-video", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-imagine-video",
		Capability: modeldomain.CapabilityVideo, Origin: modeldomain.OriginCatalog, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	adapter := modelProbeVideoAdapter{result: provider.VideoResult{AssetID: "vid_local_probe_01", URL: "https://cdn.example/keep.mp4"}}
	service := NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	service.SetModels(models)
	result, err := service.TestAccountModel(ctx, credential.ID, "grok-imagine-video", modeldomain.CapabilityVideo, "小猫在天上")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != ModelProbeOutcomeOK || result.PreviewURL != "/v1/media/videos/vid_local_probe_01" {
		t.Fatalf("result = %#v", result)
	}
	windows, err := accounts.GetQuotaWindows(ctx, []uint64{credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	remaining := -1
	for _, window := range windows[credential.ID] {
		if window.Mode == accountdomain.QuotaModeWebVideo720p {
			remaining = window.Remaining
		}
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d", remaining)
	}
}

func TestWithoutModelProbeRiskStripsFlagged(t *testing.T) {
	t.Parallel()
	result, err := withoutModelProbeRisk(AccountModelProbeResult{Outcome: ModelProbeOutcomeFlagged, PreviewURL: "https://example.com/a.png"}, nil)
	if err != nil || result.Outcome != ModelProbeOutcomeError || result.PreviewURL != "https://example.com/a.png" {
		t.Fatalf("result = %#v err=%v", result, err)
	}
}

func TestListAccountTestModelsFiltersCapabilities(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-probe-list.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "list", SourceKey: "list",
		EncryptedAccessToken: "sso", Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceProviderRoutes(ctx, accountdomain.ProviderWeb, []modeldomain.Route{
		{PublicID: "grok-3", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-3", Capability: modeldomain.CapabilityChat, Origin: modeldomain.OriginCatalog, Enabled: true},
		{PublicID: "grok-imagine-image", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-imagine-image-quality", Capability: modeldomain.CapabilityImage, Origin: modeldomain.OriginCatalog, Enabled: true},
		{PublicID: "grok-imagine-image-2.0-web", Provider: accountdomain.ProviderWeb, UpstreamModel: "imagine-image-edit", Capability: modeldomain.CapabilityImageEdit, Origin: modeldomain.OriginCatalog, Enabled: true},
		{PublicID: "grok-imagine-video", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-imagine-video", Capability: modeldomain.CapabilityVideo, Origin: modeldomain.OriginCatalog, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceAccountCapabilities(ctx, credential.ID, []string{"grok-3"}, credential.CreatedAt); err != nil {
		t.Fatal(err)
	}
	service := NewService(accounts, nil, nil, nil, nil, nil, nil)
	service.SetModels(models)
	items, err := service.ListAccountTestModels(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %#v", items)
	}
	for _, item := range items {
		if item.Capability == modeldomain.CapabilityImageEdit {
			t.Fatalf("image_edit leaked: %#v", items)
		}
	}
}

type modelProbeResponseAdapter struct {
	body string
}

func (modelProbeResponseAdapter) Provider() accountdomain.Provider { return accountdomain.ProviderWeb }

func (a modelProbeResponseAdapter) ForwardResponse(context.Context, provider.ResponseResourceRequest) (*provider.Response, error) {
	return &provider.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(a.body)),
	}, nil
}

func (modelProbeResponseAdapter) ListModels(context.Context, accountdomain.Credential) ([]string, error) {
	return []string{"grok-3"}, nil
}

type modelProbeImageAdapter struct {
	body string
}

func (modelProbeImageAdapter) Provider() accountdomain.Provider { return accountdomain.ProviderWeb }

func (a modelProbeImageAdapter) GenerateImage(context.Context, provider.ImageGenerationRequest) (*provider.Response, error) {
	return &provider.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(a.body)),
	}, nil
}

func (modelProbeImageAdapter) ListModels(context.Context, accountdomain.Credential) ([]string, error) {
	return []string{"grok-imagine-image-quality"}, nil
}

type modelProbeVideoAdapter struct {
	result provider.VideoResult
}

func (modelProbeVideoAdapter) Provider() accountdomain.Provider { return accountdomain.ProviderWeb }

func (a modelProbeVideoAdapter) GenerateVideo(context.Context, provider.VideoRequest) (provider.VideoResult, error) {
	return a.result, nil
}

func (modelProbeVideoAdapter) ListModels(context.Context, accountdomain.Credential) ([]string, error) {
	return []string{"grok-imagine-video"}, nil
}
