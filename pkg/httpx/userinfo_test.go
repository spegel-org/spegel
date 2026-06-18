package httpx

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestLoadUserinfo(t *testing.T) {
	t.Parallel()

	_, err := LoadUserinfo("")
	require.EqualError(t, err, "dir path cannot be empty")

	dirPath := t.TempDir()

	userinfo, err := LoadUserinfo(dirPath)
	require.NoError(t, err)
	require.Nil(t, userinfo)

	expectedUsername := "helloworld"
	err = os.WriteFile(filepath.Join(dirPath, UsernameFilename), []byte(expectedUsername), 0o644)
	require.NoError(t, err)
	userinfo, err = LoadUserinfo(dirPath)
	require.NoError(t, err)
	require.EqualT(t, expectedUsername, userinfo.Username())
	password, ok := userinfo.Password()
	require.FalseT(t, ok)
	require.Empty(t, password)

	expectedPassword := "supersecret"
	err = os.WriteFile(filepath.Join(dirPath, PasswordFilename), []byte(expectedPassword), 0o644)
	require.NoError(t, err)
	userinfo, err = LoadUserinfo(dirPath)
	require.NoError(t, err)
	require.EqualT(t, expectedUsername, userinfo.Username())
	password, ok = userinfo.Password()
	require.TrueT(t, ok)
	require.EqualT(t, expectedPassword, password)
}

func TestBasicAuthHeaderValue(t *testing.T) {
	t.Parallel()

	//nolint: govet // Prioritize readability in tests.
	tests := []struct {
		userinfo url.Userinfo
		expected string
	}{
		{
			userinfo: *url.User("foo"),
			expected: "Basic Zm9vOg==",
		},
		{
			userinfo: *url.UserPassword("foo", "bar"),
			expected: "Basic Zm9vOmJhcg==",
		},
		{
			userinfo: *url.UserPassword("foo", ""),
			expected: "Basic Zm9vOg==",
		},
	}
	for _, tt := range tests {
		t.Run(tt.userinfo.String(), func(t *testing.T) {
			t.Parallel()

			v := UserinfoHeaderValue(tt.userinfo)
			require.EqualT(t, tt.expected, v)
		})
	}
}

func TestAuthenticateUserinfo(t *testing.T) {
	t.Parallel()

	//nolint: govet // Prioritize readability in tests.
	tests := []struct {
		userinfo    url.Userinfo
		basicAuthFn func() (string, string, bool)
		expected    bool
	}{
		{
			userinfo: *url.User("admin"),
			basicAuthFn: func() (string, string, bool) {
				return "admin", "", true
			},
			expected: true,
		},
		{
			userinfo: *url.User("admin"),
			basicAuthFn: func() (string, string, bool) {
				return "root", "", true
			},
			expected: false,
		},
		{
			userinfo: *url.UserPassword("admin", "password!34"),
			basicAuthFn: func() (string, string, bool) {
				return "admin", "password!34", true
			},
			expected: true,
		},
		{
			userinfo: *url.User("admin"),
			basicAuthFn: func() (string, string, bool) {
				return "admin", "test", true
			},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.userinfo.String(), func(t *testing.T) {
			t.Parallel()

			ok := AuthenticateUserinfo(tt.basicAuthFn, tt.userinfo)
			require.EqualT(t, tt.expected, ok)
		})
	}
}
