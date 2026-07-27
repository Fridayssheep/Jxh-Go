package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func TestPasswordHasherRoundTripAndUpgrade(t *testing.T) {
	hasher := NewPasswordHasher(DefaultPasswordParams(), bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	encoded, err := hasher.Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	ok, upgrade, err := hasher.Verify([]byte("correct horse battery staple"), encoded)
	if err != nil || !ok || upgrade {
		t.Fatalf("Verify() = ok %v, upgrade %v, err %v", ok, upgrade, err)
	}
	if ok, _, err := hasher.Verify([]byte("wrong password"), encoded); err != nil || ok {
		t.Fatalf("wrong password Verify() = ok %v, err %v", ok, err)
	}

	oldParams := DefaultPasswordParams()
	oldParams.MemoryKiB = 32 * 1024
	oldHasher := NewPasswordHasher(oldParams, bytes.NewReader(bytes.Repeat([]byte{8}, 64)))
	oldEncoded, err := oldHasher.Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, upgrade, err := hasher.Verify([]byte("correct horse battery staple"), oldEncoded); err != nil || !ok || !upgrade {
		t.Fatalf("old Verify() = ok %v, upgrade %v, err %v", ok, upgrade, err)
	}
}

func TestPasswordHasherRejectsHostilePHCParameters(t *testing.T) {
	hasher := NewPasswordHasher(DefaultPasswordParams(), rand.Reader)
	tests := []string{
		"$argon2id$v=19$m=4294967295,t=3,p=2$AA$AA",
		"$argon2id$v=19$m=65536,t=11,p=2$AA$AA",
		"$argon2id$v=19$m=65536,t=3,p=9$AA$AA",
		"$argon2id$v=18$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2i$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=3,p=2$" + strings.Repeat("A", 1000) + "$AA",
		"$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$" + strings.Repeat("A", 1000),
	}
	for _, encoded := range tests {
		if _, _, err := hasher.Verify([]byte("x"), encoded); !errors.Is(err, ErrInvalidPasswordHash) {
			t.Fatalf("Verify(%q) error = %v, want ErrInvalidPasswordHash", encoded, err)
		}
	}
}

func TestPasswordHasherRejectsMalformedAndOutOfRangeDecodedLengths(t *testing.T) {
	hasher := NewPasswordHasher(DefaultPasswordParams(), rand.Reader)
	tests := []string{
		"",
		"not-a-phc",
		"$argon2id$v=19$m=65536,t=3,p=2$***$***",
		"$argon2id$v=19$m=65536,t=3,p=2$AA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AA",
		"$argon2id$v=19$m=7,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
	}
	for _, encoded := range tests {
		if _, _, err := hasher.Verify([]byte("x"), encoded); !errors.Is(err, ErrInvalidPasswordHash) {
			t.Fatalf("Verify(%q) error = %v, want ErrInvalidPasswordHash", encoded, err)
		}
	}
}

func TestPasswordHasherReportsRandomSourceFailure(t *testing.T) {
	hasher := NewPasswordHasher(DefaultPasswordParams(), bytes.NewReader(nil))
	if _, err := hasher.Hash([]byte("valid password material")); !errors.Is(err, ErrPasswordRandomness) {
		t.Fatalf("Hash() error = %v, want ErrPasswordRandomness", err)
	}
}
