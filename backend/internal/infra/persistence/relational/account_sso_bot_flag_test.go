package relational

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestSSOBotFlagPersistsIndependentlyOfTokenRefresh(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(openTestDatabase(t))
	web, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "web", SourceKey: "web-sso",
		EncryptedAccessToken: "sso-old", AuthStatus: account.AuthStatusActive,
		SSOBotFlagSource: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if web.SSOBotFlagSource != 1 {
		t.Fatalf("imported source = %d", web.SSOBotFlagSource)
	}

	updated, err := repo.UpdateTokens(ctx, web.ID, "sso-new", "", time.Now().UTC().Add(time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if updated.EncryptedAccessToken != "sso-new" {
		t.Fatalf("token was not replaced: %#v", updated)
	}
	if updated.SSOBotFlagSource != 1 {
		t.Fatalf("token refresh dropped SSO robot mark: %#v", updated)
	}
	if updated.BuildBotFlagSource != 0 {
		t.Fatalf("Build mark leaked onto SSO: %#v", updated)
	}

	reimported, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "web", SourceKey: "web-sso",
		EncryptedAccessToken: "sso-newer", AuthStatus: account.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reimported.EncryptedAccessToken != "sso-newer" || reimported.SSOBotFlagSource != 1 {
		t.Fatalf("upsert lost token or robot mark: %#v", reimported)
	}
}

func TestUpdateSSOBotFlagSourcesUsesAccessTokenCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(openTestDatabase(t))
	web, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "web", SourceKey: "web-cas",
		EncryptedAccessToken: "current-sso", AuthStatus: account.AuthStatusActive,
		SSOBotFlagSource: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSSOBotFlagSources(ctx, []repository.SSOBotFlagSourceUpdate{{
		AccountID: web.ID, ExpectedEncryptedAccessToken: "stale-sso", Source: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(ctx, web.ID)
	if err != nil || stored.EncryptedAccessToken != "current-sso" || stored.SSOBotFlagSource != 2 {
		t.Fatalf("stale CAS changed credential = %#v, err=%v", stored, err)
	}
	if err := repo.UpdateSSOBotFlagSources(ctx, []repository.SSOBotFlagSourceUpdate{{
		AccountID: web.ID, ExpectedEncryptedAccessToken: "current-sso", Source: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.Get(ctx, web.ID)
	if err != nil || stored.EncryptedAccessToken != "current-sso" || stored.SSOBotFlagSource != 1 {
		t.Fatalf("matching CAS did not update mark = %#v, err=%v", stored, err)
	}
}

func TestSSOBotFlagIndexDrivesListFilter(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(openTestDatabase(t))
	now := time.Now().UTC()
	flagged, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "flagged", SourceKey: "flagged",
		EncryptedAccessToken: "sso-1", AuthStatus: account.AuthStatusActive, SSOBotFlagSource: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "clean", SourceKey: "clean",
		EncryptedAccessToken: "sso-2", AuthStatus: account.AuthStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderBuild, Name: "build", SourceKey: "build",
		EncryptedAccessToken: "build", AuthStatus: account.AuthStatusActive, BuildBotFlagSource: 1,
	}); err != nil {
		t.Fatal(err)
	}

	ids, err := repo.ListSSOBotFlaggedAccountIDs(ctx)
	if err != nil || !slices.Equal(ids, []uint64{flagged.ID}) {
		t.Fatalf("flagged IDs = %v, err=%v", ids, err)
	}
	count, err := repo.CountSSOBotFlagged(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count = %d, err=%v", count, err)
	}
	_, total, err := repo.List(ctx, repository.AccountListQuery{
		Page: repository.PageQuery{Limit: 20},
		Filter: repository.AccountListFilter{
			Provider: string(account.ProviderWeb), Risk: "flagged", Now: now,
		},
	})
	if err != nil || total != 1 {
		t.Fatalf("flagged list total = %d, err=%v", total, err)
	}
	_, total, err = repo.List(ctx, repository.AccountListQuery{
		Page: repository.PageQuery{Limit: 20},
		Filter: repository.AccountListFilter{
			Provider: string(account.ProviderWeb), Risk: "normal", Now: now,
		},
	})
	if err != nil || total != 1 {
		t.Fatalf("normal list total = %d, err=%v", total, err)
	}
}

func TestInitializeSchemaAddsSSOBotFlagWithoutLosingCredentials(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	repo := NewAccountRepository(database)
	created, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "legacy", SourceKey: "legacy-sso",
		EncryptedAccessToken: "preserved-sso", AuthStatus: account.AuthStatusActive, SSOBotFlagSource: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.db.WithContext(ctx).Exec("DROP INDEX IF EXISTS idx_account_credentials_sso_bot_flag").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.withSQLiteForeignKeysDisabled(ctx, func() error {
		if err := database.db.WithContext(ctx).Migrator().DropConstraint(&accountCredentialModel{}, "chk_account_credentials_sso_bot_flag_source"); err != nil {
			return err
		}
		return database.db.WithContext(ctx).Migrator().DropColumn(&accountCredentialModel{}, "SSOBotFlagSource")
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if !database.db.WithContext(ctx).Migrator().HasColumn(&accountCredentialModel{}, "SSOBotFlagSource") {
		t.Fatal("sso_bot_flag_source column was not restored")
	}
	assertSQLiteIndexes(t, database, "account_credentials", "idx_account_credentials_sso_bot_flag")
	stored, err := repo.Get(ctx, created.ID)
	if err != nil || stored.EncryptedAccessToken != "preserved-sso" {
		t.Fatalf("stored credential = %#v, err=%v", stored, err)
	}
}
