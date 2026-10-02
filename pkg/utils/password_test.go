package utils

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateSaltAndHashUsesRandomSelfContainedArgon2id(t *testing.T) {
	storedSalt1, hash1, err := GenerateSaltAndHash("correct horse battery staple")
	require.NoError(t, err)
	storedSalt2, hash2, err := GenerateSaltAndHash("correct horse battery staple")
	require.NoError(t, err)

	require.NotEmpty(t, storedSalt1)
	require.NotEmpty(t, storedSalt2)
	require.NotEqual(t, storedSalt1, storedSalt2)
	require.True(t, strings.HasPrefix(hash1, passwordHashPrefix))
	require.True(t, strings.HasPrefix(hash2, passwordHashPrefix))
	require.NotEqual(t, hash1, hash2)

	_, salt1, _, err := parseArgon2idHash(hash1)
	require.NoError(t, err)
	_, salt2, _, err := parseArgon2idHash(hash2)
	require.NoError(t, err)
	require.Len(t, salt1, int(passwordHashSaltLength))
	require.Len(t, salt2, int(passwordHashSaltLength))
	require.NotEqual(t, salt1, salt2)
	require.Equal(t, storedSalt1, base64.RawStdEncoding.EncodeToString(salt1))
	require.Equal(t, storedSalt2, base64.RawStdEncoding.EncodeToString(salt2))

	valid, needsUpgrade, err := VerifyPassword("correct horse battery staple", hash1, "ignored")
	require.NoError(t, err)
	require.True(t, valid)
	require.False(t, needsUpgrade)

	rehashed, err := HashPassword("correct horse battery staple", storedSalt1)
	require.NoError(t, err)
	require.Equal(t, hash1, rehashed)
}

func TestVerifyPasswordRejectsWrongPasswordWithoutUpgrade(t *testing.T) {
	_, hash, err := GenerateSaltAndHash("correct-password")
	require.NoError(t, err)

	valid, needsUpgrade, err := VerifyPassword("wrong-password", hash, "")
	require.NoError(t, err)
	require.False(t, valid)
	require.False(t, needsUpgrade)
}

func TestVerifyPasswordSupportsLegacySHA256(t *testing.T) {
	const (
		password = "legacy-password"
		salt     = "salt-123456"
	)
	digest := sha256.Sum256([]byte(password + "-" + salt))
	legacyHash := hex.EncodeToString(digest[:])

	valid, needsUpgrade, err := VerifyPassword(password, legacyHash, salt)
	require.NoError(t, err)
	require.True(t, valid)
	require.True(t, needsUpgrade)

	valid, needsUpgrade, err = VerifyPassword("wrong-password", legacyHash, salt)
	require.NoError(t, err)
	require.False(t, valid)
	require.False(t, needsUpgrade)
}

func TestVerifyPasswordRejectsMalformedOrExcessiveHashes(t *testing.T) {
	_, validHash, err := GenerateSaltAndHash("password")
	require.NoError(t, err)

	tests := map[string]string{
		"empty legacy hash":        "",
		"non-hex legacy hash":      "not-a-password-hash",
		"unknown format":           "$scrypt$v=1$m=1$abc$def",
		"truncated argon2id":       "$argon2id$v=19$m=19456,t=2,p=1$abc",
		"unsupported version":      strings.Replace(validHash, "$v=19$", "$v=18$", 1),
		"excessive memory":         strings.Replace(validHash, "$m=19456,", "$m=65537,", 1),
		"excessive iterations":     strings.Replace(validHash, ",t=2,", ",t=11,", 1),
		"excessive parallelism":    strings.Replace(validHash, ",p=1$", ",p=5$", 1),
		"excessive encoded length": validHash + strings.Repeat("x", maxEncodedPasswordHashLength),
		"invalid salt encoding":    replaceArgon2idPart(validHash, 4, "!"),
		"invalid digest encoding":  replaceArgon2idPart(validHash, 5, "!"),
	}

	for name, hash := range tests {
		t.Run(name, func(t *testing.T) {
			valid, needsUpgrade, err := VerifyPassword("password", hash, "legacy-salt")
			require.Error(t, err)
			require.False(t, valid)
			require.False(t, needsUpgrade)
		})
	}
}

func replaceArgon2idPart(hash string, index int, replacement string) string {
	parts := strings.Split(hash, "$")
	parts[index] = replacement
	return strings.Join(parts, "$")
}
