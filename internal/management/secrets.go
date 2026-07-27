package management

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

var ErrInvalidSecret = errors.New("invalid management secret")

const minimumMasterSecretBytes = 32

type Secrets struct {
	SessionToken    []byte
	LoginLimiter    []byte
	AdminMutation   []byte
	SystemOperation []byte
	CommandArgument []byte
	TelemetryUser   []byte
}

func DeriveSecrets(master []byte) (Secrets, error) {
	if len(master) < minimumMasterSecretBytes {
		return Secrets{}, ErrInvalidSecret
	}
	return Secrets{
		SessionToken:    derive(master, "session-token"),
		LoginLimiter:    derive(master, "login-limiter"),
		AdminMutation:   derive(master, "admin-mutation"),
		SystemOperation: derive(master, "system-operation"),
		CommandArgument: derive(master, "command-argument"),
		TelemetryUser:   derive(master, "telemetry-user"),
	}, nil
}

func derive(master []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("jxh-manager/v1/" + purpose))
	return mac.Sum(nil)
}
