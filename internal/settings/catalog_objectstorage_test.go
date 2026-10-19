package settings

import (
	"strings"
	"testing"
)

func TestObjectStorageCatalogKeysAndDefaults(t *testing.T) {
	want := map[string]string{
		"objectstorage.reserved-bucket-names":    "admin",
		"objectstorage.reserved-bucket-prefixes": "",
		"objectstorage.cors-origins":             "",
		"objectstorage.max-buckets":              "100",
		"objectstorage.public-host":              "",
		"objectstorage.public-aliases":           "",
	}
	for key, def := range want {
		got, ok := Lookup(key)
		if !ok || got.Default != def || got.Description == "" {
			t.Fatalf("setting %q: %+v", key, got)
		}
		if err := got.Validate(got.Default); err != nil {
			t.Fatalf("the default of %q is invalid: %v", key, err)
		}
	}
}

func TestObjectStorageListsValidate(t *testing.T) {
	long := strings.Repeat("a", 64)
	cases := []struct {
		key   string
		value string
		valid bool
	}{
		{"objectstorage.reserved-bucket-names", "", true},
		{"objectstorage.reserved-bucket-names", "admin,billing", true},
		{"objectstorage.reserved-bucket-names", "a", true},
		{"objectstorage.reserved-bucket-names", "Admin", false},
		{"objectstorage.reserved-bucket-names", "admin,", false},
		{"objectstorage.reserved-bucket-names", "-admin", false},
		{"objectstorage.reserved-bucket-names", "admin-", false},
		{"objectstorage.reserved-bucket-names", long, false},
		{"objectstorage.reserved-bucket-names", "a b", false},
		{"objectstorage.reserved-bucket-prefixes", "", true},
		{"objectstorage.reserved-bucket-prefixes", "git-,cloudyfolks-,e2e-", true},
		{"objectstorage.reserved-bucket-prefixes", "git", true},
		{"objectstorage.reserved-bucket-prefixes", "Git-", false},
		{"objectstorage.reserved-bucket-prefixes", "-git", false},
		{"objectstorage.reserved-bucket-prefixes", "git_", false},
		{"objectstorage.reserved-bucket-prefixes", ",git-", false},
		{"objectstorage.cors-origins", "", true},
		{"objectstorage.cors-origins", "https://panel.example.com", true},
		{"objectstorage.cors-origins", "https://panel.example.com,http://localhost:3000", true},
		{"objectstorage.cors-origins", "https://panel.example.com/", true},
		{"objectstorage.cors-origins", "panel.example.com", false},
		{"objectstorage.cors-origins", "ftp://panel.example.com", false},
		{"objectstorage.cors-origins", "https://panel.example.com/path", false},
		{"objectstorage.cors-origins", "https://panel.example.com?x=1", false},
		{"objectstorage.cors-origins", "https://user@panel.example.com", false},
		{"objectstorage.cors-origins", "https://", false},
		{"objectstorage.cors-origins", "https://a.test,", false},
		{"objectstorage.max-buckets", "", true},
		{"objectstorage.max-buckets", "1", true},
		{"objectstorage.max-buckets", "10000", true},
		{"objectstorage.max-buckets", "0", false},
		{"objectstorage.max-buckets", "10001", false},
		{"objectstorage.max-buckets", "many", false},
		{"objectstorage.public-host", "", true},
		{"objectstorage.public-host", "s3.example.com", true},
		{"objectstorage.public-host", "S3 example", false},
		{"objectstorage.public-host", "s3.example.com,s3.other.com", false},
		{"objectstorage.public-aliases", "", true},
		{"objectstorage.public-aliases", "s3.a.example.com,s3.b.example.com", true},
		{"objectstorage.public-aliases", "s3.a.example.com,", false},
		{"objectstorage.public-aliases", "bad name", false},
	}
	for _, tc := range cases {
		if err := validates(t, tc.key, tc.value); (err == nil) != tc.valid {
			t.Fatalf("%s %q valid=%v err=%v", tc.key, tc.value, tc.valid, err)
		}
	}
}
