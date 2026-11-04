package driver

import "testing"

func TestClientInfoSecretNameFollowsChart(t *testing.T) {
	t.Setenv(clientInfoSecretEnv, "")
	if got := clientInfoSecretName(); got != defaultClientInfoSecretName {
		t.Fatalf("default = %q", got)
	}
	t.Setenv(clientInfoSecretEnv, "my-client-info")
	if got := clientInfoSecretName(); got != "my-client-info" {
		t.Fatalf("from env = %q", got)
	}
}
