package framework

import "testing"

func TestGoleakWithDefaults(t *testing.T) {
	_, _, cancel := StartTestServer(t)
	cancel()
	GoleakWithDefaults(t)
}
