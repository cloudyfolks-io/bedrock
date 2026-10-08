package proxy

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	adminGroup        = "bedrock-admins"
	recordCacheTTL    = 5 * time.Second
	sessionCheckTTL   = 15 * time.Second
	refreshMargin     = 60 * time.Second
	claimLife         = 15 * time.Second
	claimPollEvery    = 200 * time.Millisecond
	claimPollFor      = 10 * time.Second
	refreshWriteLimit = 20 * time.Second
	cachePruneAbove   = 1024
	cacheMaxAge       = time.Minute
)

var (
	ErrUnauthenticated = errors.New("no valid session")
	ErrSessionEnded    = errors.New("session ended")
	ErrUpstream        = errors.New("upstream unavailable")
)

type Principal struct {
	Subject     string
	DisplayName string
	Email       string
	Groups      []string
	Admin       bool
	Access      string
	CSRF        string
	ExpiresAt   time.Time
}

type cachedRecord struct {
	rec       Record
	fetchedAt time.Time
	checkedAt time.Time
}

type Resolver struct {
	store  *Store
	oidc   *OIDC
	reader client.Reader
	clock  func() time.Time
	holder string
	mu     sync.Mutex
	cache  map[string]cachedRecord
}

func NewResolver(store *Store, oidc *OIDC, reader client.Reader, clock func() time.Time, holder string) *Resolver {
	return &Resolver{store: store, oidc: oidc, reader: reader, clock: clock, holder: holder, cache: map[string]cachedRecord{}}
}

func (r *Resolver) Resolve(ctx context.Context, cookieValue string) (Principal, error) {
	if cookieValue == "" {
		return Principal{}, ErrUnauthenticated
	}
	hash := secret.SHA256Hex(cookieValue)
	entry, err := r.load(ctx, hash)
	if err != nil {
		return Principal{}, err
	}
	now := r.clock()
	if !entry.rec.ExpiresAt.After(now) {
		return Principal{}, r.end(ctx, hash)
	}
	if now.Sub(entry.checkedAt) >= sessionCheckTTL {
		if err := r.checkAuthn(ctx, hash, entry.rec); err != nil {
			return Principal{}, err
		}
		entry.checkedAt = now
		r.remember(hash, entry)
	}
	if entry.rec.AccessExpiry.Sub(now) > refreshMargin {
		return principalOf(entry.rec), nil
	}
	fresh, err := r.refreshed(ctx, hash)
	if err != nil {
		return Principal{}, err
	}
	r.remember(hash, cachedRecord{rec: fresh, fetchedAt: r.clock(), checkedAt: entry.checkedAt})
	return principalOf(fresh), nil
}

func (r *Resolver) load(ctx context.Context, hash string) (cachedRecord, error) {
	r.mu.Lock()
	entry, found := r.cache[hash]
	r.mu.Unlock()
	if found && r.clock().Sub(entry.fetchedAt) < recordCacheTTL {
		return entry, nil
	}
	rec, err := r.store.Session(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		r.forget(hash)
		return cachedRecord{}, ErrUnauthenticated
	}
	if err != nil {
		slog.ErrorContext(ctx, "read session record", "error", err)
		return cachedRecord{}, ErrUpstream
	}
	loaded := cachedRecord{rec: rec, fetchedAt: r.clock(), checkedAt: entry.checkedAt}
	r.remember(hash, loaded)
	return loaded, nil
}

func (r *Resolver) checkAuthn(ctx context.Context, hash string, rec Record) error {
	var authnSession v1alpha1.Session
	err := r.reader.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: rec.SessionName}, &authnSession)
	if apierrors.IsNotFound(err) || err == nil && !authnSession.Spec.ExpiresAt.After(r.clock()) {
		return r.end(ctx, hash)
	}
	if err != nil {
		slog.ErrorContext(ctx, "read authn session", "error", err)
		return ErrUpstream
	}
	var user v1alpha1.User
	err = r.reader.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: rec.Subject}, &user)
	if apierrors.IsNotFound(err) || err == nil && user.Spec.Disabled {
		return r.end(ctx, hash)
	}
	if err != nil {
		slog.ErrorContext(ctx, "read authn user", "error", err)
		return ErrUpstream
	}
	return nil
}

func (r *Resolver) refreshed(ctx context.Context, hash string) (Record, error) {
	deadline := time.Now().Add(claimPollFor)
	for {
		rec, err := r.claimAndRefresh(ctx, hash)
		if !errors.Is(err, ErrClaimed) {
			return rec, err
		}
		if time.Now().After(deadline) {
			return Record{}, ErrUpstream
		}
		select {
		case <-ctx.Done():
			return Record{}, ErrUpstream
		case <-time.After(claimPollEvery):
		}
		current, readErr := r.store.Session(ctx, hash)
		if errors.Is(readErr, ErrNotFound) {
			return Record{}, ErrSessionEnded
		}
		if readErr == nil && current.AccessExpiry.Sub(r.clock()) > refreshMargin {
			return current, nil
		}
	}
}

func (r *Resolver) claimAndRefresh(ctx context.Context, hash string) (Record, error) {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshWriteLimit)
	defer cancel()
	claimed, err := r.store.ClaimRefresh(work, hash, r.holder, r.clock().Add(claimLife))
	if errors.Is(err, ErrNotFound) {
		return Record{}, ErrSessionEnded
	}
	if errors.Is(err, ErrClaimed) {
		return Record{}, ErrClaimed
	}
	if err != nil {
		slog.ErrorContext(ctx, "claim session refresh", "error", err)
		return Record{}, ErrUpstream
	}
	if claimed.AccessExpiry.Sub(r.clock()) > refreshMargin {
		return r.releaseFresh(work, hash, claimed)
	}
	tokens, err := r.oidc.Refresh(work, claimed.Refresh)
	if errors.Is(err, ErrInvalidGrant) {
		return Record{}, r.end(work, hash)
	}
	if err != nil {
		slog.ErrorContext(ctx, "refresh the access token", "error", err)
		r.releaseQuietly(work, hash)
		return Record{}, ErrUpstream
	}
	next := withTokens(claimed, tokens)
	if err := r.store.CompleteRefresh(work, hash, next); err != nil {
		slog.ErrorContext(ctx, "store the refreshed tokens", "error", err)
		return Record{}, ErrUpstream
	}
	return withClaim(next, nil), nil
}

func (r *Resolver) releaseFresh(ctx context.Context, hash string, rec Record) (Record, error) {
	r.releaseQuietly(ctx, hash)
	return withClaim(rec, nil), nil
}

func (r *Resolver) releaseQuietly(ctx context.Context, hash string) {
	if err := r.store.ReleaseClaim(ctx, hash); err != nil && !errors.Is(err, ErrNotFound) {
		slog.WarnContext(ctx, "release the refresh claim", "error", err)
	}
}

func (r *Resolver) end(ctx context.Context, hash string) error {
	r.forget(hash)
	if err := r.store.DeleteSession(ctx, hash); err != nil {
		slog.WarnContext(ctx, "delete the ended session", "error", err)
	}
	return ErrSessionEnded
}

func (r *Resolver) remember(hash string, entry cachedRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[hash] = entry
	if len(r.cache) > cachePruneAbove {
		cutoff := r.clock().Add(-cacheMaxAge)
		for key, cached := range r.cache {
			if cached.fetchedAt.Before(cutoff) {
				delete(r.cache, key)
			}
		}
	}
}

func (r *Resolver) forget(hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, hash)
}

func withTokens(rec Record, tokens Tokens) Record {
	rec.Access = tokens.Access
	rec.AccessExpiry = tokens.AccessExpiry
	rec.Refresh = tokens.Refresh
	return rec
}

func principalOf(rec Record) Principal {
	return Principal{
		Subject:     rec.Subject,
		DisplayName: rec.DisplayName,
		Email:       rec.Email,
		Groups:      slices.Clone(rec.Groups),
		Admin:       slices.Contains(rec.Groups, adminGroup),
		Access:      rec.Access,
		CSRF:        rec.CSRF,
		ExpiresAt:   rec.ExpiresAt,
	}
}
