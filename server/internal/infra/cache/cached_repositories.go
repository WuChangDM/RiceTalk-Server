// Package cacheinfra provides cache wrappers for Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.3 缓存层设计.
//
// CachedUserRepository / CachedChannelRepository / CachedMessageRepository
// wrap a delegate Repository with a TTL cache (decorator pattern). Cache
// policies follow the design doc:
//   - User by ID:        15 minutes (session-scoped reads)
//   - User by Email:     15 minutes
//   - User by Username:  15 minutes
//   - Channel by ID:     10 minutes (metadata)
//   - Channels by Space:  5 minutes (member-list-like reads)
//   - Message by ID:      5 minutes
//
// Writes (Create/Update/Delete) invalidate the corresponding cache entries.
// List-by-channel queries are NOT cached because message streams are
// append-heavy and stale reads would harm UX.
package cacheinfra

import (
	"context"
	"time"

	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

// Cache TTLs per design doc §5B.3.
const (
	userTTL    = 15 * time.Minute
	channelTTL = 10 * time.Minute
	listTTL    = 5 * time.Minute
	messageTTL = 5 * time.Minute
)

// Ensure wrappers implement the corresponding interfaces.
var (
	_ repositories.UserRepository    = (*CachedUserRepository)(nil)
	_ repositories.ChannelRepository = (*CachedChannelRepository)(nil)
	_ repositories.MessageRepository = (*CachedMessageRepository)(nil)
)

// CachedUserRepository wraps a UserRepository with a TTL cache.
type CachedUserRepository struct {
	delegate repositories.UserRepository
	cache    Cache
}

// NewCachedUserRepository wraps delegate with cache.
func NewCachedUserRepository(delegate repositories.UserRepository, cache Cache) *CachedUserRepository {
	return &CachedUserRepository{delegate: delegate, cache: cache}
}

func userKeyByID(id string) string       { return "user:id:" + id }
func userKeyByEmail(email string) string { return "user:email:" + email }
func userKeyByUsername(u string) string  { return "user:username:" + u }

func (r *CachedUserRepository) GetByID(ctx context.Context, id string) (*model.User, error) {
	key := userKeyByID(id)
	if v, ok := r.cache.Get(key); ok {
		if u, ok := v.(*model.User); ok {
			return u, nil
		}
	}
	u, err := r.delegate.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	r.cache.Set(key, u, userTTL)
	return u, nil
}

func (r *CachedUserRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	key := userKeyByEmail(email)
	if v, ok := r.cache.Get(key); ok {
		if u, ok := v.(*model.User); ok {
			return u, nil
		}
	}
	u, err := r.delegate.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	r.cache.Set(key, u, userTTL)
	// Also populate the ID-keyed entry to maximize hit rate.
	r.cache.Set(userKeyByID(u.ID), u, userTTL)
	return u, nil
}

func (r *CachedUserRepository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	key := userKeyByUsername(username)
	if v, ok := r.cache.Get(key); ok {
		if u, ok := v.(*model.User); ok {
			return u, nil
		}
	}
	u, err := r.delegate.GetByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	r.cache.Set(key, u, userTTL)
	r.cache.Set(userKeyByID(u.ID), u, userTTL)
	return u, nil
}

func (r *CachedUserRepository) Create(ctx context.Context, user *model.User) error {
	if err := r.delegate.Create(ctx, user); err != nil {
		return err
	}
	r.cache.Set(userKeyByID(user.ID), user, userTTL)
	return nil
}

func (r *CachedUserRepository) Update(ctx context.Context, user *model.User) error {
	if err := r.delegate.Update(ctx, user); err != nil {
		return err
	}
	r.invalidateUser(user)
	// Re-populate with the fresh value.
	r.cache.Set(userKeyByID(user.ID), user, userTTL)
	return nil
}

func (r *CachedUserRepository) UpdateLastLogin(ctx context.Context, userID string) error {
	if err := r.delegate.UpdateLastLogin(ctx, userID); err != nil {
		return err
	}
	// LastLoginAt change is non-critical for authz decisions; drop the cached
	// row so the next read picks up the new timestamp.
	r.cache.Del(userKeyByID(userID))
	return nil
}

func (r *CachedUserRepository) Delete(ctx context.Context, id string) error {
	if err := r.delegate.Delete(ctx, id); err != nil {
		return err
	}
	r.cache.Del(userKeyByID(id))
	return nil
}

// invalidateUser drops all cache entries that may reference the given user.
func (r *CachedUserRepository) invalidateUser(u *model.User) {
	r.cache.Del(userKeyByID(u.ID))
	r.cache.Del(userKeyByEmail(u.Email))
	r.cache.Del(userKeyByUsername(u.Username))
}

// InvalidateUser is the public cache invalidation entry point for services
// that bypass the Repository (e.g. direct s.db writes in auth.Service.Register).
// It is safe to call with a partially populated User (only ID/Email/Username
// are used for key derivation).
func (r *CachedUserRepository) InvalidateUser(u *model.User) {
	r.invalidateUser(u)
}

// InvalidateUserByID removes the cache entry for a user by ID. Useful when
// only the ID is known (e.g. after a direct db.Delete).
func (r *CachedUserRepository) InvalidateUserByID(id string) {
	r.cache.Del(userKeyByID(id))
}

// CachedChannelRepository wraps a ChannelRepository with a TTL cache.
type CachedChannelRepository struct {
	delegate repositories.ChannelRepository
	cache    Cache
}

// NewCachedChannelRepository wraps delegate with cache.
func NewCachedChannelRepository(delegate repositories.ChannelRepository, cache Cache) *CachedChannelRepository {
	return &CachedChannelRepository{delegate: delegate, cache: cache}
}

func channelKeyByID(id string) string        { return "ch:id:" + id }
func channelKeyBySpace(spaceID string) string { return "ch:space:" + spaceID }

func (r *CachedChannelRepository) GetByID(ctx context.Context, id string) (*model.Channel, error) {
	key := channelKeyByID(id)
	if v, ok := r.cache.Get(key); ok {
		if ch, ok := v.(*model.Channel); ok {
			return ch, nil
		}
	}
	ch, err := r.delegate.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	r.cache.Set(key, ch, channelTTL)
	return ch, nil
}

func (r *CachedChannelRepository) GetBySpaceID(ctx context.Context, spaceID string) ([]model.Channel, error) {
	key := channelKeyBySpace(spaceID)
	if v, ok := r.cache.Get(key); ok {
		if list, ok := v.([]model.Channel); ok {
			return list, nil
		}
	}
	list, err := r.delegate.GetBySpaceID(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	r.cache.Set(key, list, listTTL)
	return list, nil
}

func (r *CachedChannelRepository) Create(ctx context.Context, channel *model.Channel) error {
	if err := r.delegate.Create(ctx, channel); err != nil {
		return err
	}
	r.cache.Set(channelKeyByID(channel.ID), channel, channelTTL)
	// A new channel changes the per-space list; invalidate it.
	r.cache.Del(channelKeyBySpace(channel.SpaceID))
	return nil
}

func (r *CachedChannelRepository) Update(ctx context.Context, channel *model.Channel) error {
	if err := r.delegate.Update(ctx, channel); err != nil {
		return err
	}
	r.cache.Del(channelKeyByID(channel.ID))
	r.cache.Del(channelKeyBySpace(channel.SpaceID))
	// Re-populate the ID-keyed entry with the fresh value.
	r.cache.Set(channelKeyByID(channel.ID), channel, channelTTL)
	return nil
}

func (r *CachedChannelRepository) Delete(ctx context.Context, id string) error {
	// Read the channel first so we know which SpaceID list to invalidate.
	ch, err := r.delegate.GetByID(ctx, id)
	if err != nil {
		// Fall through: best-effort invalidation by ID only.
		r.cache.Del(channelKeyByID(id))
		return r.delegate.Delete(ctx, id)
	}
	if err := r.delegate.Delete(ctx, id); err != nil {
		return err
	}
	r.cache.Del(channelKeyByID(id))
	r.cache.Del(channelKeyBySpace(ch.SpaceID))
	return nil
}

// InvalidateChannel is the public cache invalidation entry point for services
// that bypass the Repository (e.g. direct s.db writes in channel.Service.UpdateChannel).
func (r *CachedChannelRepository) InvalidateChannel(channelID, spaceID string) {
	r.cache.Del(channelKeyByID(channelID))
	if spaceID != "" {
		r.cache.Del(channelKeyBySpace(spaceID))
	}
}

// CachedMessageRepository wraps a MessageRepository with a TTL cache.
// Only GetByID is cached; list queries are pass-through (see package doc).
type CachedMessageRepository struct {
	delegate repositories.MessageRepository
	cache    Cache
}

// NewCachedMessageRepository wraps delegate with cache.
func NewCachedMessageRepository(delegate repositories.MessageRepository, cache Cache) *CachedMessageRepository {
	return &CachedMessageRepository{delegate: delegate, cache: cache}
}

func messageKeyByID(id string) string { return "msg:id:" + id }

func (r *CachedMessageRepository) GetByID(ctx context.Context, id string) (*model.Message, error) {
	key := messageKeyByID(id)
	if v, ok := r.cache.Get(key); ok {
		if m, ok := v.(*model.Message); ok {
			return m, nil
		}
	}
	m, err := r.delegate.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	r.cache.Set(key, m, messageTTL)
	return m, nil
}

// GetByChannelID is pass-through: message streams are append-heavy and stale
// list reads would harm UX. The cache layer still implements the method to
// satisfy the MessageRepository interface.
func (r *CachedMessageRepository) GetByChannelID(ctx context.Context, channelID string, limit, offset int) ([]model.Message, error) {
	return r.delegate.GetByChannelID(ctx, channelID, limit, offset)
}

func (r *CachedMessageRepository) Create(ctx context.Context, message *model.Message) error {
	if err := r.delegate.Create(ctx, message); err != nil {
		return err
	}
	r.cache.Set(messageKeyByID(message.ID), message, messageTTL)
	return nil
}

func (r *CachedMessageRepository) Update(ctx context.Context, message *model.Message) error {
	if err := r.delegate.Update(ctx, message); err != nil {
		return err
	}
	r.cache.Del(messageKeyByID(message.ID))
	r.cache.Set(messageKeyByID(message.ID), message, messageTTL)
	return nil
}

func (r *CachedMessageRepository) Delete(ctx context.Context, id string) error {
	if err := r.delegate.Delete(ctx, id); err != nil {
		return err
	}
	r.cache.Del(messageKeyByID(id))
	return nil
}

// InvalidateMessage is the public cache invalidation entry point for services
// that bypass the Repository (e.g. direct s.db writes in message.Service.CreateMessage).
func (r *CachedMessageRepository) InvalidateMessage(messageID string) {
	r.cache.Del(messageKeyByID(messageID))
}
