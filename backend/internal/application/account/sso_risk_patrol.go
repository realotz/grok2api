package account

import (
	"context"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
)

const (
	ssoRiskPatrolStartupDelay = 2 * time.Minute
	ssoRiskPatrolInterval     = 30 * time.Minute
	ssoRiskPatrolRunTimeout   = 25 * time.Minute
	ssoRiskPatrolLockTTL      = 20 * time.Minute
	ssoRiskPatrolLockKey      = "account-sso-risk-patrol"
	ssoRiskPatrolConcurrency  = 6
)

// RunSSORiskPatrol silently inspects grok.com SSO risk for enabled Web/Console
// accounts. Current snapshots can fall; historical risk-ever never clears.
func (s *Service) RunSSORiskPatrol(ctx context.Context) {
	timer := time.NewTimer(ssoRiskPatrolStartupDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.runSSORiskPatrol(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("sso_risk_patrol_failed", "error", err)
			}
			resetCredentialRefreshTimer(timer, ssoRiskPatrolInterval)
		}
	}
}

func (s *Service) runSSORiskPatrol(ctx context.Context) error {
	runCtx, cancel := context.WithTimeout(ctx, ssoRiskPatrolRunTimeout)
	defer cancel()
	if s.refreshLock != nil {
		release, acquired, err := s.refreshLock.Acquire(runCtx, ssoRiskPatrolLockKey, ssoRiskPatrolLockTTL)
		if err != nil {
			return err
		}
		if !acquired {
			return nil
		}
		defer release()
	}
	pool := s.patrolPool
	if pool == nil {
		pool = s.detectPool
	}
	for _, providerValue := range []accountdomain.Provider{accountdomain.ProviderWeb, accountdomain.ProviderConsole} {
		if s.providers == nil {
			continue
		}
		if _, ok := s.providers.SSORisk(providerValue); !ok {
			continue
		}
		succeeded, failed, err := s.detectSSOAccounts(runCtx, providerValue, nil, true, pool, "sso_risk_patrol", nil, nil)
		if err != nil && runCtx.Err() == nil {
			s.logger.Warn("sso_risk_patrol_provider_incomplete", "provider", providerValue, "succeeded", succeeded, "failed", failed, "error", err)
			continue
		}
		s.logger.Info("sso_risk_patrol_provider_completed", "provider", providerValue, "succeeded", succeeded, "failed", failed)
	}
	return runCtx.Err()
}
