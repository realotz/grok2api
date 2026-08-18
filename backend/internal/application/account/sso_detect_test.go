package account

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

type detectSSORiskAdapter struct {
	risk provider.SSOAccountRisk
	err  error
}

func (a detectSSORiskAdapter) Provider() accountdomain.Provider { return accountdomain.ProviderWeb }

func (a detectSSORiskAdapter) InspectSSORisk(context.Context, accountdomain.Credential) (provider.SSOAccountRisk, error) {
	return a.risk, a.err
}

func TestDetectSSOAccountsRequiresExplicitScope(t *testing.T) {
	service := &Service{providers: provider.NewRegistry(detectSSORiskAdapter{})}
	if _, _, err := service.DetectSSOAccountsWithProgress(context.Background(), accountdomain.ProviderWeb, nil, false, nil, nil); err == nil {
		t.Fatal("missing all and ids should be rejected")
	}
	if _, _, err := service.DetectSSOAccountsWithProgress(context.Background(), accountdomain.ProviderWeb, []uint64{1}, true, nil, nil); err == nil {
		t.Fatal("all and ids together should be rejected")
	}
	if _, _, err := service.DetectSSOAccountsWithProgress(context.Background(), accountdomain.ProviderBuild, []uint64{1}, false, nil, nil); err == nil {
		t.Fatal("Build provider should be rejected")
	}
}

func TestDetectSSOAccountPersistsRobotMarkWithoutRewritingToken(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "sso-detect.db"))
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
	repo := relational.NewAccountRepository(database)
	credential, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "flagged", SourceKey: "flagged",
		EncryptedAccessToken: token, Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := detectSSORiskAdapter{risk: provider.SSOAccountRisk{Inspected: true, Flagged: true, Source: 2, Details: "policy=allow"}}
	service := NewService(repo, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	item := service.detectSSOAccount(ctx, adapter, credential.ID)
	if item.Outcome != SSODetectOutcomeFlagged || item.Source != 2 {
		t.Fatalf("item = %#v", item)
	}
	stored, err := repo.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EncryptedAccessToken != token {
		t.Fatalf("detect rewrote SSO token")
	}
	if stored.SSOBotFlagSource != 2 {
		t.Fatalf("stored source = %d", stored.SSOBotFlagSource)
	}
}

func TestSupportsRiskFilterIncludesSSOProviders(t *testing.T) {
	if !supportsRiskFilter(string(accountdomain.ProviderWeb)) || !supportsRiskFilter(string(accountdomain.ProviderConsole)) || !supportsRiskFilter(string(accountdomain.ProviderBuild)) {
		t.Fatal("Web, Console, and Build should accept the risk filter")
	}
	if supportsRiskFilter("") {
		t.Fatal("empty provider should not accept the risk filter")
	}
}
