package credential

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("correct horse battery staple", DefaultParameters)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify failed: %v", err)
	}
	ok, err = Verify(hash, "incorrect password")
	if err != nil || ok {
		t.Fatalf("invalid password accepted: %v", err)
	}
}

func TestRejectsExpensiveHashParameters(t *testing.T) {
	_, err := Verify("$argon2id$v=19$app=1$m=999999,t=3,p=2$c2FsdA$a2V5", "password")
	if err == nil {
		t.Fatal("expected parameter limit error")
	}
}
