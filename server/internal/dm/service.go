package dm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/database"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

// Service handles direct-message channels.
type Service struct {
	db     *gorm.DB
	dmRepo repositories.DMRepository
}

// NewService creates a new DM service with a default GORM-backed
// DMRepository (no cache). Use NewServiceWithRepos to inject a mock repository.
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:     db,
		dmRepo: gormrepo.NewGormDMRepository(db),
	}
}

// NewServiceWithRepos creates a new DM service with the given DMRepository.
func NewServiceWithRepos(db *gorm.DB, dmRepo repositories.DMRepository) *Service {
	if dmRepo == nil {
		dmRepo = gormrepo.NewGormDMRepository(db)
	}
	return &Service{db: db, dmRepo: dmRepo}
}

// ListDMChannels returns all DM channels for the given user.
func (s *Service) ListDMChannels(userID string) ([]model.Channel, error) {
	dmChannels, err := s.dmRepo.GetByUserID(context.Background(), userID)
	if err != nil {
		return nil, database.ClassifyError(err)
	}
	if len(dmChannels) == 0 {
		return []model.Channel{}, nil
	}

	channelIDs := make([]string, 0, len(dmChannels))
	for _, dm := range dmChannels {
		channelIDs = append(channelIDs, dm.ChannelID)
	}

	var channels []model.Channel
	if err := s.db.Where("id IN ?", channelIDs).Order("created_at DESC").Find(&channels).Error; err != nil {
		return nil, database.ClassifyError(err)
	}
	return channels, nil
}

// GetOrCreateDMChannel returns the existing DM channel between two users or creates one.
func (s *Service) GetOrCreateDMChannel(userAID, userBID string) (*model.Channel, error) {
	if userAID == userBID {
		return nil, errors.ErrBadRequest.WithDetails("cannot create DM with yourself")
	}

	var result *model.Channel
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var dm model.DMChannel
		if err := tx.Where(
			"(user_aid = ? AND user_bid = ?) OR (user_aid = ? AND user_bid = ?)",
			userAID, userBID, userBID, userAID,
		).First(&dm).Error; err == nil {
			var ch model.Channel
			if err := tx.First(&ch, "id = ?", dm.ChannelID).Error; err != nil {
				return err
			}
			result = &ch
			return nil
		} else if err != gorm.ErrRecordNotFound {
			return err
		}

		space, err := s.getOrCreateDefaultSpace(tx, userAID)
		if err != nil {
			return err
		}

		channelID := idgen.GenerateID(idgen.PrefixChannel)
		ch := &model.Channel{
			ID:         channelID,
			SpaceID:    space.ID,
			Name:       fmt.Sprintf("DM:%s-%s", userAID, userBID),
			Type:       "DM",
			Visibility: "private",
			CreatedBy:  userAID,
		}
		if err := tx.Create(ch).Error; err != nil {
			return err
		}
		dm = model.DMChannel{
			ID:        idgen.GenerateID(idgen.PrefixDM),
			ChannelID: channelID,
			UserAID:   userAID,
			UserBID:   userBID,
		}
		if err := tx.Create(&dm).Error; err != nil {
			return err
		}
		result = ch
		return nil
	})
	if err != nil {
		return nil, database.ClassifyError(err)
	}
	return result, nil
}

// getOrCreateDefaultSpace returns the default space or creates one inside the
// given transaction. With multiple spaces present the default is the
// earliest-created one (deterministic, matching auth.Register's auto-join).
// DM channels need a space ID even when no space has been explicitly
// bootstrapped.
func (s *Service) getOrCreateDefaultSpace(tx *gorm.DB, ownerID string) (*model.Space, error) {
	var space model.Space
	if err := tx.Order("created_at ASC, id ASC").First(&space).Error; err == nil {
		return &space, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	space = model.Space{
		ID:        idgen.GenerateID(idgen.PrefixSpace),
		Name:      "dm",
		OwnerID:   ownerID,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := tx.Create(&space).Error; err != nil {
		return nil, err
	}
	return &space, nil
}

// IsDMChannelParticipant returns true if the user is one of the two DM peers.
func (s *Service) IsDMChannelParticipant(channelID, userID string) (bool, error) {
	count, err := s.dmRepo.CountByChannelAndUser(context.Background(), channelID, userID)
	if err != nil {
		return false, database.ClassifyError(err)
	}
	return count > 0, nil
}
