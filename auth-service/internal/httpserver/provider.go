package httpserver

import (
	"context"
	"github.com/k1n3ticnerdcore/flowdraw/auth-service/internal/storage"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/token/jwt"
	"time"
)

func Provider(store *storage.OAuthStore, issuer string, secret []byte, keys Keys) fosite.OAuth2Provider {
	config := &fosite.Config{
		GlobalSecret: secret, IDTokenIssuer: issuer, AccessTokenIssuer: issuer,
		EnforcePKCE: true, EnforcePKCEForPublicClients: true, EnablePKCEPlainChallengeMethod: false,
		AccessTokenLifespan: 15 * time.Minute, AuthorizeCodeLifespan: 5 * time.Minute,
		IDTokenLifespan: 15 * time.Minute, RefreshTokenLifespan: 30 * 24 * time.Hour,
		RefreshTokenScopes: []string{"offline_access"}, ScopeStrategy: fosite.ExactScopeStrategy,
		SendDebugMessagesToClients: false,
	}
	// Fosite otherwise installs this default lazily in GetSecretsHasher.
	// Initialize it before concurrent HTTP requests share the provider.
	config.ClientSecretsHasher = &fosite.BCrypt{Config: config}
	getter := func(context.Context) (interface{}, error) { return &keys.Active, nil }
	return compose.Compose(config, store, &compose.CommonStrategy{
		CoreStrategy:               compose.NewOAuth2HMACStrategy(config),
		OpenIDConnectTokenStrategy: compose.NewOpenIDConnectStrategy(getter, config),
		Signer:                     &jwt.DefaultSigner{GetPrivateKey: getter},
	}, compose.OAuth2AuthorizeExplicitFactory, compose.OAuth2RefreshTokenGrantFactory,
		compose.OpenIDConnectExplicitFactory, compose.OpenIDConnectRefreshFactory,
		compose.OAuth2TokenIntrospectionFactory, compose.OAuth2TokenRevocationFactory, compose.OAuth2PKCEFactory)
}
