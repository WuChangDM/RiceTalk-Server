// Package cacheinfra provides cache wrappers for Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.3 缓存层设计.
package cacheinfra

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

// fakeUserRepo is a stub UserRepository that counts calls for cache verification.
type fakeUserRepo struct {
	getByIDCalls    int
	getByEmailCalls int
	createCalls     int
	updateCalls     int
	deleteCalls     int
	updateLoginCalls int
	user            *model.User
	err             error
}

func (r *fakeUserRepo) GetByID(ctx context.Context, id string) (*model.User, error) {
	r.getByIDCalls++
	if r.err != nil {
		return nil, r.err
	}
	if r.user != nil {
		return r.user, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *fakeUserRepo) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	r.getByEmailCalls++
	if r.err != nil {
		return nil, r.err
	}
	if r.user != nil {
		return r.user, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *fakeUserRepo) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	if r.user != nil {
		return r.user, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *fakeUserRepo) Create(ctx context.Context, user *model.User) error {
	r.createCalls++
	return nil
}

func (r *fakeUserRepo) Update(ctx context.Context, user *model.User) error {
	r.updateCalls++
	return nil
}

func (r *fakeUserRepo) UpdateLastLogin(ctx context.Context, userID string) error {
	r.updateLoginCalls++
	return nil
}

func (r *fakeUserRepo) Delete(ctx context.Context, id string) error {
	r.deleteCalls++
	return nil
}

// Compile-time check: fakeUserRepo implements UserRepository.
var _ repositories.UserRepository = (*fakeUserRepo)(nil)

func TestCachedUserRepository_CacheHit(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeUserRepo{user: &model.User{ID: "u1", Email: "a@b.c", Username: "alice"}}
	cached := NewCachedUserRepository(delegate, cache)

	ctx := context.Background()

	// First call: delegate is hit, cache is populated.
	u1, err := cached.GetByID(ctx, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u1.ID != "u1" {
		t.Fatalf("got user %v, want u1", u1)
	}
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call, got %d", delegate.getByIDCalls)
	}

	// Second call: should be served from cache, no new delegate call.
	u2, err := cached.GetByID(ctx, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u2.ID != "u1" {
		t.Fatalf("got user %v, want u1", u2)
	}
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call (cache hit), got %d", delegate.getByIDCalls)
	}
}

func TestCachedUserRepository_UpdateInvalidates(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeUserRepo{user: &model.User{ID: "u1", Email: "a@b.c", Username: "alice"}}
	cached := NewCachedUserRepository(delegate, cache)

	ctx := context.Background()

	// Populate cache with the original user.
	u1, _ := cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call, got %d", delegate.getByIDCalls)
	}

	// Update should invalidate the stale cache entry and repopulate with the
	// fresh value (design: write-through cache for hot keys).
	updated := &model.User{ID: "u1", Email: "a@b.c", Username: "alice", DisplayName: "Alice v2"}
	if err := cached.Update(ctx, updated); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delegate.updateCalls != 1 {
		t.Fatalf("expected 1 delegate Update call, got %d", delegate.updateCalls)
	}

	// Next GetByID should be a cache hit (repulated by Update), returning the
	// fresh value — NOT the stale one from the first read.
	u2, _ := cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate GetByID call (cache hit after repopulate), got %d", delegate.getByIDCalls)
	}
	if u2.DisplayName != "Alice v2" {
		t.Fatalf("stale cache value after Update: got DisplayName=%q, want %q", u2.DisplayName, "Alice v2")
	}
	// The original pointer should not have been mutated by the cache.
	if u1.DisplayName == "Alice v2" {
		t.Fatal("Update should not mutate the previously-cached pointer in place")
	}
}

func TestCachedUserRepository_DeleteInvalidates(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeUserRepo{user: &model.User{ID: "u1", Email: "a@b.c", Username: "alice"}}
	cached := NewCachedUserRepository(delegate, cache)

	ctx := context.Background()

	// Populate cache.
	_, _ = cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call, got %d", delegate.getByIDCalls)
	}

	// Delete should invalidate the cache entry.
	if err := cached.Delete(ctx, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Next GetByID should hit the delegate again (returns not-found from stub).
	_, _ = cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 2 {
		t.Fatalf("expected 2 delegate calls after delete, got %d", delegate.getByIDCalls)
	}
}

func TestCachedUserRepository_UpdateLastLoginInvalidates(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeUserRepo{user: &model.User{ID: "u1", Email: "a@b.c", Username: "alice"}}
	cached := NewCachedUserRepository(delegate, cache)

	ctx := context.Background()

	// Populate cache.
	_, _ = cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call, got %d", delegate.getByIDCalls)
	}

	// UpdateLastLogin should invalidate the cache entry.
	if err := cached.UpdateLastLogin(ctx, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Next GetByID should hit the delegate again.
	_, _ = cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 2 {
		t.Fatalf("expected 2 delegate calls after UpdateLastLogin, got %d", delegate.getByIDCalls)
	}
}

func TestCachedUserRepository_GetByEmailPopulatesByID(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeUserRepo{user: &model.User{ID: "u1", Email: "a@b.c", Username: "alice"}}
	cached := NewCachedUserRepository(delegate, cache)

	ctx := context.Background()

	// GetByEmail should populate both the email-keyed and ID-keyed entries.
	_, _ = cached.GetByEmail(ctx, "a@b.c")
	if delegate.getByEmailCalls != 1 {
		t.Fatalf("expected 1 GetByEmail call, got %d", delegate.getByEmailCalls)
	}

	// GetByID should be served from cache (populated by GetByEmail).
	_, _ = cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 0 {
		t.Fatalf("expected 0 GetByID calls (cache hit), got %d", delegate.getByIDCalls)
	}
}

// fakeChannelRepo is a stub ChannelRepository for cache verification.
type fakeChannelRepo struct {
	getByIDCalls   int
	getBySpaceCalls int
	createCalls    int
	updateCalls    int
	deleteCalls    int
	ch             *model.Channel
	list           []model.Channel
}

func (r *fakeChannelRepo) GetByID(ctx context.Context, id string) (*model.Channel, error) {
	r.getByIDCalls++
	if r.ch != nil {
		return r.ch, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *fakeChannelRepo) GetBySpaceID(ctx context.Context, spaceID string) ([]model.Channel, error) {
	r.getBySpaceCalls++
	return r.list, nil
}

func (r *fakeChannelRepo) Create(ctx context.Context, channel *model.Channel) error {
	r.createCalls++
	return nil
}

func (r *fakeChannelRepo) Update(ctx context.Context, channel *model.Channel) error {
	r.updateCalls++
	return nil
}

func (r *fakeChannelRepo) Delete(ctx context.Context, id string) error {
	r.deleteCalls++
	return nil
}

var _ repositories.ChannelRepository = (*fakeChannelRepo)(nil)

func TestCachedChannelRepository_CacheHit(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeChannelRepo{ch: &model.Channel{ID: "c1", SpaceID: "s1", Name: "general"}}
	cached := NewCachedChannelRepository(delegate, cache)

	ctx := context.Background()

	_, _ = cached.GetByID(ctx, "c1")
	_, _ = cached.GetByID(ctx, "c1")
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call (cache hit), got %d", delegate.getByIDCalls)
	}
}

func TestCachedChannelRepository_CreateInvalidatesSpaceList(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeChannelRepo{
		ch:   &model.Channel{ID: "c1", SpaceID: "s1", Name: "general"},
		list: []model.Channel{{ID: "c1", SpaceID: "s1", Name: "general"}},
	}
	cached := NewCachedChannelRepository(delegate, cache)

	ctx := context.Background()

	// Populate space-list cache.
	_, _ = cached.GetBySpaceID(ctx, "s1")
	if delegate.getBySpaceCalls != 1 {
		t.Fatalf("expected 1 GetBySpaceID call, got %d", delegate.getBySpaceCalls)
	}

	// Create a new channel in the same space; should invalidate the list.
	if err := cached.Create(ctx, &model.Channel{ID: "c2", SpaceID: "s1", Name: "random"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Next GetBySpaceID should hit the delegate again.
	_, _ = cached.GetBySpaceID(ctx, "s1")
	if delegate.getBySpaceCalls != 2 {
		t.Fatalf("expected 2 GetBySpaceID calls after create, got %d", delegate.getBySpaceCalls)
	}
}

// fakeMessageRepo is a stub MessageRepository for cache verification.
type fakeMessageRepo struct {
	getByIDCalls int
	createCalls  int
	updateCalls  int
	deleteCalls  int
	msg          *model.Message
}

func (r *fakeMessageRepo) GetByID(ctx context.Context, id string) (*model.Message, error) {
	r.getByIDCalls++
	if r.msg != nil {
		return r.msg, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *fakeMessageRepo) GetByChannelID(ctx context.Context, channelID string, limit, offset int) ([]model.Message, error) {
	return nil, nil
}

func (r *fakeMessageRepo) Create(ctx context.Context, message *model.Message) error {
	r.createCalls++
	return nil
}

func (r *fakeMessageRepo) Update(ctx context.Context, message *model.Message) error {
	r.updateCalls++
	return nil
}

func (r *fakeMessageRepo) Delete(ctx context.Context, id string) error {
	r.deleteCalls++
	return nil
}

var _ repositories.MessageRepository = (*fakeMessageRepo)(nil)

func TestCachedMessageRepository_CacheHit(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeMessageRepo{msg: &model.Message{ID: "m1", ChannelID: "c1"}}
	cached := NewCachedMessageRepository(delegate, cache)

	ctx := context.Background()

	_, _ = cached.GetByID(ctx, "m1")
	_, _ = cached.GetByID(ctx, "m1")
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call (cache hit), got %d", delegate.getByIDCalls)
	}
}

func TestCachedMessageRepository_DeleteInvalidates(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeMessageRepo{msg: &model.Message{ID: "m1", ChannelID: "c1"}}
	cached := NewCachedMessageRepository(delegate, cache)

	ctx := context.Background()

	_, _ = cached.GetByID(ctx, "m1")
	if delegate.getByIDCalls != 1 {
		t.Fatalf("expected 1 delegate call, got %d", delegate.getByIDCalls)
	}

	if err := cached.Delete(ctx, "m1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, _ = cached.GetByID(ctx, "m1")
	if delegate.getByIDCalls != 2 {
		t.Fatalf("expected 2 delegate calls after delete, got %d", delegate.getByIDCalls)
	}
}

// TestCachedRepository_ErrorPropagation ensures errors from the delegate are
// returned to the caller and NOT cached.
func TestCachedUserRepository_ErrorNotCached(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	delegate := &fakeUserRepo{err: errors.New("db down")}
	cached := NewCachedUserRepository(delegate, cache)

	ctx := context.Background()

	_, err := cached.GetByID(ctx, "u1")
	if err == nil {
		t.Fatal("expected error from delegate")
	}

	// Second call should also hit the delegate (error not cached).
	_, _ = cached.GetByID(ctx, "u1")
	if delegate.getByIDCalls != 2 {
		t.Fatalf("expected 2 delegate calls (error not cached), got %d", delegate.getByIDCalls)
	}
}
