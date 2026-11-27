package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/model"
)

func TestSecretEnvName_MarksTheNamesOfSecrets(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"GITHUB_TOKEN", "my_secret", "DB_Password", "AWS_CREDENTIALS", "API_KEY", "api_key"} {
		assert.True(t, model.SecretEnvName(name), name)
	}

	for _, name := range []string{"GH_PAT", "gh_pat", "DATABASE_DSN", "SENTRY_DSN", "SERVICE_AUTH", "api_auth", "PAT", "AUTH", "DSN", "pat", "GH_PAT_OLD", "gh_pat_old"} {
		assert.True(t, model.SecretEnvName(name), name)
	}

	for _, name := range []string{"REPO", "BASE", "KEYBOARD", "OWNER_LOGIN", "PATH", "PATCH_LEVEL", "PAT_LEVEL", "AUTHOR", "GIT_AUTHOR", "DSN_HOST", "OAUTH", "SSH_AUTH_SOCK"} {
		assert.False(t, model.SecretEnvName(name), name)
	}
}
