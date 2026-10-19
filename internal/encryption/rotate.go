package encryption

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"

	apiserverv1 "k8s.io/apiserver/pkg/apis/apiserver/v1"
	"sigs.k8s.io/yaml"
)

var (
	ErrNotAESGCM = errors.New("the encryption config has no aesgcm provider for secrets")

	keyNamePattern = regexp.MustCompile(`^key(\d+)$`)
)

func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func Keys(content []byte) ([]string, error) {
	cfg, err := parseConfig(content)
	if err != nil {
		return nil, err
	}
	provider, err := aesgcmOf(cfg)
	if err != nil {
		return nil, err
	}
	return keyNamesOf(provider.AESGCM.Keys), nil
}

func NextKeyName(content []byte) (string, error) {
	names, err := Keys(content)
	if err != nil {
		return "", err
	}
	highest := 0
	for _, name := range names {
		if match := keyNamePattern.FindStringSubmatch(name); match != nil {
			number, _ := strconv.Atoi(match[1])
			highest = max(highest, number)
		}
	}
	return "key" + strconv.Itoa(highest+1), nil
}

func AddKey(content []byte, name string, random io.Reader) ([]byte, error) {
	return editKeys(content, func(keys []apiserverv1.Key) ([]apiserverv1.Key, error) {
		if slices.ContainsFunc(keys, func(key apiserverv1.Key) bool { return key.Name == name }) {
			return keys, nil
		}
		secret := make([]byte, aesKeyBytes)
		if _, err := io.ReadFull(random, secret); err != nil {
			return nil, fmt.Errorf("read %d random bytes for the encryption key: %w", aesKeyBytes, err)
		}
		return append(slices.Clone(keys), apiserverv1.Key{Name: name, Secret: base64.StdEncoding.EncodeToString(secret)}), nil
	})
}

func Promote(content []byte, name string) ([]byte, error) {
	return editKeys(content, func(keys []apiserverv1.Key) ([]apiserverv1.Key, error) {
		index := slices.IndexFunc(keys, func(key apiserverv1.Key) bool { return key.Name == name })
		if index < 0 {
			return nil, fmt.Errorf("the encryption config has no key %s", name)
		}
		if index == 0 {
			return keys, nil
		}
		ordered := slices.Clone(keys)
		chosen := ordered[index]
		ordered = slices.Delete(ordered, index, index+1)
		return slices.Insert(ordered, 0, chosen), nil
	})
}

func RemoveKey(content []byte, name string) ([]byte, error) {
	return editKeys(content, func(keys []apiserverv1.Key) ([]apiserverv1.Key, error) {
		kept := slices.DeleteFunc(slices.Clone(keys), func(key apiserverv1.Key) bool { return key.Name == name })
		if len(kept) == len(keys) {
			return keys, nil
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("the encryption config must keep one key, cannot remove %s", name)
		}
		return kept, nil
	})
}

func DropsKey(previous, next []byte) bool {
	before, errBefore := Keys(previous)
	after, errAfter := Keys(next)
	if errBefore != nil || errAfter != nil {
		return false
	}
	return slices.ContainsFunc(before, func(name string) bool { return !slices.Contains(after, name) })
}

func editKeys(content []byte, edit func([]apiserverv1.Key) ([]apiserverv1.Key, error)) ([]byte, error) {
	cfg, err := parseConfig(content)
	if err != nil {
		return nil, err
	}
	provider, err := aesgcmOf(cfg)
	if err != nil {
		return nil, err
	}
	edited, err := edit(provider.AESGCM.Keys)
	if err != nil {
		return nil, err
	}
	if slices.Equal(keyNamesOf(edited), keyNamesOf(provider.AESGCM.Keys)) && slices.Equal(edited, provider.AESGCM.Keys) {
		return bytes.Clone(content), nil
	}
	provider.AESGCM.Keys = edited
	return yaml.Marshal(cfg)
}

func parseConfig(content []byte) (apiserverv1.EncryptionConfiguration, error) {
	var cfg apiserverv1.EncryptionConfiguration
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return cfg, fmt.Errorf("parse encryption config: %w", err)
	}
	return cfg, nil
}

func aesgcmOf(cfg apiserverv1.EncryptionConfiguration) (*apiserverv1.ProviderConfiguration, error) {
	for entryIndex := range cfg.Resources {
		entry := &cfg.Resources[entryIndex]
		if !slices.Contains(entry.Resources, secretsResource) {
			continue
		}
		for providerIndex := range entry.Providers {
			if entry.Providers[providerIndex].AESGCM != nil {
				return &entry.Providers[providerIndex], nil
			}
		}
	}
	return nil, ErrNotAESGCM
}

func keyNamesOf(keys []apiserverv1.Key) []string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.Name)
	}
	return names
}
