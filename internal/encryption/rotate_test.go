package encryption

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func singleKeyConfig(t *testing.T) []byte {
	t.Helper()
	content, err := NewConfig(rand.Reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func keyNames(t *testing.T, content []byte) []string {
	t.Helper()
	names, err := Keys(content)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestKeysListsNamesInOrder(t *testing.T) {
	content, err := AddKey(singleKeyConfig(t), "key2", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if got := keyNames(t, content); !slices.Equal(got, []string{"key1", "key2"}) {
		t.Fatalf("keys %v", got)
	}
}

func TestAddKeyPutsTheNewKeySecondWith32RandomBytes(t *testing.T) {
	fixed := bytes.Repeat([]byte{9}, 32)
	content, err := AddKey(singleKeyConfig(t), "key2", bytes.NewReader(fixed))
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, content)
	keys := secretsProviders(t, cfg)[0].AESGCM.Keys
	if len(keys) != 2 || keys[0].Name != "key1" || keys[1].Name != "key2" {
		t.Fatalf("keys %+v", keys)
	}
	if keys[1].Secret != base64.StdEncoding.EncodeToString(fixed) {
		t.Fatalf("new key bytes %q", keys[1].Secret)
	}
}

func TestAddKeyOfAPresentKeyChangesNothing(t *testing.T) {
	once, err := AddKey(singleKeyConfig(t), "key2", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := AddKey(once, "key2", rand.Reader)
	if err != nil || !bytes.Equal(once, twice) {
		t.Fatalf("second add changed the content: %v", err)
	}
}

func TestAddKeyReadsExactly32Bytes(t *testing.T) {
	if _, err := AddKey(singleKeyConfig(t), "key2", bytes.NewReader([]byte{1, 2})); err == nil {
		t.Fatal("a short random source must fail")
	}
}

func TestPromoteMovesTheKeyFirstAndIsIdempotent(t *testing.T) {
	added, err := AddKey(singleKeyConfig(t), "key2", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := Promote(added, "key2")
	if err != nil {
		t.Fatal(err)
	}
	if got := keyNames(t, promoted); !slices.Equal(got, []string{"key2", "key1"}) {
		t.Fatalf("keys %v", got)
	}
	again, err := Promote(promoted, "key2")
	if err != nil || !bytes.Equal(promoted, again) {
		t.Fatalf("second promote changed the content: %v", err)
	}
}

func TestPromoteOfAnUnknownKeyFails(t *testing.T) {
	if _, err := Promote(singleKeyConfig(t), "key9"); err == nil {
		t.Fatal("promote of an unknown key must fail")
	}
}

func TestRemoveKeyDropsTheKeyAndIsIdempotent(t *testing.T) {
	added, _ := AddKey(singleKeyConfig(t), "key2", rand.Reader)
	promoted, _ := Promote(added, "key2")
	removed, err := RemoveKey(promoted, "key1")
	if err != nil {
		t.Fatal(err)
	}
	if got := keyNames(t, removed); !slices.Equal(got, []string{"key2"}) {
		t.Fatalf("keys %v", got)
	}
	again, err := RemoveKey(removed, "key1")
	if err != nil || !bytes.Equal(removed, again) {
		t.Fatalf("second remove changed the content: %v", err)
	}
}

func TestRemoveKeyRefusesTheLastKey(t *testing.T) {
	if _, err := RemoveKey(singleKeyConfig(t), "key1"); err == nil {
		t.Fatal("removing the last key must fail")
	}
}

func TestNextKeyNameCountsUp(t *testing.T) {
	content := singleKeyConfig(t)
	if name, err := NextKeyName(content); err != nil || name != "key2" {
		t.Fatalf("name %q err %v", name, err)
	}
	added, _ := AddKey(content, "key7", rand.Reader)
	if name, err := NextKeyName(added); err != nil || name != "key8" {
		t.Fatalf("name %q err %v", name, err)
	}
}

func TestRotationOperationsRefuseKMS(t *testing.T) {
	content, err := NewConfig(rand.Reader, &v1alpha1.KMSSpec{Endpoint: kmsEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Keys(content); !errors.Is(err, ErrNotAESGCM) {
		t.Fatalf("keys err %v", err)
	}
	if _, err := AddKey(content, "key2", rand.Reader); !errors.Is(err, ErrNotAESGCM) {
		t.Fatalf("add err %v", err)
	}
	if _, err := NextKeyName(content); !errors.Is(err, ErrNotAESGCM) {
		t.Fatalf("next err %v", err)
	}
}

func TestHashIsStableAndSensitive(t *testing.T) {
	content := singleKeyConfig(t)
	if Hash(content) != Hash(slices.Clone(content)) || Hash(content) == Hash(append(slices.Clone(content), '\n')) {
		t.Fatal("hash must follow the content")
	}
	if len(Hash(content)) != 64 {
		t.Fatalf("hash %q", Hash(content))
	}
}

func TestDropsKey(t *testing.T) {
	one := singleKeyConfig(t)
	added, _ := AddKey(one, "key2", rand.Reader)
	promoted, _ := Promote(added, "key2")
	removed, _ := RemoveKey(promoted, "key1")
	cases := []struct {
		name           string
		previous, next []byte
		want           bool
	}{
		{"add keeps every key", one, added, false},
		{"promote keeps every key", added, promoted, false},
		{"remove drops a key", promoted, removed, true},
		{"same content", removed, removed, false},
	}
	for _, c := range cases {
		if got := DropsKey(c.previous, c.next); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}
