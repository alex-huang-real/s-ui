package common

import (
	"crypto/rand"
	"encoding/base64"
)

func NewSubscriptionToken() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}
