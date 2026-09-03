package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
	"github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	mediadomain "github.com/chenyme/grok2api/backend/internal/domain/media"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/pkg/requestmeta"
)

// ImageGenerationInput 表示图片生成用例已经完成协议校验后的输入。
type ImageGenerationInput struct {
	RequestID      string
	ClientKey      clientkey.Key
	PublicModel    string
	Prompt         string
	Count          int
	Size           string
	AspectRatio    string
	Resolution     string
	Quality        string
	ResponseFormat string
	Streaming      bool
	PartialImages  int
	Method         string
	Path           string
	Headers        map[string][]string
}

// ImageEditInput 表示图片编辑用例已经完成协议校验后的输入。
type ImageEditInput struct {
	RequestID        string
	ClientKey        clientkey.Key
	PublicModel      string
	Prompt           string
	ImageURLs        []string
	Count            int
	Size             string
	AspectRatio      string
	Resolution       string
	Quality          string
	ResponseFormat   string
	Streaming        bool
	PartialImages    int
	SelectionRegions []provider.ImageSelectionRegion
	Method           string
	Path             string
	Headers          map[string][]string
}

// ImageLayerInput 表示图片分层用例已经完成协议校验后的输入。
type ImageLayerInput struct {
	RequestID   string
	ClientKey   clientkey.Key
	PublicModel string
	ImageURLs   []string
}

type imageProviderSupport func(accountdomain.Provider) bool

type imageExecution func(context.Context, accountdomain.Provider, accountdomain.Credential, string) (*provider.Response, error)

type imageAssetReader interface {
	OpenImage(ctx context.Context, id string) (mediadomain.Asset, io.ReadCloser, error)
	PublicImageURL(id string) string
}

// GenerateImage 选择支持图片生成的路由和账号，并返回可统一审计的上游响应。
func (s *Service) GenerateImage(ctx context.Context, input ImageGenerationInput) (*Result, error) {
	return s.executeImage(ctx, input.RequestID, input.ClientKey, input.PublicModel, audit.OperationImage, modeldomain.CapabilityImage, func(providerValue accountdomain.Provider) bool {
		_, ok := s.providers.ImageGeneration(providerValue)
		return ok
	}, func(executionCtx context.Context, providerValue accountdomain.Provider, credential accountdomain.Credential, upstream string) (*provider.Response, error) {
		adapter, ok := s.providers.ImageGeneration(providerValue)
		if !ok {
			return nil, ErrNoAvailableAccount
		}
		return adapter.GenerateImage(executionCtx, provider.ImageGenerationRequest{
			Credential: credential, Model: upstream, Prompt: input.Prompt, Count: input.Count,
			Size: input.Size, AspectRatio: input.AspectRatio, Resolution: input.Resolution, Quality: input.Quality,
			ResponseFormat: input.ResponseFormat, Streaming: input.Streaming, PartialImages: input.PartialImages,
		})
	}, input.Streaming, input.Resolution, input.Quality, input.Count, 0, input.Method, input.Path, input.Headers)
}

// EditImage 选择支持图片编辑的路由和账号，并返回可统一审计的上游响应。
func (s *Service) EditImage(ctx context.Context, input ImageEditInput) (*Result, error) {
	imageURLs, err := s.materializeLocalImageInputs(ctx, input.ImageURLs)
	if err != nil {
		return nil, err
	}
	selectionRegions := cloneImageSelectionRegions(input.SelectionRegions)
	return s.executeImage(ctx, input.RequestID, input.ClientKey, input.PublicModel, audit.OperationImageEdit, modeldomain.CapabilityImageEdit, func(providerValue accountdomain.Provider) bool {
		_, ok := s.providers.ImageEdit(providerValue)
		return ok
	}, func(executionCtx context.Context, providerValue accountdomain.Provider, credential accountdomain.Credential, upstream string) (*provider.Response, error) {
		adapter, ok := s.providers.ImageEdit(providerValue)
		if !ok {
			return nil, ErrNoAvailableAccount
		}
		return adapter.EditImage(executionCtx, provider.ImageEditRequest{
			Credential: credential, PublicModel: input.PublicModel, Model: upstream, Prompt: input.Prompt,
			ImageURLs: imageURLs, Count: input.Count, Size: input.Size, AspectRatio: input.AspectRatio,
			Resolution: input.Resolution, Quality: input.Quality, ResponseFormat: input.ResponseFormat,
			Streaming: input.Streaming, PartialImages: input.PartialImages,
			SelectionRegions: selectionRegions,
		})
	}, input.Streaming, input.Resolution, input.Quality, input.Count, len(input.ImageURLs), input.Method, input.Path, input.Headers)
}

// DetectImageLayers 将原图上传到 Grok Web 后返回 Imagine 分区 segmentation。
func (s *Service) DetectImageLayers(ctx context.Context, input ImageLayerInput) (*Result, error) {
	imageURLs, err := s.materializeLocalImageInputs(ctx, input.ImageURLs)
	if err != nil {
		return nil, err
	}
	supports := func(providerValue accountdomain.Provider) bool {
		_, ok := s.providers.ImageLayer(providerValue)
		return ok
	}
	capability, err := s.imageLayerCapability(ctx, input.ClientKey, input.PublicModel, supports)
	if err != nil {
		return nil, err
	}
	return s.executeImage(ctx, input.RequestID, input.ClientKey, input.PublicModel, audit.OperationImageLayer, capability, supports, func(executionCtx context.Context, providerValue accountdomain.Provider, credential accountdomain.Credential, upstream string) (*provider.Response, error) {
		adapter, ok := s.providers.ImageLayer(providerValue)
		if !ok {
			return nil, ErrNoAvailableAccount
		}
		return adapter.DetectImageLayers(executionCtx, provider.ImageLayerRequest{
			Credential: credential, Model: upstream, ImageURLs: imageURLs,
		})
	}, false, "", "", 0, len(imageURLs), "", "", nil)
}

func (s *Service) imageLayerCapability(ctx context.Context, key clientkey.Key, publicModel string, supports imageProviderSupport) (modeldomain.Capability, error) {
	routes, err := s.models.GetByPublicIDCandidates(ctx, publicModel)
	if err != nil {
		return "", ErrModelNotFound
	}
	var lastErr error
	for _, capability := range []modeldomain.Capability{modeldomain.CapabilityImageEdit, modeldomain.CapabilityImage} {
		eligible, _, routeErr := s.eligibleMediaRoutes(routes, key, capability, supports)
		if routeErr == nil && len(eligible) > 0 {
			return capability, nil
		}
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		lastErr = ErrNoAvailableAccount
	}
	if lastErr == nil {
		return "", ErrModelNotFound
	}
	return "", lastErr
}

// cloneImageSelectionRegions 固化账号无关的归一化选区，换号重试时只重新上传原图。
func cloneImageSelectionRegions(values []provider.ImageSelectionRegion) []provider.ImageSelectionRegion {
	result := make([]provider.ImageSelectionRegion, len(values))
	for index, value := range values {
		result[index].Points = append([]float64(nil), value.Points...)
	}
	return result
}

// materializeLocalImageInputs 将本服务返回的媒体 URL 还原为可直接上传给上游的图片数据。
func (s *Service) materializeLocalImageInputs(ctx context.Context, values []string) ([]string, error) {
	result := append([]string(nil), values...)
	reader, ok := s.mediaAssets.(imageAssetReader)
	if !ok {
		return result, nil
	}
	for index, raw := range result {
		assetID, local := localImageAssetID(raw, reader)
		if !local {
			continue
		}
		asset, body, err := reader.OpenImage(ctx, assetID)
		if err != nil {
			return nil, fmt.Errorf("读取本地编辑图片 %s: %w", assetID, err)
		}
		data, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取本地编辑图片 %s: %w", assetID, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("关闭本地编辑图片 %s: %w", assetID, closeErr)
		}
		result[index] = "data:" + asset.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	return result, nil
}

func localImageAssetID(raw string, reader imageAssetReader) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	const prefix = "/v1/media/images/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return "", false
	}
	assetID := strings.TrimPrefix(parsed.Path, prefix)
	if assetID == "" || strings.Contains(assetID, "/") {
		return "", false
	}
	if parsed.IsAbs() && strings.TrimSuffix(reader.PublicImageURL(assetID), "/") != strings.TrimSuffix(trimmed, "/") {
		return "", false
	}
	return assetID, true
}

func (s *Service) executeImage(
	ctx context.Context,
	requestID string,
	key clientkey.Key,
	publicModel string,
	operation audit.Operation,
	capability modeldomain.Capability,
	supports imageProviderSupport,
	execute imageExecution,
	streaming bool,
	resolution string,
	quality string,
	requestedCount int,
	inputImageCount int,
	method string,
	path string,
	headers map[string][]string,
) (*Result, error) {
	ctx, egressTrace := infraegress.WithTrace(ctx)
	startedAt := time.Now()
	eventID := newAuditEventID()
	routes, err := s.models.GetByPublicIDCandidates(ctx, publicModel)
	if err != nil {
		return nil, ErrModelNotFound
	}
	consumesQuota := operation != audit.OperationImageLayer
	route, preselectedSession, err := s.selectSchedulableMediaRoute(ctx, routes, key, capability, consumesQuota, supports)
	if err != nil {
		// Preserve the established failure-audit path when every eligible target
		// is currently unschedulable. The request loop will reproduce the
		// selection error for the representative route after auditBase exists.
		route, err = s.selectMediaRoute(routes, key, capability, supports)
		if err != nil {
			return nil, err
		}
		preselectedSession = nil
	}
	externalModel := modeldomain.ExternalPublicID(route.Provider, route.PublicID)
	guardFastResult := isFastWebImage20Route(route, operation)
	auditBase := audit.Record{
		EventID: eventID, RequestID: requestID, ClientKeyID: key.ID, ClientKeyName: key.Name,
		ClientIP:     requestmeta.ClientIP(ctx),
		ModelRouteID: route.ID, ModelPublicID: externalModel, ModelUpstreamModel: modeldomain.DisplayUpstreamModel(route.Provider, route.UpstreamModel),
		Provider: string(route.Provider), Operation: operation, UsageSource: audit.UsageSourceNone, Streaming: streaming,
		RequestMethod: method, RequestPath: path, RequestHeaders: headers,
	}
	if operation == audit.OperationImageEdit || operation == audit.OperationImageLayer {
		auditBase.MediaInputImages = int64(max(0, inputImageCount))
	}
	if err := s.checkLedgerReady(); err != nil {
		return nil, err
	}
	writeFailureAudit := func(statusCode int, errorCode string, credential *accountdomain.Credential) {
		record := auditBase
		record.StatusCode = statusCode
		record.ErrorCode = errorCode
		record.DurationMS = time.Since(startedAt).Milliseconds()
		record.CreatedAt = time.Now().UTC()
		if credential != nil {
			accountID := credential.ID
			record.AccountID = &accountID
			record.AccountName = credential.Name
		}
		applyAuditEgress(&record, egressTrace, route.Provider)
		persistCtx, cancel := context.WithTimeout(context.Background(), finalizationTimeout)
		defer cancel()
		if auditErr := s.audits.Create(persistCtx, record); auditErr != nil {
			s.logger.Error("request_usage_write_failed", "event_id", record.EventID, "request_id", requestID, "error", auditErr)
		}
	}
	pricingModel := s.providers.PricingModel(route.Provider, route.UpstreamModel)
	pricingResolution, pricingQuality := resolution, quality
	if route.Provider == accountdomain.ProviderWeb && operation == audit.OperationImage {
		// Grok Web Imagine selects the product through the catalog model and
		// only forwards aspect_ratio/n. Do not reserve or record a price tier
		// derived from Console-only resolution/quality compatibility fields.
		pricingResolution, pricingQuality = "", ""
	}
	var reservation audit.PricingResult
	var priced bool
	switch operation {
	case audit.OperationImage:
		reservation, priced = audit.EstimateOfficialImageCost(pricingModel, pricingResolution, pricingQuality, requestedCount)
	case audit.OperationImageEdit:
		reservation, priced = audit.EstimateOfficialImageEditCost(pricingModel, pricingResolution, pricingQuality, requestedCount, inputImageCount)
	}
	reserved := false
	if priced {
		reserved, err = s.clientKeys.ReserveBilling(ctx, key, eventID, reservation.CostInUSDTicks, mediaBillingReservationTTL)
		if err != nil {
			return nil, err
		}
	}
	finalizationOwnsReservation := false
	defer func() {
		if reserved && !finalizationOwnsReservation {
			s.cancelBillingReservation(eventID)
		}
	}()
	quotaMode := s.providers.QuotaMode(route.Provider, route.UpstreamModel)
	quotaRefreshGroup := s.providers.QuotaRefreshGroup(route.Provider, route.UpstreamModel)
	attemptPolicy := newRoutingAttemptPolicy(int(s.maxAttempts.Load()))
	excluded := make(map[uint64]bool)
	selection := preselectedSession
	var lease *accountLease
	var credential accountdomain.Credential
	var response *provider.Response
	var lastCredentialFailure *accountdomain.Credential
	var lastCredentialError error
	for attempt := 0; attemptPolicy.allows(attempt); attempt++ {
		attemptStarted := time.Now()
		for {
			if selection == nil {
				selection, err = s.selector.beginSelectionSessionForKey(ctx, route.Provider, route.ID, route.UpstreamModel, quotaMode, "", excluded, false, key.AccountScope())
			}
			if err == nil {
				lease, err = selection.Acquire(ctx, excluded, false)
			}
			if err != nil || !guardFastResult || !s.selector.credentialBlockedByGuardedWebImageRisk(lease.Credential) {
				break
			}
			excluded[lease.Credential.ID] = true
			lease.Release()
			lease = nil
		}
		if err != nil {
			errorCode := "upstream_unavailable"
			var selectionFailure *SelectionUnavailableError
			if errors.As(err, &selectionFailure) {
				errorCode = selectionFailure.Code()
			}
			writeFailureAudit(http.StatusServiceUnavailable, errorCode, lastCredentialFailure)
			return nil, fmt.Errorf("%w: %w", ErrNoAvailableAccount, err)
		}
		excluded[lease.Credential.ID] = true
		credential, err = s.accounts.EnsureCredential(ctx, lease.Credential, false)
		if err != nil {
			s.logger.Error("image_credential_failed", "event_id", eventID, "request_id", requestID, "model", externalModel, "provider", route.Provider, "account_id", lease.Credential.ID, "error", err)
			failedCredential := lease.Credential
			lastCredentialFailure = &failedCredential
			lastCredentialError = err
			lease.Release()
			continue
		}
		lease.markSelectorUpstreamStarted()
		response, err = execute(ctx, route.Provider, credential, route.UpstreamModel)
		if err != nil {
			s.logger.Error("image_upstream_failed", "event_id", eventID, "request_id", requestID, "model", externalModel, "provider", route.Provider, "account_id", credential.ID, "error", err)
			if isSSOCredentialRejected(err, credential) {
				s.markSSOCredentialRejected(ctx, credential, fmt.Sprintf("%s SSO credential rejected", credential.Provider))
				failedCredential := credential
				lastCredentialFailure = &failedCredential
				lastCredentialError = provider.ErrUnauthorized
				lease.Release()
				continue
			}
			if !provider.IsMediaPostProcessingError(err) {
				s.selector.MarkFailure(ctx, credential, 0, 0)
			}
			lease.Release()
			errorCode := "upstream_unavailable"
			if provider.IsMediaPostProcessingError(err) {
				errorCode = "media_postprocessing_failed"
			}
			writeFailureAudit(http.StatusBadGateway, errorCode, &credential)
			return nil, err
		}
		if response.StatusCode == http.StatusUnauthorized && credential.AuthType == accountdomain.AuthTypeSSO {
			_, _ = readRetryableBody(response.Body)
			s.markSSOCredentialRejected(ctx, credential, fmt.Sprintf("%s SSO credential rejected", credential.Provider))
			failedCredential := credential
			lastCredentialFailure = &failedCredential
			lastCredentialError = provider.ErrUnauthorized
			response = nil
			lease.Release()
			continue
		}
		if s.providers.RetryForbiddenAsEgress(credential.Provider) && response.StatusCode == http.StatusForbidden && attempt == 0 && attemptPolicy.hasNext(attempt) {
			_, _ = readRetryableBody(response.Body)
			delete(excluded, credential.ID)
			if selection != nil {
				selection.RetryAccount(credential.ID)
			}
			lease.Release()
			continue
		}
		if quotaKind, _ := s.providers.QuotaKind(credential.Provider); quotaKind == provider.QuotaRemoteWindow && response.StatusCode == http.StatusTooManyRequests && lease.QuotaMode != "" {
			retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
			exhausted, reconcileErr := s.accounts.ReconcileWebRateLimit(ctx, credential.ID, lease.QuotaMode, retryAfter)
			s.selector.MarkQuotaStateChanged(credential.Provider, credential.ID)
			if reconcileErr != nil || !exhausted {
				s.selector.MarkFailure(ctx, credential, response.StatusCode, retryAfter)
			}
			if attemptPolicy.hasNext(attempt) {
				_, _ = readRetryableBody(response.Body)
				lease.Release()
				continue
			}
		}
		if guardFastResult && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			var fastRisk bool
			response.Body, fastRisk, err = holdImageRiskResponse(ctx, response.Body, streaming, attemptStarted, s.imageRiskWindow())
			if err != nil {
				s.logger.Error("image_risk_hold_failed", "event_id", eventID, "request_id", requestID, "model", externalModel, "provider", route.Provider, "account_id", credential.ID, "error", err)
				if !provider.IsMediaPostProcessingError(err) {
					s.selector.MarkFailure(ctx, credential, 0, 0)
				}
				lease.Release()
				errorCode := "upstream_unavailable"
				if provider.IsMediaPostProcessingError(err) {
					errorCode = "media_postprocessing_failed"
				}
				writeFailureAudit(http.StatusBadGateway, errorCode, &credential)
				return nil, err
			}
			if fastRisk {
				if response.Body != nil {
					_ = response.Body.Close()
				}
				elapsed := time.Since(attemptStarted)
				s.logger.Warn("image_risk_scenery_detected", "event_id", eventID, "request_id", requestID, "model", externalModel, "account_id", credential.ID, "elapsed_ms", elapsed.Milliseconds())
				markCtx, markCancel := context.WithTimeout(context.Background(), accountStateWriteTimeout)
				markErr := s.accounts.MarkAccountMediaRisk(markCtx, credential, "image_scenery")
				markCancel()
				if markErr != nil {
					s.logger.Error("image_risk_mark_failed", "event_id", eventID, "request_id", requestID, "account_id", credential.ID, "error", markErr)
				} else {
					s.selector.evictCandidate(credential.Provider, credential.ID)
				}
				failedCredential := credential
				lastCredentialFailure = &failedCredential
				lastCredentialError = provider.ErrImageRiskScenery
				response = nil
				lease.Release()
				continue
			}
		}
		break
	}
	if response == nil {
		writeFailureAudit(http.StatusServiceUnavailable, "upstream_unavailable", lastCredentialFailure)
		if lastCredentialError == nil {
			lastCredentialError = ErrNoAvailableAccount
		}
		return nil, fmt.Errorf("%w: %w", ErrNoAvailableAccount, lastCredentialError)
	}
	effectiveQuotaMode := lease.QuotaMode
	accountID := credential.ID
	var once sync.Once
	finalize := func(_ Usage, _ string, errorCode string) {
		once.Do(func() {
			successful := auditRequestSucceeded(response.StatusCode, errorCode)
			lease.completeSelectorObservation(successful)
			lease.Release()
			budget := newFinalizationBudget(string(operation), string(route.Provider))
			record := auditBase
			record.AccountID, record.AccountName, record.StatusCode = &accountID, credential.Name, response.StatusCode
			record.ErrorCode = errorCode
			record.DurationMS, record.CreatedAt = time.Since(startedAt).Milliseconds(), time.Now().UTC()
			applyAuditEgress(&record, egressTrace, route.Provider)
			if successful {
				record.MediaOutputImages = int64(max(0, requestedCount))
				var pricing audit.PricingResult
				var priced bool
				switch operation {
				case audit.OperationImage:
					pricing, priced = audit.EstimateOfficialImageCost(pricingModel, pricingResolution, pricingQuality, requestedCount)
				case audit.OperationImageEdit:
					pricing, priced = audit.EstimateOfficialImageEditCost(pricingModel, pricingResolution, pricingQuality, requestedCount, inputImageCount)
				}
				if priced {
					record.EstimatedCostInUSDTicks = pricing.CostInUSDTicks
					record.PricingModel = pricing.Model
					record.PricingVersion = audit.OfficialPricingAsOf
				}
			}
			quotaKind, _ := s.providers.QuotaKind(route.Provider)
			refreshMode, decrementMode, availabilityMode := quotaFinalizationModes(effectiveQuotaMode, quotaRefreshGroup)
			if successful && operation != audit.OperationImageLayer && quotaKind == provider.QuotaRemoteWindow && refreshMode != "" {
				if decrementMode != "" && decrementMode != "weekly" {
					units := max(1, response.QuotaUnits)
					var updated bool
					err := budget.run("quota_decrement", finalizationQuotaBudget, func(stageCtx context.Context) error {
						var decrementErr error
						updated, decrementErr = s.accounts.DecrementWebQuota(stageCtx, accountID, decrementMode, units)
						return decrementErr
					})
					if err != nil {
						s.logger.Warn("web_quota_decrement_failed", "account_id", accountID, "mode", decrementMode, "units", units, "error", err)
					} else if updated {
						s.selector.ConsumeQuota(route.Provider, accountID, decrementMode, units)
					}
				}
				s.accounts.QueueQuotaRefresh(accountID, refreshMode)
				if availabilityMode != "" && availabilityMode != refreshMode {
					s.accounts.QueueQuotaRefresh(accountID, availabilityMode)
				}
			}
			if err := budget.run("audit", finalizationAuditBudget, func(stageCtx context.Context) error {
				return s.audits.Create(stageCtx, record)
			}); err != nil {
				s.logger.Error("request_usage_write_failed", "event_id", record.EventID, "request_id", requestID, "error", err)
			}
		})
	}
	finalizationOwnsReservation = true
	return &Result{StatusCode: response.StatusCode, Status: response.Status, Header: response.Header, Body: &finalizingBody{ReadCloser: response.Body, finalize: func() { finalize(Usage{}, "", "stream_closed") }}, Finalize: finalize}, nil
}

func isFastWebImage20Route(route modeldomain.Route, operation audit.Operation) bool {
	if route.Provider != accountdomain.ProviderWeb || (operation != audit.OperationImage && operation != audit.OperationImageEdit) {
		return false
	}
	return strings.EqualFold(modeldomain.ExternalPublicID(route.Provider, route.PublicID), "grok-imagine-image-2.0-web")
}

func (s *Service) imageRiskWindow() time.Duration {
	if s.imageRiskReadyWithin > 0 {
		return s.imageRiskReadyWithin
	}
	return provider.ImageRiskReadyWithin
}

func holdImageRiskResponse(ctx context.Context, body io.ReadCloser, streaming bool, startedAt time.Time, window time.Duration) (io.ReadCloser, bool, error) {
	elapsed := time.Since(startedAt)
	if !streaming || elapsed < 0 || elapsed >= window {
		return body, imageRiskReadyWithin(elapsed, window), nil
	}
	if body == nil {
		return nil, true, nil
	}

	prefix, err := os.CreateTemp("", "grok2api-image-risk-*")
	if err != nil {
		_ = body.Close()
		return nil, false, fmt.Errorf("创建图片风控流暂存文件: %w", err)
	}
	pump := newQualityReadPump(body)
	cleanup := func() error {
		return closeImageRiskReplay(prefix, pump, prefix.Name())
	}
	timer := time.NewTimer(time.Until(startedAt.Add(window)))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = cleanup()
			return nil, false, ctx.Err()
		case <-timer.C:
			if eofAt := pump.eofAt.Load(); eofAt > 0 && imageRiskReadyWithin(time.Unix(0, eofAt).Sub(startedAt), window) {
				_ = cleanup()
				return nil, true, nil
			}
			if _, err := prefix.Seek(0, io.SeekStart); err != nil {
				_ = cleanup()
				return nil, false, fmt.Errorf("回放图片风控流: %w", err)
			}
			return newImageRiskReplayBody(prefix, pump), false, nil
		case result, ok := <-pump.results:
			if !ok {
				if imageRiskReadyWithin(time.Since(startedAt), window) {
					_ = cleanup()
					return nil, true, nil
				}
				if _, err := prefix.Seek(0, io.SeekStart); err != nil {
					_ = cleanup()
					return nil, false, fmt.Errorf("回放图片风控流: %w", err)
				}
				return newImageRiskReplayBody(prefix, pump), false, nil
			}
			if len(result.data) > 0 {
				if _, err := prefix.Write(result.data); err != nil {
					_ = cleanup()
					return nil, false, fmt.Errorf("暂存图片风控流: %w", err)
				}
			}
			if result.err == nil {
				continue
			}
			if errors.Is(result.err, io.EOF) {
				completedAt := result.at
				if completedAt.IsZero() {
					completedAt = time.Now()
				}
				fastRisk := imageRiskReadyWithin(completedAt.Sub(startedAt), window)
				if fastRisk {
					_ = cleanup()
					return nil, true, nil
				}
				if _, err := prefix.Seek(0, io.SeekStart); err != nil {
					_ = cleanup()
					return nil, false, fmt.Errorf("回放图片风控流: %w", err)
				}
				return newImageRiskReplayBody(prefix, pump), false, nil
			}
			_ = cleanup()
			return nil, false, result.err
		}
	}
}

func imageRiskReadyWithin(elapsed, window time.Duration) bool {
	if window == provider.ImageRiskReadyWithin {
		return provider.IsFastImageRisk(elapsed)
	}
	return elapsed >= 0 && elapsed < window
}

type imageRiskReplayBody struct {
	io.Reader
	prefix    *os.File
	source    io.ReadCloser
	path      string
	closeOnce sync.Once
	closeErr  error
}

func newImageRiskReplayBody(prefix *os.File, source io.ReadCloser) io.ReadCloser {
	return &imageRiskReplayBody{Reader: io.MultiReader(prefix, source), prefix: prefix, source: source, path: prefix.Name()}
}

func (b *imageRiskReplayBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = closeImageRiskReplay(b.prefix, b.source, b.path)
	})
	return b.closeErr
}

func closeImageRiskReplay(prefix *os.File, source io.Closer, path string) error {
	var errs []error
	if source != nil {
		errs = append(errs, source.Close())
	}
	if prefix != nil {
		errs = append(errs, prefix.Close())
	}
	if path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// quotaFinalizationModes separates the immediate local consumption fence from
// the authoritative provider refresh. A refresh group may update several
// upstream windows atomically, while the local fence must charge the exact
// window selected for this account so concurrent media requests cannot
// over-allocate during the short refresh delay.
func quotaFinalizationModes(effectiveMode, refreshGroup string) (refreshMode, decrementMode, availabilityMode string) {
	// Availability-only Imagine products on paid Web tiers are governed by the
	// shared weekly pool. Refresh its numeric counter and also re-read the
	// product group so available=false/nextAvailableAt can install an exact
	// product fence that overrides weekly routing.
	if effectiveMode == "weekly" {
		return effectiveMode, effectiveMode, refreshGroup
	}
	refreshMode = effectiveMode
	if refreshGroup != "" {
		refreshMode = refreshGroup
	}
	return refreshMode, effectiveMode, ""
}
