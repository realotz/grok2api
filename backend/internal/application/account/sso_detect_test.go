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

func TestSSORiskPatrolInspectsEnabledWebAccounts(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "sso-patrol.db"))
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
	token, err := cipher.Encrypt("patrol-sso")
	if err != nil {
		t.Fatal(err)
	}
	repo := relational.NewAccountRepository(database)
	credential, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "patrol", SourceKey: "patrol",
		EncryptedAccessToken: token, Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, nil, nil, nil, provider.NewRegistry(detectSSORiskAdapter{risk: provider.SSOAccountRisk{
		Inspected: true, Flagged: true, Source: 1, Policy: "deny", Event: "$registration", Risk: 1, RiskSet: true,
	}}), cipher, nil)
	if err := service.runSSORiskPatrol(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SSOBotRiskSet || stored.SSOBotRisk != 1 || !stored.SSOBotRiskEver {
		t.Fatalf("patrol inspect = %#v", stored)
	}
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
	adapter := detectSSORiskAdapter{risk: provider.SSOAccountRisk{
		Inspected: true, Flagged: true, Source: 2, Details: "policy=deny,risk=1.00,event=$registration",
		Policy: "deny", Event: "$registration", Risk: 1, RiskSet: true,
	}}
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
	if stored.SSOBotFlagSource != 2 || stored.SSOBotPolicy != "deny" || stored.SSOBotEvent != "$registration" || !stored.SSOBotRiskSet || stored.SSOBotRisk != 1 || !stored.SSOBotRiskEver {
		t.Fatalf("stored inspect = %#v", stored)
	}
	if !stored.BlocksVideoBySSORisk() {
		t.Fatal("risk=1 should block video")
	}
	item = service.detectSSOAccount(ctx, detectSSORiskAdapter{risk: provider.SSOAccountRisk{
		Inspected: true, Flagged: false, Source: 0, Details: "policy=allow,risk=0.40,event=$login",
		Policy: "allow", Event: "$login", Risk: 0.4, RiskSet: true,
	}}, credential.ID)
	if item.Outcome != SSODetectOutcomeOK {
		t.Fatalf("clean item = %#v", item)
	}
	stored, err = repo.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SSOBotFlagSource != 0 || stored.SSOBotPolicy != "allow" || stored.SSOBotEvent != "$login" || !stored.SSOBotRiskSet || stored.SSOBotRisk != 0.4 || !stored.SSOBotRiskEver {
		t.Fatalf("decayed inspect = %#v", stored)
	}
	if stored.BlocksVideoBySSORisk() {
		t.Fatal("risk=0.4 should not block video")
	}
}

func TestDetectSSOAccountSyncsInspectToLinkedBuildAndConsole(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "sso-detect-sync.db"))
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
	encrypt := func(plain string) string {
		t.Helper()
		token, err := cipher.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	repo := relational.NewAccountRepository(database)
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const userID = "shared-user"
	web, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "web", SourceKey: "sso:" + digest,
		UserID: userID, EncryptedAccessToken: encrypt("web-sso"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, AuthType: accountdomain.AuthTypeOAuth, Name: "build", SourceKey: "build-" + digest[:8],
		UserID: userID, EncryptedAccessToken: encrypt("build-oauth"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	console, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderConsole, AuthType: accountdomain.AuthTypeSSO, Name: "console", SourceKey: "console-sso:" + digest,
		UserID: userID, EncryptedAccessToken: encrypt("console-sso"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkWebToBuild(ctx, web.ID, build.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReconcileProviderLinks(ctx, web.ID); err != nil {
		t.Fatal(err)
	}
	adapter := detectSSORiskAdapter{risk: provider.SSOAccountRisk{
		Inspected: true, Flagged: true, Source: 1, Details: "policy=deny,risk=1.00,event=$registration",
		Policy: "deny", Event: "$registration", Risk: 1, RiskSet: true,
	}}
	service := NewService(repo, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	item := service.detectSSOAccount(ctx, adapter, web.ID)
	if item.Outcome != SSODetectOutcomeFlagged {
		t.Fatalf("item = %#v", item)
	}
	for _, id := range []uint64{web.ID, build.ID, console.ID} {
		stored, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if stored.SSOBotFlagSource != 1 || stored.SSOBotPolicy != "deny" || stored.SSOBotEvent != "$registration" || !stored.SSOBotRiskSet || stored.SSOBotRisk != 1 || !stored.SSOBotRiskEver {
			t.Fatalf("account %d inspect = %#v", id, stored)
		}
		if !stored.BlocksBySSORisk() {
			t.Fatalf("account %d should block on risk=1", id)
		}
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
