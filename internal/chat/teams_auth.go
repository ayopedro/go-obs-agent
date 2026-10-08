package chat

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type signingKey struct {
	key          *rsa.PublicKey
	endorsements []string
}
type TeamsAuth struct {
	AppID     string
	Client    *http.Client
	mu        sync.Mutex
	keys      map[string]signingKey
	refreshed time.Time
	keysURL   string
}
type connectorClaims struct {
	jwt.RegisteredClaims
	ServiceURL string `json:"serviceUrl"`
}

func (a *TeamsAuth) Verify(ctx context.Context, authorization, serviceURL string) error {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return fmt.Errorf("missing bearer token")
	}
	claims := &connectorClaims{}
	_, err := jwt.ParseWithClaims(parts[1], claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing key id")
		}
		key, err := a.key(ctx, kid)
		if err != nil {
			return nil, err
		}
		endorsed := false
		for _, channel := range key.endorsements {
			if channel == "msteams" {
				endorsed = true
			}
		}
		if !endorsed {
			return nil, fmt.Errorf("key lacks Teams endorsement")
		}
		return key.key, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("https://api.botframework.com"), jwt.WithAudience(a.AppID), jwt.WithExpirationRequired(), jwt.WithLeeway(5*time.Minute))
	if err != nil {
		return err
	}
	if claims.NotBefore == nil || claims.ServiceURL == "" || claims.ServiceURL != serviceURL {
		return fmt.Errorf("invalid validity or service URL claim")
	}
	return nil
}
func (a *TeamsAuth) key(ctx context.Context, kid string) (signingKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if key, ok := a.keys[kid]; ok && time.Since(a.refreshed) < time.Hour {
		return key, nil
	}
	// Throttle unknown-key refreshes, while permitting normal key rotation.
	if time.Since(a.refreshed) < time.Minute {
		return signingKey{}, fmt.Errorf("unknown signing key")
	}
	endpoint := a.keysURL
	if endpoint == "" {
		endpoint = "https://login.botframework.com/v1/.well-known/keys"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return signingKey{}, err
	}
	response, err := a.Client.Do(req)
	if err != nil {
		return signingKey{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return signingKey{}, fmt.Errorf("signing-key lookup failed")
	}
	var document struct {
		Keys []struct {
			KID, KTY, N, E string
			Endorsements   []string
		}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return signingKey{}, err
	}
	keys := map[string]signingKey{}
	for _, k := range document.Keys {
		if k.KTY != "RSA" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil || len(e) > 4 {
			continue
		}
		exponent := 0
		for _, b := range e {
			exponent = exponent<<8 | int(b)
		}
		if exponent < 3 || len(n) < 256 {
			continue
		}
		keys[k.KID] = signingKey{&rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}, k.Endorsements}
	}
	a.keys = keys
	a.refreshed = time.Now()
	key, ok := keys[kid]
	if !ok {
		return signingKey{}, fmt.Errorf("unknown signing key")
	}
	return key, nil
}
