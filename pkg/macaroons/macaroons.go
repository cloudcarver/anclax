package macaroons

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/cloudcarver/anclax/pkg/macaroons/store"
	"github.com/pkg/errors"
)

var (
	ErrMalformedToken   = errors.New("malformed token")
	ErrInvalidSignature = errors.New("invalid signature")
)

type Macaroon struct {
	Caveats []Caveat `json:"caveats"`

	keyID             int64
	signature         []byte
	encodedToken      string
	encodedTokenNoSig string
}

func (m *Macaroon) StringToken() string {
	return m.encodedToken
}

func (m *Macaroon) KeyID() int64 {
	return m.keyID
}

func (m *Macaroon) AddCaveat(caveat Caveat) error {
	// encode caveat
	encodedCaveat, err := EncodeCaveat(caveat)
	if err != nil {
		return errors.Wrap(err, "failed to encode caveat")
	}

	// calculate the new signature
	sig, err := sign(m.signature, encodedCaveat)
	if err != nil {
		return errors.Wrap(err, "failed to sign")
	}
	encodedSignature := base64.StdEncoding.EncodeToString(sig)

	m.encodedTokenNoSig = m.encodedTokenNoSig + "." + encodedCaveat
	m.encodedToken = m.encodedTokenNoSig + "." + encodedSignature
	m.Caveats = append(m.Caveats, caveat)
	m.signature = sig
	return nil
}

type MacaroonsManager struct {
	keyStore     store.KeyStore
	caveatParser CaveatParserInterface

	randomKey func() ([]byte, error)
}

func NewMacaroonManager(keyStore store.KeyStore, caveatParser CaveatParserInterface) MacaroonManagerInterface {
	return &MacaroonsManager{
		keyStore:     keyStore,
		caveatParser: caveatParser,
		randomKey:    randomKey,
	}
}

func (m *MacaroonsManager) CreateToken(ctx context.Context, caveats []Caveat, ttl time.Duration, userID *int32) (*Macaroon, error) {
	key, err := m.randomKey()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate random key")
	}
	keyID, err := m.keyStore.Create(ctx, key, ttl, userID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get key")
	}

	return CreateMacaroon(keyID, key, caveats)
}

func CreateMacaroon(keyID int64, key []byte, caveats []Caveat) (*Macaroon, error) {
	encodedKeyID := base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(keyID, 10)))
	token := encodedKeyID

	encodedCaveats := make([]string, len(caveats))
	for i, caveat := range caveats {
		encodedCaveat, err := EncodeCaveat(caveat)
		if err != nil {
			return nil, errors.Wrap(err, "failed to encode caveat")
		}
		encodedCaveats[i] = encodedCaveat
		token += "." + encodedCaveat
	}

	signature, err := chainedHmac(key, encodedKeyID, encodedCaveats)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calculate signature")
	}

	encodedSignature := base64.StdEncoding.EncodeToString(signature)
	encodedTokenNoSig := token
	token += "." + encodedSignature

	return &Macaroon{
		keyID:             keyID,
		Caveats:           caveats,
		signature:         signature,
		encodedTokenNoSig: encodedTokenNoSig,
		encodedToken:      token,
	}, nil
}

func (m *MacaroonsManager) Parse(ctx context.Context, token string) (*Macaroon, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errors.Wrap(ErrMalformedToken, "token must contain at least 2 parts")
	}
	encodedKeyID := parts[0]
	encodedCaveats := parts[1 : len(parts)-1]
	encodedSignature := parts[len(parts)-1]

	// decode nounce and keyID
	header, err := base64.StdEncoding.DecodeString(encodedKeyID)
	if err != nil {
		return nil, errors.Wrap(ErrMalformedToken, "failed to decode header")
	}
	keyID, err := strconv.ParseInt(string(header), 10, 64)
	if err != nil {
		return nil, errors.Wrap(ErrMalformedToken, "failed to convert keyID to int")
	}
	key, err := m.keyStore.Get(ctx, keyID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get key")
	}

	// decode signature
	signature, err := base64.StdEncoding.DecodeString(encodedSignature)
	if err != nil {
		return nil, errors.Wrapf(ErrMalformedToken, "failed to decode signature: %s", err.Error())
	}

	// verify signature
	calculatedSignature, err := chainedHmac(key, encodedKeyID, encodedCaveats)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calculate signature")
	}
	if !hmac.Equal(signature, calculatedSignature) {
		return nil, ErrInvalidSignature
	}

	// decode caveats
	caveats := make([]Caveat, len(encodedCaveats))
	for i, part := range encodedCaveats {
		caveat, err := m.caveatParser.Parse(part)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse caveat")
		}
		caveats[i] = caveat
	}

	return &Macaroon{
		keyID:             keyID,
		Caveats:           caveats,
		signature:         signature,
		encodedTokenNoSig: strings.TrimSuffix(token, "."+encodedSignature),
		encodedToken:      token,
	}, nil
}

func (m *MacaroonsManager) InvalidateUserTokens(ctx context.Context, userID int32) error {
	if err := m.keyStore.DeleteUserKeys(ctx, userID); err != nil {
		if errors.Is(err, store.ErrKeyNotFound) {
			return nil
		}
		return errors.Wrap(err, "failed to delete user keys")
	}
	return nil
}

func (m *MacaroonsManager) InvalidateToken(ctx context.Context, keyID int64) error {
	if err := m.keyStore.Delete(ctx, keyID); err != nil {
		if errors.Is(err, store.ErrKeyNotFound) {
			return nil
		}
		return errors.Wrap(err, "failed to delete key")
	}
	return nil
}

func chainedHmac(key []byte, encodedKeyID string, encodedCaveats []string) ([]byte, error) {
	parts := make([]string, len(encodedCaveats)+1)
	parts[0] = encodedKeyID
	copy(parts[1:], encodedCaveats)

	hmacKey := key
	for _, part := range parts {
		sig, err := sign(hmacKey, part)
		if err != nil {
			return nil, errors.Wrap(err, "failed to sign")
		}
		hmacKey = sig
	}
	return hmacKey, nil
}

func randomKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate random key")
	}
	return key, nil
}

func sign(key []byte, content string) ([]byte, error) {
	hash := hmac.New(sha256.New, key)
	if _, err := hash.Write([]byte(content)); err != nil {
		return nil, errors.Wrap(err, "failed to write to hmac")
	}
	return hash.Sum(nil), nil
}
