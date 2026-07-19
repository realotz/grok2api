package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
	"github.com/chenyme/grok2api/backend/internal/domain/media"
	"github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestRecoverVideoJobsRetriesUsageWithoutRegeneratingVideo(t *testing.T) {
	completedAt := time.Now().UTC()
	repository := &videoUsageRepository{job: media.Job{
		ID: "video_usage_recovery", RequestID: "request-usage-recovery",
		ClientKeyID: 1, ClientKeyName: "client", AccountID: 2, AccountName: "account",
		Provider: "grok_web", Model: "grok-imagine-video", ModelRouteID: 3, UpstreamModel: "video",
		Seconds: 8, Quality: "720p", Status: media.StatusCompleted, InputJSON: `{}`, CreatedAt: completedAt.Add(-time.Minute), CompletedAt: &completedAt,
	}}
	recorder := &durableVideoAuditRecorder{failures: 1}
	service := &Service{mediaJobs: repository, audits: recorder}
	if err := service.RecoverVideoJobs(context.Background()); err == nil {
		t.Fatal("first durable audit failure was ignored")
	}
	if repository.job.UsageRecordedAt != nil {
		t.Fatal("usage was marked before durable audit commit")
	}
	if err := service.RecoverVideoJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.job.UsageRecordedAt == nil || recorder.calls != 2 {
		t.Fatalf("recordedAt = %v, audit calls = %d", repository.job.UsageRecordedAt, recorder.calls)
	}
	if recorder.last.EventID != "video_usage_video_usage_recovery" || recorder.last.EstimatedCostInUSDTicks <= 0 {
		t.Fatalf("audit = %#v", recorder.last)
	}
}

func TestRecoverVideoJobsRecordsFailedAuditWithEgress(t *testing.T) {
	completedAt := time.Now().UTC()
	nodeID := uint64(42)
	repository := &videoUsageRepository{job: media.Job{
		ID: "video_failed_recovery", RequestID: "request-failed-recovery",
		ClientKeyID: 1, ClientKeyName: "client", AccountID: 2, AccountName: "account",
		Provider: "grok_web", Model: "grok-imagine-video", ModelRouteID: 3, UpstreamModel: "video",
		Seconds: 8, Quality: "720p", Status: media.StatusFailed, ErrorCode: "generation_failed", ErrorMessage: "upstream disconnected",
		EgressNodeID: &nodeID, EgressNodeName: "warp", EgressScope: "grok_web", EgressMode: "proxy",
		InputJSON: `{}`, CreatedAt: completedAt.Add(-time.Minute), CompletedAt: &completedAt,
	}}
	recorder := &durableVideoAuditRecorder{}
	service := &Service{mediaJobs: repository, audits: recorder}
	if err := service.RecoverVideoJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.job.UsageRecordedAt == nil || recorder.calls != 1 {
		t.Fatalf("recordedAt = %v, audit calls = %d", repository.job.UsageRecordedAt, recorder.calls)
	}
	if recorder.last.StatusCode != 502 || recorder.last.ErrorCode != "generation_failed" || recorder.last.EgressNodeID == nil || *recorder.last.EgressNodeID != nodeID || recorder.last.EgressNodeName != "warp" || recorder.last.EgressMode != audit.EgressModeProxy {
		t.Fatalf("audit = %#v", recorder.last)
	}
	if recorder.last.EstimatedCostInUSDTicks != 0 || recorder.last.MediaOutputSeconds != 0 {
		t.Fatalf("failed job was billed: %#v", recorder.last)
	}
}

func TestVideoQueueIsBoundedAndDeduplicated(t *testing.T) {
	service := &Service{}
	service.ConfigureMedia(&videoUsageRepository{}, 1)
	capacity := cap(service.mediaQueue)
	for index := range capacity {
		if !service.enqueueVideoJob(fmt.Sprintf("video_%d", index)) {
			t.Fatalf("enqueue %d failed before capacity", index)
		}
	}
	if !service.enqueueVideoJob("video_0") {
		t.Fatal("duplicate queued job should be treated as accepted")
	}
	if service.enqueueVideoJob("video_overflow") {
		t.Fatal("queue accepted a job beyond its capacity")
	}
}

func TestPersistRemoteVideoRetriesSameResultWithoutRegeneration(t *testing.T) {
	adapter := &videoPersistAdapter{failures: 1}
	store := &videoAssetStoreStub{}
	service := &Service{mediaAssets: store}
	credential := account.Credential{ID: 42, Provider: account.ProviderWeb}
	result, err := service.persistRemoteVideo(context.Background(), "video_job", adapter, credential, provider.VideoResult{URL: "https://assets.grok.com/video.mp4", ContentType: "video/mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.generateCalls != 0 || adapter.downloadCalls != 2 || adapter.lastCredentialID != credential.ID {
		t.Fatalf("generate=%d download=%d credential=%d", adapter.generateCalls, adapter.downloadCalls, adapter.lastCredentialID)
	}
	if store.saveCalls != 1 || result.AssetID != "vid_local" || result.ContentType != "video/mp4" {
		t.Fatalf("store calls=%d result=%#v", store.saveCalls, result)
	}
}

func TestVideoRefreshesStaleBillingAndSwitchesExpiredSuperAccount(t *testing.T) {
	adapter := &videoFailoverAdapter{}
	service, accountRepo, jobs, accounts := newVideoFailoverTestService(t, adapter, 3, 2)
	now := time.Now().UTC()
	if err := accountRepo.SaveBilling(context.Background(), account.Billing{AccountID: accounts[0].ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := accountRepo.SaveBilling(context.Background(), account.Billing{AccountID: accounts[1].ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	adapter.billings = map[uint64]account.Billing{
		accounts[0].ID: {PlanName: "Free", SyncedAt: now},
	}

	job := testVideoFailoverJob(accounts[0])
	jobs.job = job
	result, lease, err := service.generateVideoWithFailover(context.Background(), &job, testBuildVideoRoute(), adapter, nil)
	if lease != nil {
		lease.Release()
	}
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetID != "generated" || len(adapter.generateAccountIDs) != 1 || adapter.generateAccountIDs[0] != accounts[1].ID {
		t.Fatalf("result=%#v generate accounts=%v", result, adapter.generateAccountIDs)
	}
	if len(adapter.billingAccountIDs) != 1 || adapter.billingAccountIDs[0] != accounts[0].ID {
		t.Fatalf("billing refresh accounts=%v", adapter.billingAccountIDs)
	}
	if jobs.job.AccountID != accounts[1].ID || jobs.job.AccountName != accounts[1].Name {
		t.Fatalf("persisted ownership=%d/%q", jobs.job.AccountID, jobs.job.AccountName)
	}
}

func TestVideoSkipsRiskFlaggedPinnedOAuthAccount(t *testing.T) {
	adapter := &videoFailoverAdapter{}
	service, accountRepo, jobs, accounts := newVideoFailoverTestService(t, adapter, 3, 2)
	adapter.flaggedAccountIDs = map[uint64]bool{accounts[0].ID: true}
	now := time.Now().UTC()
	for _, credential := range accounts {
		if err := accountRepo.SaveBilling(context.Background(), account.Billing{AccountID: credential.ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	job := testVideoFailoverJob(accounts[0])
	jobs.job = job
	result, lease, err := service.generateVideoWithFailover(context.Background(), &job, testBuildVideoRoute(), adapter, nil)
	if lease != nil {
		lease.Release()
	}
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetID != "generated" || len(adapter.generateAccountIDs) != 1 || adapter.generateAccountIDs[0] != accounts[1].ID {
		t.Fatalf("result=%#v generate accounts=%v", result, adapter.generateAccountIDs)
	}
	if jobs.job.AccountID != accounts[1].ID {
		t.Fatalf("persisted account=%d, want %d", jobs.job.AccountID, accounts[1].ID)
	}
}

func TestVideoRejectsPoolContainingOnlyRiskFlaggedOAuth(t *testing.T) {
	adapter := &videoFailoverAdapter{}
	service, accountRepo, jobs, accounts := newVideoFailoverTestService(t, adapter, 3, 1)
	adapter.flaggedAccountIDs = map[uint64]bool{accounts[0].ID: true}
	if err := accountRepo.SaveBilling(context.Background(), account.Billing{
		AccountID: accounts[0].ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	job := testVideoFailoverJob(accounts[0])
	jobs.job = job
	_, lease, err := service.generateVideoWithFailover(context.Background(), &job, testBuildVideoRoute(), adapter, nil)
	if lease != nil {
		lease.Release()
	}
	if err == nil {
		t.Fatal("risk-only video pool was accepted")
	}
	if len(adapter.generateAccountIDs) != 0 {
		t.Fatalf("risk account reached video adapter: %v", adapter.generateAccountIDs)
	}
}

func TestVideoUnauthorizedRefreshesCredentialAndRetriesSameAccount(t *testing.T) {
	adapter := &videoFailoverAdapter{generate: func(request provider.VideoRequest, call int) (provider.VideoResult, error) {
		if call == 1 {
			return provider.VideoResult{}, provider.NewVideoFailureError(provider.VideoFailureCreate, true, provider.ErrUnauthorized)
		}
		return provider.VideoResult{AssetID: "generated", ContentType: "video/mp4"}, nil
	}}
	service, accountRepo, jobs, accounts := newVideoFailoverTestService(t, adapter, 3, 1)
	now := time.Now().UTC()
	if err := accountRepo.SaveBilling(context.Background(), account.Billing{AccountID: accounts[0].ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	job := testVideoFailoverJob(accounts[0])
	jobs.job = job
	_, lease, err := service.generateVideoWithFailover(context.Background(), &job, testBuildVideoRoute(), adapter, nil)
	if lease != nil {
		lease.Release()
	}
	if err != nil {
		t.Fatal(err)
	}
	if adapter.refreshCalls != 1 || len(adapter.generateAccountIDs) != 2 || adapter.generateAccountIDs[0] != accounts[0].ID || adapter.generateAccountIDs[1] != accounts[0].ID {
		t.Fatalf("refresh=%d generate accounts=%v", adapter.refreshCalls, adapter.generateAccountIDs)
	}
}

func TestVideoExplicitCreateForbiddenSwitchesAccount(t *testing.T) {
	adapter := &videoFailoverAdapter{generate: func(_ provider.VideoRequest, call int) (provider.VideoResult, error) {
		if call == 1 {
			return provider.VideoResult{}, provider.NewVideoFailureError(provider.VideoFailureCreate, true, videoTestHTTPError(http.StatusForbidden))
		}
		return provider.VideoResult{AssetID: "generated", ContentType: "video/mp4"}, nil
	}}
	service, accountRepo, jobs, accounts := newVideoFailoverTestService(t, adapter, 3, 2)
	now := time.Now().UTC()
	for _, credential := range accounts {
		if err := accountRepo.SaveBilling(context.Background(), account.Billing{AccountID: credential.ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	adapter.billings = map[uint64]account.Billing{accounts[0].ID: {PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now}}

	job := testVideoFailoverJob(accounts[0])
	jobs.job = job
	_, lease, err := service.generateVideoWithFailover(context.Background(), &job, testBuildVideoRoute(), adapter, nil)
	if lease != nil {
		lease.Release()
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(adapter.generateAccountIDs) != 2 || adapter.generateAccountIDs[0] != accounts[0].ID || adapter.generateAccountIDs[1] != accounts[1].ID {
		t.Fatalf("generate accounts=%v", adapter.generateAccountIDs)
	}
	updated, err := accountRepo.Get(context.Background(), accounts[0].ID)
	if err != nil || updated.FailureCount != 0 || updated.CooldownUntil != nil {
		t.Fatalf("403 changed account-wide health: credential=%#v err=%v", updated, err)
	}
	blocked, blockErr := service.selector.AcquirePinned(context.Background(), account.ProviderBuild, accounts[0].ID, testBuildVideoRoute().UpstreamModel, "", true)
	if blocked != nil {
		blocked.Release()
	}
	if blockErr == nil {
		t.Fatal("403 did not apply a model-scoped cooldown")
	}
}

func TestVideoPollFailureNeverSwitchesAccount(t *testing.T) {
	adapter := &videoFailoverAdapter{generate: func(provider.VideoRequest, int) (provider.VideoResult, error) {
		return provider.VideoResult{}, provider.NewVideoFailureError(provider.VideoFailurePoll, false, videoTestHTTPError(http.StatusForbidden))
	}}
	service, accountRepo, jobs, accounts := newVideoFailoverTestService(t, adapter, 3, 2)
	now := time.Now().UTC()
	for _, credential := range accounts {
		if err := accountRepo.SaveBilling(context.Background(), account.Billing{AccountID: credential.ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	job := testVideoFailoverJob(accounts[0])
	jobs.job = job
	_, lease, err := service.generateVideoWithFailover(context.Background(), &job, testBuildVideoRoute(), adapter, nil)
	if lease != nil {
		lease.Release()
	}
	if err == nil {
		t.Fatal("poll failure was ignored")
	}
	if len(adapter.generateAccountIDs) != 1 || adapter.generateAccountIDs[0] != accounts[0].ID {
		t.Fatalf("generate accounts=%v", adapter.generateAccountIDs)
	}
}

func TestVideoAccountFailoverHonorsAttemptLimit(t *testing.T) {
	adapter := &videoFailoverAdapter{generate: func(provider.VideoRequest, int) (provider.VideoResult, error) {
		return provider.VideoResult{}, provider.NewVideoFailureError(provider.VideoFailureCreate, true, videoTestHTTPError(http.StatusTooManyRequests))
	}}
	service, accountRepo, jobs, accounts := newVideoFailoverTestService(t, adapter, 2, 3)
	now := time.Now().UTC()
	for _, credential := range accounts {
		if err := accountRepo.SaveBilling(context.Background(), account.Billing{AccountID: credential.ID, PlanName: "SuperGrok", MonthlyLimit: 15000, SyncedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	job := testVideoFailoverJob(accounts[0])
	jobs.job = job
	_, lease, err := service.generateVideoWithFailover(context.Background(), &job, testBuildVideoRoute(), adapter, nil)
	if lease != nil {
		lease.Release()
	}
	if err == nil {
		t.Fatal("retry limit failure was ignored")
	}
	if len(adapter.generateAccountIDs) != 2 || adapter.generateAccountIDs[0] == adapter.generateAccountIDs[1] {
		t.Fatalf("generate accounts=%v", adapter.generateAccountIDs)
	}
}

type videoFailoverAdapter struct {
	billings           map[uint64]account.Billing
	flaggedAccountIDs  map[uint64]bool
	billingAccountIDs  []uint64
	generateAccountIDs []uint64
	refreshCalls       int
	generate           func(provider.VideoRequest, int) (provider.VideoResult, error)
}

func (a *videoFailoverAdapter) Provider() account.Provider { return account.ProviderBuild }

func (a *videoFailoverAdapter) CredentialMetadata(credential account.Credential) provider.CredentialMetadata {
	return provider.CredentialMetadata{BuildBotFlagged: a.flaggedAccountIDs[credential.ID]}
}

func (a *videoFailoverAdapter) Definition() provider.Definition {
	return provider.Definition{
		Provider: account.ProviderBuild,
		Credential: provider.CredentialSurface{
			Refresh: true,
		},
		Media: provider.MediaSurface{VideoGeneration: true},
	}
}

func (a *videoFailoverAdapter) GenerateVideo(_ context.Context, request provider.VideoRequest) (provider.VideoResult, error) {
	a.generateAccountIDs = append(a.generateAccountIDs, request.Credential.ID)
	if a.generate != nil {
		return a.generate(request, len(a.generateAccountIDs))
	}
	return provider.VideoResult{AssetID: "generated", ContentType: "video/mp4"}, nil
}

func (a *videoFailoverAdapter) GetBilling(_ context.Context, credential account.Credential) (account.Billing, error) {
	a.billingAccountIDs = append(a.billingAccountIDs, credential.ID)
	billing, ok := a.billings[credential.ID]
	if !ok {
		billing = account.Billing{PlanName: "SuperGrok", MonthlyLimit: 15000}
	}
	billing.AccountID = credential.ID
	if billing.SyncedAt.IsZero() {
		billing.SyncedAt = time.Now().UTC()
	}
	return billing, nil
}

func (a *videoFailoverAdapter) RefreshCredential(_ context.Context, credential account.Credential) (provider.RefreshedCredential, error) {
	a.refreshCalls++
	return provider.RefreshedCredential{
		EncryptedAccessToken:  fmt.Sprintf("refreshed-access-%d", a.refreshCalls),
		EncryptedRefreshToken: credential.EncryptedRefreshToken,
		ExpiresAt:             time.Now().UTC().Add(time.Hour),
	}, nil
}

type videoTestHTTPError int

func (e videoTestHTTPError) Error() string       { return fmt.Sprintf("upstream status %d", int(e)) }
func (e videoTestHTTPError) HTTPStatusCode() int { return int(e) }

func newVideoFailoverTestService(t *testing.T, adapter *videoFailoverAdapter, attempts, accountCount int) (*Service, *relational.AccountRepository, *videoUsageRepository, []account.Credential) {
	t.Helper()
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, t.TempDir()+"/video-failover.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accountRepo := relational.NewAccountRepository(database)
	credentials := make([]account.Credential, 0, accountCount)
	for index := 0; index < accountCount; index++ {
		credential, _, upsertErr := accountRepo.UpsertByIdentity(ctx, account.Credential{
			Provider: account.ProviderBuild, AuthType: account.AuthTypeOAuth,
			Name: fmt.Sprintf("video-account-%d", index+1), SourceKey: fmt.Sprintf("video-source-%d", index+1),
			EncryptedAccessToken: fmt.Sprintf("access-%d", index+1), EncryptedRefreshToken: fmt.Sprintf("refresh-%d", index+1),
			ExpiresAt: time.Now().UTC().Add(time.Hour), Enabled: true, AuthStatus: account.AuthStatusActive,
			Priority: 200 - index*50, MaxConcurrent: 1,
		})
		if upsertErr != nil {
			t.Fatal(upsertErr)
		}
		credentials = append(credentials, credential)
	}
	registry := provider.NewRegistry(adapter)
	sticky := memory.NewStickyStore()
	accountService := accountapp.NewService(accountRepo, nil, memory.NewDeviceSessionStore(), sticky, registry, testCipher(t), nil)
	selector := NewSelector(accountRepo, memory.NewConcurrencyLimiter(), sticky, registry, time.Hour, time.Second, time.Minute)
	jobs := &videoUsageRepository{}
	service := &Service{
		accounts: accountService, providers: registry, selector: selector, mediaJobs: jobs,
		logger: slog.Default(), modelSyncing: make(map[uint64]struct{}),
	}
	service.UpdateMaxAttempts(attempts)
	return service, accountRepo, jobs, credentials
}

func testVideoFailoverJob(credential account.Credential) media.Job {
	now := time.Now().UTC()
	return media.Job{
		ID: "video_failover", AccountID: credential.ID, AccountName: credential.Name,
		Provider: string(account.ProviderBuild), Model: "grok-imagine-video-1.5", UpstreamModel: "Build/grok-imagine-video-1.5",
		Prompt: "test", Seconds: 6, Size: "16:9", Quality: "720p", Status: media.StatusInProgress,
		InputJSON: `{}`, CreatedAt: now, UpdatedAt: now,
	}
}

func testBuildVideoRoute() model.Route {
	return model.Route{Provider: account.ProviderBuild, UpstreamModel: "grok-imagine-video-1.5"}
}

type videoPersistAdapter struct {
	failures         int
	generateCalls    int
	downloadCalls    int
	lastCredentialID uint64
}

func (a *videoPersistAdapter) Provider() account.Provider { return account.ProviderWeb }

func (a *videoPersistAdapter) GenerateVideo(context.Context, provider.VideoRequest) (provider.VideoResult, error) {
	a.generateCalls++
	return provider.VideoResult{}, errors.New("must not regenerate")
}

func (a *videoPersistAdapter) DownloadVideo(_ context.Context, credential account.Credential, _ string) (io.ReadCloser, string, int64, error) {
	a.downloadCalls++
	a.lastCredentialID = credential.ID
	if a.downloadCalls <= a.failures {
		return nil, "", 0, errors.New("temporary download failure")
	}
	return io.NopCloser(strings.NewReader("video")), "video/mp4", 5, nil
}

type videoAssetStoreStub struct{ saveCalls int }

func (s *videoAssetStoreStub) SaveVideo(_ context.Context, jobID, contentType string, body io.Reader) (media.Asset, error) {
	s.saveCalls++
	if jobID != "video_job" {
		return media.Asset{}, fmt.Errorf("job ID = %s", jobID)
	}
	if contentType != "video/mp4" {
		return media.Asset{}, fmt.Errorf("content type = %s", contentType)
	}
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "video" {
		return media.Asset{}, fmt.Errorf("video body = %q: %w", data, err)
	}
	return media.Asset{ID: "vid_local", Kind: "video", MIMEType: "video/mp4", SizeBytes: int64(len(data))}, nil
}

func (*videoAssetStoreStub) OpenVideo(context.Context, string) (media.Asset, io.ReadCloser, error) {
	return media.Asset{}, nil, errors.New("not implemented")
}

type durableVideoAuditRecorder struct {
	failures int
	calls    int
	last     audit.Record
}

func (r *durableVideoAuditRecorder) Create(context.Context, audit.Record) error { return nil }

func (r *durableVideoAuditRecorder) CreateDurable(_ context.Context, value audit.Record) error {
	r.calls++
	r.last = value
	if r.calls <= r.failures {
		return errors.New("database unavailable")
	}
	return nil
}

type videoUsageRepository struct{ job media.Job }

func (r *videoUsageRepository) CreateMediaJob(context.Context, media.Job) error { return nil }

func (r *videoUsageRepository) GetMediaJob(context.Context, string, uint64) (media.Job, error) {
	return r.job, nil
}

func (r *videoUsageRepository) GetMediaJobsByIDs(context.Context, []string) ([]media.Job, error) {
	return []media.Job{r.job}, nil
}

func (r *videoUsageRepository) UpdateMediaJob(_ context.Context, value media.Job) error {
	r.job = value
	return nil
}

func (r *videoUsageRepository) DeleteMediaJob(context.Context, string) error { return nil }

func (r *videoUsageRepository) ListMediaJobs(context.Context, repository.MediaJobListQuery) ([]media.Job, int64, error) {
	return nil, 0, nil
}

func (r *videoUsageRepository) SummarizeMediaJobs(context.Context) (repository.MediaJobStats, error) {
	return repository.MediaJobStats{}, nil
}

func (r *videoUsageRepository) ListRecoverableMediaJobs(context.Context, int) ([]media.Job, error) {
	return nil, nil
}

func (r *videoUsageRepository) ListUnrecordedTerminalMediaJobs(context.Context, int) ([]media.Job, error) {
	if r.job.UsageRecordedAt != nil || (r.job.Status != media.StatusCompleted && r.job.Status != media.StatusFailed) {
		return nil, nil
	}
	return []media.Job{r.job}, nil
}

func (r *videoUsageRepository) TryClaimMediaJob(context.Context, string, time.Time, time.Time, string) (media.Job, bool, error) {
	return media.Job{}, false, nil
}

func (r *videoUsageRepository) MarkMediaJobUsageRecorded(_ context.Context, _ string, recordedAt time.Time) error {
	r.job.UsageRecordedAt = &recordedAt
	return nil
}
