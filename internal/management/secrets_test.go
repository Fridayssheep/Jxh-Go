package management

import (
	"bytes"
	"errors"
	"testing"
)

func TestDeriveSecretsSeparatesPurposesAndCopiesOutput(t *testing.T) {
	master := bytes.Repeat([]byte{0x5a}, minimumMasterSecretBytes)
	first, err := DeriveSecrets(master)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeriveSecrets(master)
	if err != nil {
		t.Fatal(err)
	}
	values := [][]byte{first.SessionToken, first.LoginLimiter, first.SystemOperation, first.CommandArgument, first.TelemetryUser}
	for index, value := range values {
		if len(value) != 32 {
			t.Fatalf("derived secret %d length=%d", index, len(value))
		}
		for previous := 0; previous < index; previous++ {
			if bytes.Equal(value, values[previous]) {
				t.Fatalf("derived secrets %d and %d are equal", previous, index)
			}
		}
	}
	if !bytes.Equal(first.SessionToken, second.SessionToken) {
		t.Fatal("derivation is not deterministic")
	}
	first.SessionToken[0] ^= 0xff
	if bytes.Equal(first.SessionToken, second.SessionToken) {
		t.Fatal("derived outputs share mutable storage")
	}
}

func TestDeriveSecretsRejectsWeakMaster(t *testing.T) {
	if _, err := DeriveSecrets(make([]byte, minimumMasterSecretBytes-1)); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("error=%v", err)
	}
}
