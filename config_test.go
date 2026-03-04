package caddyscope_test

import (
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	caddyscope "github.com/luzilla/caddyscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalCaddyfile(t *testing.T) {
	t.Run("Full", func(t *testing.T) {
		input := `caddyscope /dashboard {
			username admin
			password $2a$14$hZKMKHEkwOyh3jCnJRNYVubBSCcZaMSw8VIeRn61HFe37nLGz/RAO
			refresh 10s
		}`

		d := caddyfile.NewTestDispenser(input)
		var cs caddyscope.CaddyScope
		require.NoError(t, cs.UnmarshalCaddyfile(d))

		assert.Equal(t, "/dashboard", cs.Path)
		assert.Equal(t, "admin", cs.Username)
		assert.Equal(t, "$2a$14$hZKMKHEkwOyh3jCnJRNYVubBSCcZaMSw8VIeRn61HFe37nLGz/RAO", cs.PasswordHash)
		assert.Equal(t, 10*time.Second, time.Duration(cs.Refresh))
	})

	t.Run("Minimal", func(t *testing.T) {
		input := `caddyscope /scope {
			username viewer
			password $2b$12$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ12
		}`

		d := caddyfile.NewTestDispenser(input)
		var cs caddyscope.CaddyScope
		require.NoError(t, cs.UnmarshalCaddyfile(d))

		assert.Equal(t, "/scope", cs.Path)
		assert.Zero(t, cs.Refresh, "Refresh should be unset")
	})

	t.Run("InvalidRefresh", func(t *testing.T) {
		input := `caddyscope /dashboard {
			username admin
			password $2a$14$hZKMKHEkwOyh3jCnJRNYVubBSCcZaMSw8VIeRn61HFe37nLGz/RAO
			refresh notaduration
		}`

		d := caddyfile.NewTestDispenser(input)
		var cs caddyscope.CaddyScope
		require.Error(t, cs.UnmarshalCaddyfile(d), "expected error for invalid refresh duration")
	})
}

func TestValidateErrors(t *testing.T) {
	fixtures := []struct {
		title  string
		config string
		errMsg string
	}{
		{
			title: "MissingUsername",
			config: `caddyscope /dashboard {
				password $2a$14$hZKMKHEkwOyh3jCnJRNYVubBSCcZaMSw8VIeRn61HFe37nLGz/RAO
			}`,
			errMsg: "expected validation error for missing username",
		},
		{
			title: "MissingPassword",
			config: `caddyscope /dashboard {
				username admin
			}`,
			errMsg: "expected validation error for missing password",
		},
		{
			title: "PathMissingLeadingSlash",
			config: `caddyscope dashboard {
				username admin
				password $2a$14$hZKMKHEkwOyh3jCnJRNYVubBSCcZaMSw8VIeRn61HFe37nLGz/RAO
			}`,
			errMsg: "expected validation error for path without leading /",
		},
		{
			title: "InvalidBcryptHash",
			config: `caddyscope /dashboard {
				username admin
				password notabcrypthash
			}`,
			errMsg: "expected validation error for non-bcrypt password hash",
		},
	}

	for _, f := range fixtures {
		t.Run(f.title, func(t *testing.T) {
			d := caddyfile.NewTestDispenser(f.config)
			var cs caddyscope.CaddyScope
			require.NoError(t, cs.UnmarshalCaddyfile(d), "parse should succeed; validation catches missing fields")
			require.Error(t, cs.Validate(), f.errMsg)
		})
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cs := &caddyscope.CaddyScope{
		Path:         "/dashboard",
		Username:     "admin",
		PasswordHash: "$2a$14$hZKMKHEkwOyh3jCnJRNYVubBSCcZaMSw8VIeRn61HFe37nLGz/RAO",
		Refresh:      caddy.Duration(5 * time.Second),
	}
	require.NoError(t, cs.Validate())
}
