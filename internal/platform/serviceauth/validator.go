package serviceauth

import (
	"crypto/subtle"
	"strings"
)

// Validator authenticates trusted services on the private backend network.
// It deliberately uses a token distinct from the Telegram Bot API token.
type Validator struct {
	token []byte
}

func NewValidator(token string) *Validator {
	return &Validator{token: []byte(token)}
}

func (v *Validator) ValidateAuthorization(header string) bool {
	scheme, credential, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}

	credential = strings.TrimSpace(credential)
	if credential == "" || len(v.token) == 0 || len(credential) != len(v.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(credential), v.token) == 1
}
