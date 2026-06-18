package httpx

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

const (
	UsernameFilename = "username"
	PasswordFilename = "password"
)

// LoadUserinfo loads a username and password file from the given base directory.
func LoadUserinfo(dirPath string) (*url.Userinfo, error) {
	if dirPath == "" {
		return nil, errors.New("dir path cannot be empty")
	}
	username, err := os.ReadFile(filepath.Join(dirPath, UsernameFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	password, err := os.ReadFile(filepath.Join(dirPath, PasswordFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(username) == 0 && len(password) == 0 {
		return nil, nil
	}
	if len(username) != 0 && len(password) == 0 {
		return url.User(string(username)), nil
	}
	return url.UserPassword(string(username), string(password)), nil
}

// UserinfoHeaderValue returns the authorization header value for basic auth.
func UserinfoHeaderValue(userinfo url.Userinfo) string {
	authorization := userinfo.Username() + ":"
	if password, ok := userinfo.Password(); ok {
		authorization += password
	}
	authorization = base64.StdEncoding.EncodeToString([]byte(authorization))
	authorization = "Basic " + authorization
	return authorization
}

// AuthenticateUserinfo returns true if the username and password from basic auth matches the user info.
func AuthenticateUserinfo(basicAuthFn func() (string, string, bool), userinfo url.Userinfo) bool {
	username, password, ok := basicAuthFn()
	if !ok {
		return false
	}

	expectedUsername := userinfo.Username()
	expectedPassword, _ := userinfo.Password()

	usernameMatch := (subtle.ConstantTimeCompare([]byte(username), []byte(expectedUsername)) == 1)
	passwordMatch := (subtle.ConstantTimeCompare([]byte(password), []byte(expectedPassword)) == 1)
	return usernameMatch && passwordMatch
}
