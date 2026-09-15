package config

import "testing"

func TestHTTPSOrigin(t *testing.T) {
	for _, value := range []string{"http://auth.example.test", "https:", "https:///missing", "https://u:p@auth.test", "https://auth.test/path", "https://auth.test?token=x", "https://auth.test#x", "https://auth.test?", "https://auth.test\n"} {
		if ValidateHTTPSOrigin(value) == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if err := ValidateHTTPSOrigin("https://auth.example.test"); err != nil {
		t.Fatal(err)
	}
}
