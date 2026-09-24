package utils

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordHashAlgorithm = "argon2id"
	passwordHashPrefix    = "$" + passwordHashAlgorithm + "$"

	// These parameters follow the memory-constrained Argon2id profile recommended
	// by OWASP: 19 MiB of memory, two iterations, and one lane. Keeping one lane
	// bounds the goroutine and CPU amplification caused by each login attempt.
	passwordHashMemory      uint32 = 19 * 1024
	passwordHashIterations  uint32 = 2
	passwordHashParallelism uint8  = 1
	passwordHashSaltLength  uint32 = 16
	passwordHashKeyLength   uint32 = 32

	// Encoded hashes normally come from the database. Enforce conservative upper
	// bounds before calling Argon2 so a corrupt or tampered record cannot request
	// unbounded memory or CPU during authentication.
	maxPasswordHashMemory        uint32 = 64 * 1024
	maxPasswordHashIterations    uint32 = 10
	maxPasswordHashParallelism   uint8  = 4
	maxPasswordHashSaltLength    uint32 = 64
	maxPasswordHashKeyLength     uint32 = 64
	maxEncodedPasswordHashLength        = 512
)

type passwordHashParams struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	saltLength  uint32
	keyLength   uint32
}

var currentPasswordHashParams = passwordHashParams{
	memory:      passwordHashMemory,
	iterations:  passwordHashIterations,
	parallelism: passwordHashParallelism,
	saltLength:  passwordHashSaltLength,
	keyLength:   passwordHashKeyLength,
}

// GenerateSaltAndHash creates a versioned, self-contained Argon2id password
// hash. The separately returned salt is retained for API and schema compatibility;
// verification of the new format relies only on the encoded hash.
func GenerateSaltAndHash(password string) (string, string, error) {
	salt := make([]byte, currentPasswordHashParams.saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", "", fmt.Errorf("generate password salt: %w", err)
	}

	encoded, err := hashPasswordWithSalt(password, salt, currentPasswordHashParams)
	if err != nil {
		return "", "", err
	}
	return base64.RawStdEncoding.EncodeToString(salt), encoded, nil
}

// HashPassword hashes a password with a base64-encoded salt using the current
// Argon2id parameters. Prefer GenerateSaltAndHash for new passwords so the salt
// is produced by crypto/rand.
func HashPassword(password, salt string) (string, error) {
	saltBytes, err := base64.RawStdEncoding.Strict().DecodeString(salt)
	if err != nil {
		return "", fmt.Errorf("decode password salt: %w", err)
	}

	params := currentPasswordHashParams
	params.saltLength = uint32(len(saltBytes))
	return hashPasswordWithSalt(password, saltBytes, params)
}

// VerifyPassword verifies either the current Argon2id format or the legacy
// SHA-256 format. needsUpgrade is true only after a successful verification, so
// an incorrect password can never trigger a password-record rewrite.
func VerifyPassword(password, encodedHash, legacySalt string) (valid, needsUpgrade bool, err error) {
	if len(encodedHash) > maxEncodedPasswordHashLength {
		return false, false, fmt.Errorf("password hash exceeds the allowed length")
	}
	if strings.HasPrefix(encodedHash, passwordHashPrefix) {
		return verifyArgon2idPassword(password, encodedHash)
	}
	if strings.HasPrefix(encodedHash, "$") {
		return false, false, fmt.Errorf("unsupported password hash format")
	}
	return verifyLegacyPassword(password, encodedHash, legacySalt)
}

func hashPasswordWithSalt(password string, salt []byte, params passwordHashParams) (string, error) {
	params.saltLength = uint32(len(salt))
	if err := validatePasswordHashParams(params); err != nil {
		return "", err
	}

	hash := argon2.IDKey(
		[]byte(password),
		salt,
		params.iterations,
		params.memory,
		params.parallelism,
		params.keyLength,
	)

	return fmt.Sprintf(
		"$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		passwordHashAlgorithm,
		argon2.Version,
		params.memory,
		params.iterations,
		params.parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyArgon2idPassword(password, encodedHash string) (bool, bool, error) {
	params, salt, expectedHash, err := parseArgon2idHash(encodedHash)
	if err != nil {
		return false, false, err
	}

	actualHash := argon2.IDKey(
		[]byte(password),
		salt,
		params.iterations,
		params.memory,
		params.parallelism,
		uint32(len(expectedHash)),
	)
	valid := subtle.ConstantTimeCompare(actualHash, expectedHash) == 1
	if !valid {
		return false, false, nil
	}

	needsUpgrade := params.memory != currentPasswordHashParams.memory ||
		params.iterations != currentPasswordHashParams.iterations ||
		params.parallelism != currentPasswordHashParams.parallelism ||
		uint32(len(salt)) != currentPasswordHashParams.saltLength ||
		uint32(len(expectedHash)) != currentPasswordHashParams.keyLength

	return true, needsUpgrade, nil
}

func parseArgon2idHash(encodedHash string) (passwordHashParams, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != passwordHashAlgorithm {
		return passwordHashParams{}, nil, nil, fmt.Errorf("malformed password hash")
	}

	version, err := parseUintParameter(parts[2], "v=", 8)
	if err != nil || version != argon2.Version {
		return passwordHashParams{}, nil, nil, fmt.Errorf("unsupported password hash version")
	}

	paramParts := strings.Split(parts[3], ",")
	if len(paramParts) != 3 {
		return passwordHashParams{}, nil, nil, fmt.Errorf("malformed password hash parameters")
	}
	memory, err := parseUintParameter(paramParts[0], "m=", 32)
	if err != nil {
		return passwordHashParams{}, nil, nil, fmt.Errorf("malformed password hash memory parameter")
	}
	iterations, err := parseUintParameter(paramParts[1], "t=", 32)
	if err != nil {
		return passwordHashParams{}, nil, nil, fmt.Errorf("malformed password hash iteration parameter")
	}
	parallelism, err := parseUintParameter(paramParts[2], "p=", 8)
	if err != nil {
		return passwordHashParams{}, nil, nil, fmt.Errorf("malformed password hash parallelism parameter")
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return passwordHashParams{}, nil, nil, fmt.Errorf("malformed password hash salt")
	}
	expectedHash, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return passwordHashParams{}, nil, nil, fmt.Errorf("malformed password hash value")
	}

	params := passwordHashParams{
		memory:      uint32(memory),
		iterations:  uint32(iterations),
		parallelism: uint8(parallelism),
		saltLength:  uint32(len(salt)),
		keyLength:   uint32(len(expectedHash)),
	}
	if err := validatePasswordHashParams(params); err != nil {
		return passwordHashParams{}, nil, nil, err
	}

	return params, salt, expectedHash, nil
}

func validatePasswordHashParams(params passwordHashParams) error {
	if params.parallelism == 0 || params.parallelism > maxPasswordHashParallelism {
		return fmt.Errorf("password hash parallelism is outside the allowed range")
	}
	if params.memory < 8*uint32(params.parallelism) || params.memory > maxPasswordHashMemory {
		return fmt.Errorf("password hash memory is outside the allowed range")
	}
	if params.iterations == 0 || params.iterations > maxPasswordHashIterations {
		return fmt.Errorf("password hash iterations are outside the allowed range")
	}
	if params.saltLength < 8 || params.saltLength > maxPasswordHashSaltLength {
		return fmt.Errorf("password hash salt length is outside the allowed range")
	}
	if params.keyLength < 16 || params.keyLength > maxPasswordHashKeyLength {
		return fmt.Errorf("password hash length is outside the allowed range")
	}
	return nil
}

func parseUintParameter(value, prefix string, bitSize int) (uint64, error) {
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, fmt.Errorf("missing parameter")
	}
	return strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, bitSize)
}

func verifyLegacyPassword(password, encodedHash, salt string) (bool, bool, error) {
	expectedHash, err := hex.DecodeString(encodedHash)
	if err != nil || len(expectedHash) != sha256.Size {
		return false, false, fmt.Errorf("malformed legacy password hash")
	}

	actualHash := sha256.Sum256([]byte(password + "-" + salt))
	valid := subtle.ConstantTimeCompare(actualHash[:], expectedHash) == 1
	return valid, valid, nil
}
