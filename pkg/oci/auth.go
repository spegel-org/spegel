package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/spegel-org/spegel/pkg/httpx"
)

type Credential struct {
	Username string
	Password string
}

func (c Credential) IsEmpty() bool {
	return c == Credential{}
}

type CredentialProvider interface {
	Credential(ctx context.Context, registry string) (Credential, error)
}

type BasicAuthCredentialProvider struct {
	credential Credential
}

func NewBasicAuthCredentialProvider(userInfo url.Userinfo) BasicAuthCredentialProvider {
	username := userInfo.Username()
	password, _ := userInfo.Password()
	return BasicAuthCredentialProvider{
		credential: Credential{Username: username, Password: password},
	}
}

func (p BasicAuthCredentialProvider) Credential(ctx context.Context, registry string) (Credential, error) {
	return p.credential, nil
}

type Authenticator struct {
	group      singleflight.Group
	httpClient *http.Client
	cache      map[string]string
	cps        []CredentialProvider
	cacheMx    sync.RWMutex
}

func NewAuthenticator(httpClient *http.Client, cps []CredentialProvider) *Authenticator {
	return &Authenticator{
		cps:        cps,
		httpClient: httpClient,
		group:      singleflight.Group{},
		cache:      map[string]string{},
	}
}

func (a *Authenticator) Authorize(req *http.Request, registry, repo string) {
	a.cacheMx.RLock()
	key := authKey(registry, repo)
	h, ok := a.cache[key]
	a.cacheMx.RUnlock()
	if ok {
		req.Header.Set(httpx.HeaderAuthorization, h)
	}
}

func (a *Authenticator) HandleChallenge(ctx context.Context, registry, repo, oldAuth string, wwwAuth []string) error {
	key := authKey(registry, repo)

	a.cacheMx.Lock()
	if h, ok := a.cache[key]; ok && h != oldAuth {
		// Someone already refreshed token.
		a.cacheMx.Unlock()
		return nil
	}
	delete(a.cache, key)
	a.cacheMx.Unlock()

	ch, ok := pickChallenge(wwwAuth)
	if !ok {
		return errors.New("no usable WWW-Authenticate challenge")
	}
	_, err, _ := a.group.Do(key, func() (any, error) {
		h, err := a.authorizationHeader(ctx, registry, repo, ch)
		if err != nil {
			return nil, err
		}
		a.cacheMx.Lock()
		a.cache[key] = h
		a.cacheMx.Unlock()
		return nil, nil
	})
	return err
}

func (a *Authenticator) authorizationHeader(ctx context.Context, registry, repo string, ch Challenge) (string, error) {
	cred, err := func() (Credential, error) {
		for _, prov := range a.cps {
			cred, err := prov.Credential(ctx, registry)
			if err != nil {
				return Credential{}, err
			}
			if cred.IsEmpty() {
				continue
			}
			return cred, nil
		}
		return Credential{}, nil
	}()
	if err != nil {
		return "", err
	}

	switch ch.Scheme {
	case "bearer":
		tok, err := a.fetchToken(ctx, repo, ch, cred)
		if err != nil {
			return "", err
		}
		return "Bearer " + tok, nil
	case "basic":
		if cred.IsEmpty() {
			return "", errors.New("basic challenge and no credentials")
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(cred.Username+":"+cred.Password)), nil
	}
	return "", fmt.Errorf("unsupported challenge scheme %q", ch.Scheme)
}

func (a *Authenticator) fetchToken(ctx context.Context, repo string, ch Challenge, cred Credential) (string, error) {
	realm := ch.Params["realm"]
	if realm == "" {
		return "", errors.New("bearer challenge missing realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if s := ch.Params["service"]; s != "" {
		q.Set("service", s)
	}
	scope := ch.Params["scope"]
	if u.Host == "ghcr.io" {
		scope = strings.ReplaceAll(scope, "user/image", repo)
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if !cred.IsEmpty() {
		req.SetBasicAuth(cred.Username, cred.Password)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	err = httpx.CheckResponseStatus(resp, http.StatusOK)
	if err != nil {
		return "", err
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	if err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", errors.New("no token in response")
}

func authKey(registry, repo string) string {
	return registry + "," + repo
}

type Challenge struct {
	Params map[string]string
	Scheme string
}

var paramRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// Prefers bearer if several are offered.
func pickChallenge(headers []string) (Challenge, bool) {
	var first *Challenge
	for _, h := range headers {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
		c := Challenge{Scheme: strings.ToLower(scheme), Params: map[string]string{}}
		for _, m := range paramRe.FindAllStringSubmatch(rest, -1) {
			c.Params[strings.ToLower(m[1])] = m[2]
		}
		if c.Scheme == "bearer" {
			return c, true
		}
		if first == nil && c.Scheme != "" {
			first = &c
		}
	}
	if first == nil {
		return Challenge{}, false
	}
	return *first, true
}
