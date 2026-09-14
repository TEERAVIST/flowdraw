package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/handler/pkce"
)

const (
	kindCode    = "authorize_code"
	kindAccess  = "access_token"
	kindRefresh = "refresh_token"
	kindPKCE    = "pkce"
	kindOIDC    = "oidc"
)

type OAuthStore struct{ DB *pgxpool.Pool }

var _ fosite.Storage = (*OAuthStore)(nil)
var _ oauth2.CoreStorage = (*OAuthStore)(nil)
var _ oauth2.TokenRevocationStorage = (*OAuthStore)(nil)
var _ pkce.PKCERequestStorage = (*OAuthStore)(nil)
var _ openid.OpenIDConnectRequestStorage = (*OAuthStore)(nil)

type storedRequest struct {
	ID                string                 `json:"id"`
	RequestedAt       time.Time              `json:"requested_at"`
	ClientID          string                 `json:"client_id"`
	RequestedScope    []string               `json:"requested_scope"`
	GrantedScope      []string               `json:"granted_scope"`
	RequestedAudience []string               `json:"requested_audience"`
	GrantedAudience   []string               `json:"granted_audience"`
	Form              url.Values             `json:"form"`
	Session           *openid.DefaultSession `json:"session"`
	AccessSignature   string                 `json:"access_signature,omitempty"`
}

func encodeRequest(request fosite.Requester, accessSignature string) ([]byte, time.Time, error) {
	session, ok := request.GetSession().(*openid.DefaultSession)
	if !ok {
		return nil, time.Time{}, errors.New("unsupported Fosite session type")
	}
	payload, err := json.Marshal(storedRequest{
		ID: request.GetID(), RequestedAt: request.GetRequestedAt(), ClientID: request.GetClient().GetID(),
		RequestedScope: request.GetRequestedScopes(), GrantedScope: request.GetGrantedScopes(),
		RequestedAudience: request.GetRequestedAudience(), GrantedAudience: request.GetGrantedAudience(),
		Form: request.GetRequestForm(), Session: session, AccessSignature: accessSignature,
	})
	expires := session.GetExpiresAt(fosite.RefreshToken)
	if codeExpiry := session.GetExpiresAt(fosite.AuthorizeCode); codeExpiry.After(expires) {
		expires = codeExpiry
	}
	if accessExpiry := session.GetExpiresAt(fosite.AccessToken); accessExpiry.After(expires) {
		expires = accessExpiry
	}
	if expires.IsZero() {
		expires = time.Now().UTC().Add(24 * time.Hour)
	}
	return payload, expires, err
}

func (s *OAuthStore) decodeRequest(ctx context.Context, payload []byte) (fosite.Requester, error) {
	var stored storedRequest
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, err
	}
	client, err := s.GetClient(ctx, stored.ClientID)
	if err != nil {
		return nil, err
	}
	return &fosite.Request{
		ID: stored.ID, RequestedAt: stored.RequestedAt, Client: client,
		RequestedScope: stored.RequestedScope, GrantedScope: stored.GrantedScope,
		RequestedAudience: stored.RequestedAudience, GrantedAudience: stored.GrantedAudience,
		Form: stored.Form, Session: stored.Session,
	}, nil
}

func (s *OAuthStore) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	var client fosite.DefaultClient
	err := s.DB.QueryRow(ctx, `SELECT id, secret_hash, redirect_uris, grant_types, response_types, scopes FROM oauth_clients WHERE id=$1 AND enabled`, id).
		Scan(&client.ID, &client.Secret, &client.RedirectURIs, &client.GrantTypes, &client.ResponseTypes, &client.Scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &fosite.DefaultOpenIDConnectClient{DefaultClient: &client, TokenEndpointAuthMethod: "client_secret_basic"}, nil
}

func (s *OAuthStore) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM oauth_client_jtis WHERE jti=$1 AND expires_at>now())`, jti).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return fosite.ErrJTIKnown
	}
	return nil
}

func (s *OAuthStore) SetClientAssertionJWT(ctx context.Context, jti string, expiresAt time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM oauth_client_jtis WHERE expires_at<=now()`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO oauth_client_jtis(jti,expires_at) VALUES($1,$2)`, jti, expiresAt); err != nil {
		return fosite.ErrJTIKnown
	}
	return tx.Commit(ctx)
}

func (s *OAuthStore) put(ctx context.Context, kind, signature, accessSignature string, request fosite.Requester) error {
	payload, expires, err := encodeRequest(request, accessSignature)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO oauth_sessions(kind,signature,request_id,payload,expires_at) VALUES($1,$2,$3,$4,$5)`, kind, signature, request.GetID(), payload, expires)
	return err
}

func (s *OAuthStore) get(ctx context.Context, kind, signature string, invalidError error) (fosite.Requester, error) {
	var payload []byte
	var active bool
	err := s.DB.QueryRow(ctx, `SELECT payload, active FROM oauth_sessions WHERE kind=$1 AND signature=$2 AND expires_at>now()`, kind, signature).Scan(&payload, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	request, err := s.decodeRequest(ctx, payload)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, invalidError
	}
	return request, nil
}

func (s *OAuthStore) delete(ctx context.Context, kind, signature string) error {
	result, err := s.DB.Exec(ctx, `DELETE FROM oauth_sessions WHERE kind=$1 AND signature=$2`, kind, signature)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fosite.ErrNotFound
	}
	return nil
}

func (s *OAuthStore) CreateAuthorizeCodeSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.put(ctx, kindCode, signature, "", request)
}
func (s *OAuthStore) GetAuthorizeCodeSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(ctx, kindCode, signature, fosite.ErrInvalidatedAuthorizeCode)
}
func (s *OAuthStore) InvalidateAuthorizeCodeSession(ctx context.Context, signature string) error {
	_, err := s.DB.Exec(ctx, `UPDATE oauth_sessions SET active=false WHERE kind=$1 AND signature=$2`, kindCode, signature)
	return err
}
func (s *OAuthStore) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.put(ctx, kindAccess, signature, "", request)
}
func (s *OAuthStore) GetAccessTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(ctx, kindAccess, signature, fosite.ErrInactiveToken)
}
func (s *OAuthStore) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	return s.delete(ctx, kindAccess, signature)
}
func (s *OAuthStore) CreateRefreshTokenSession(ctx context.Context, signature, accessSignature string, request fosite.Requester) error {
	return s.put(ctx, kindRefresh, signature, accessSignature, request)
}
func (s *OAuthStore) GetRefreshTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(ctx, kindRefresh, signature, fosite.ErrInactiveToken)
}
func (s *OAuthStore) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	return s.delete(ctx, kindRefresh, signature)
}
func (s *OAuthStore) RotateRefreshToken(ctx context.Context, requestID, signature string) error {
	_, err := s.DB.Exec(ctx, `UPDATE oauth_sessions SET active=false WHERE kind=$1 AND request_id=$2 AND signature<>$3`, kindRefresh, requestID, signature)
	return err
}
func (s *OAuthStore) RevokeRefreshToken(ctx context.Context, requestID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE oauth_sessions SET active=false WHERE kind=$1 AND request_id=$2`, kindRefresh, requestID)
	return err
}
func (s *OAuthStore) RevokeAccessToken(ctx context.Context, requestID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE oauth_sessions SET active=false WHERE kind=$1 AND request_id=$2`, kindAccess, requestID)
	return err
}
func (s *OAuthStore) CreatePKCERequestSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.put(ctx, kindPKCE, signature, "", request)
}
func (s *OAuthStore) GetPKCERequestSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(ctx, kindPKCE, signature, fosite.ErrNotFound)
}
func (s *OAuthStore) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return s.delete(ctx, kindPKCE, signature)
}
func (s *OAuthStore) CreateOpenIDConnectSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.put(ctx, kindOIDC, signature, "", request)
}
func (s *OAuthStore) GetOpenIDConnectSession(ctx context.Context, signature string, _ fosite.Requester) (fosite.Requester, error) {
	return s.get(ctx, kindOIDC, signature, fosite.ErrNotFound)
}
func (s *OAuthStore) DeleteOpenIDConnectSession(ctx context.Context, signature string) error {
	return s.delete(ctx, kindOIDC, signature)
}
