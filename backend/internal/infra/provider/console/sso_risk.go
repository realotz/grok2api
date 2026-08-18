package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/provider/ssorisk"
)

// InspectSSORisk reads grok.com botFlagSource for a Console SSO account.
// The mark is SSO-specific and never written during token refresh.
func (a *Adapter) InspectSSORisk(ctx context.Context, credential account.Credential) (provider.SSOAccountRisk, error) {
	if credential.Provider != account.ProviderConsole || credential.AuthType != account.AuthTypeSSO {
		return provider.SSOAccountRisk{}, fmt.Errorf("仅 Grok Console SSO 账号支持风控检测")
	}
	state, err := ssorisk.Inspect(ctx, a.config().SessionBaseURL, credential, a.egress, a.cipher)
	return toSSOAccountRisk(state, err)
}

func toSSOAccountRisk(state ssorisk.AccountState, err error) (provider.SSOAccountRisk, error) {
	result := provider.SSOAccountRisk{
		Inspected:  state.Found,
		Flagged:    ssorisk.Classify(state) == ssorisk.VerdictFlagged,
		Source:     ssorisk.FlagSource(state),
		Details:    state.Details,
		Policy:     state.Policy,
		StatusCode: state.StatusCode,
		Error:      state.Error,
	}
	if errors.Is(err, provider.ErrUnauthorized) {
		result.Unauthorized = true
		return result, err
	}
	if err != nil {
		if result.Error == "" {
			result.Error = err.Error()
		}
		return result, err
	}
	return result, nil
}

var _ provider.SSORiskAdapter = (*Adapter)(nil)
