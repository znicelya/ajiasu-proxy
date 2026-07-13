package testkit

import "testing"

func TestDockerRequiredFromEnvironment(t *testing.T) {
	t.Setenv("AJIASU_REQUIRE_DOCKER", "")
	if dockerRequired() {
		t.Fatal("dockerRequired() = true without explicit CI requirement")
	}

	t.Setenv("AJIASU_REQUIRE_DOCKER", "1")
	if !dockerRequired() {
		t.Fatal("dockerRequired() = false with AJIASU_REQUIRE_DOCKER=1")
	}
}
