package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
)

func TestAPIKeyStoredAsHashOnly(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secret := "mr-sk-" + strings.Repeat("s", 24)
	key := &model.APIKey{
		ID: NewID("key"), Name: "test", Prefix: secret[:12],
		Hash: HashToken(secret), Enabled: true, CreatedAt: Now(),
	}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatal(err)
	}

	got, err := db.APIKeyByHash(HashToken(secret))
	if err != nil {
		t.Fatalf("lookup by hash: %v", err)
	}
	if got.ID != key.ID {
		t.Fatal("wrong key returned")
	}
	if got.Hash == secret || len(got.Hash) != 64 {
		t.Fatalf("hash must be sha256 hex, not the plaintext")
	}

	if _, err := db.APIKeyByHash(HashToken("wrong")); err == nil {
		t.Fatal("wrong secret must not resolve")
	}
}

func TestCredentialRoundTrip(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	p := &model.Provider{ID: NewID("prv"), Name: "P", Type: "openai", BaseURL: "https://api.openai.com/v1", CreatedAt: Now()}
	if err := db.CreateProvider(p); err != nil {
		t.Fatal(err)
	}
	ct := []byte{1, 2, 3, 4}
	nonce := []byte{9, 8, 7}
	if err := db.SaveCredential(p.ID, ct, nonce, 1, "sk-****abcd"); err != nil {
		t.Fatal(err)
	}
	gotCT, gotNonce, err := db.CredentialFor(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCT) != string(ct) || string(gotNonce) != string(nonce) {
		t.Fatal("credential ciphertext/nonce mismatch")
	}
}

func TestRouteResolveByAliasCaseInsensitive(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	route := &model.Route{Alias: "Coding", Enabled: true, Strategy: "priority", Retry: 1, TimeoutMS: 30000}
	if err := db.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"coding", "CODING", "CoDiNg"} {
		if _, err := db.RouteByAlias(alias); err != nil {
			t.Errorf("alias %q should resolve: %v", alias, err)
		}
	}
}

func TestSecretRedaction(t *testing.T) {
	// RedactText lives in logx; this test guards the store-level hash contract.
	h1 := HashToken("secret-a")
	h2 := HashToken("secret-b")
	if h1 == h2 {
		t.Fatal("different secrets must hash differently")
	}
	if len(h1) != 64 {
		t.Fatalf("expected sha256 hex length 64, got %d", len(h1))
	}
}

// TestEmptyListsEncodeAsArrays guards the SPA contract: a nil slice marshals to
// `null`, and the dashboard calls .map/.filter on these fields. A fresh install
// with zero routes used to blank Routes/Activity/API Keys with a TypeError.
func TestEmptyListsEncodeAsArrays(t *testing.T) {
	db, err := Open(t.TempDir() + "/empty.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	routes, err := db.ListRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if routes == nil {
		t.Fatal("ListRoutes returned nil; JSON would be null")
	}
	raw, err := json.Marshal(routes)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("routes JSON = %s, want []", raw)
	}

	if got := SplitCSV(""); got == nil {
		t.Fatal("SplitCSV on empty input returned nil; JSON would be null")
	}
}
