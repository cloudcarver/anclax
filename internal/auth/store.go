package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/config"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/model/querier"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

var (
	algEd25519 = "EdDSA"
	ktyEd25519 = "Ed25519"
)

type Key struct {
	ID   string
	Pub  ed25519.PublicKey
	Priv ed25519.PrivateKey
}

type AuthStore struct {
	m      model.ModelInterface
	now    func() time.Time
	expiry time.Duration
}

func NewAuthStore(m model.ModelInterface, cfg *config.Config) (AuthStoreInterface, error) {
	if cfg.Auth.AccessExpiry == nil {
		return nil, errors.New("token expiry is not set")
	}

	return &AuthStore{
		m:      m,
		now:    time.Now,
		expiry: *cfg.Auth.AccessExpiry,
	}, nil
}

func generateEd25519KeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	return pub, priv, nil
}

func (s *AuthStore) GenerateKeys(ctx context.Context) (*apigen.JWKS, error) {
	pub, priv, err := generateEd25519KeyPair()
	if err != nil {
		return nil, err
	}

	_, err = s.m.StoreKey(ctx, querier.StoreKeyParams{
		PublicKey:  base64.RawURLEncoding.EncodeToString(pub),
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv),
		ExpiredAt:  s.now().Add(s.expiry),
	})

	if err != nil {
		return nil, err
	}

	return s.GetKeys(ctx)
}

func (s *AuthStore) GetKeys(ctx context.Context) (*apigen.JWKS, error) {
	jwks, err := s.m.GetKeys(ctx)
	if err != nil {
		return nil, err
	}

	ret := &apigen.JWKS{
		Keys: make([]apigen.JWK, len(jwks)),
	}

	for i, key := range jwks {
		ret.Keys[i] = keyToAPI(key)
	}

	return ret, nil
}

func (s *AuthStore) GetLatestKey(ctx context.Context) (*Key, error) {
	key, err := s.m.GetLatestKey(ctx)
	if err != nil {
		return nil, err
	}

	return parseKey(key)
}

func (s *AuthStore) GetKeyByID(ctx context.Context, id string) (*Key, error) {
	kidUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid key ID format: %v", err)
	}

	key, err := s.m.GetKeyByID(ctx, kidUUID)
	if err != nil {
		return nil, err
	}

	return parseKey(key)
}

func parseKey(key *querier.Key) (*Key, error) {
	pub, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid public key format: %v", err)
	}

	priv, err := base64.RawURLEncoding.DecodeString(key.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid private key format: %v", err)
	}

	return &Key{
		ID:   key.ID.String(),
		Pub:  pub,
		Priv: priv,
	}, nil
}

func keyToAPI(key *querier.Key) apigen.JWK {
	return apigen.JWK{
		Kty: ktyEd25519,
		Kid: key.ID.String(),
		Exp: key.ExpiredAt,
		Use: "sig",
		Alg: algEd25519,
		X:   key.PublicKey,
	}
}
