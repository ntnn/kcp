package framework

import "testing"

func TestGoleakWithDefaults(t *testing.T) {
	defer GoleakWithDefaults(t)

	_, _, cancel := StartTestServer(t)
	cancel()
}
