package service

import (
	"context"

	"github.com/HeartBtz/pxl/internal/crypto"
	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/google/uuid"
)

// AlbumService gère la logique métier des albums d'images :
// création avec short ID unique, listage, ajout/retrait d'images.
type AlbumService struct {
	albumRepo *repository.AlbumRepository
	imageRepo *repository.ImageRepository
}

// NewAlbumService crée un nouveau service d'albums.
func NewAlbumService(ar *repository.AlbumRepository, ir *repository.ImageRepository) *AlbumService {
	return &AlbumService{albumRepo: ar, imageRepo: ir}
}

func (s *AlbumService) Create(ctx context.Context, userID uuid.UUID, req *domain.AlbumRequest) (*domain.Album, error) {
	if req.Title == "" || len(req.Title) > 255 || len(req.Description) > 5000 || len(req.ImageIDs) > 1000 {
		return nil, domain.ErrInvalidInput
	}
	imageIDs := make([]uuid.UUID, 0, len(req.ImageIDs))
	for _, idStr := range req.ImageIDs {
		imgID, err := uuid.Parse(idStr)
		if err != nil {
			return nil, domain.ErrImageNotFound
		}
		img, err := s.imageRepo.GetByID(ctx, imgID)
		if err != nil {
			return nil, err
		}
		if img.UserID == nil || *img.UserID != userID {
			return nil, domain.ErrForbidden
		}
		imageIDs = append(imageIDs, imgID)
	}
	album := &domain.Album{
		ShortID:     crypto.GenerateShortID(8),
		UserID:      userID,
		Title:       req.Title,
		Description: req.Description,
		IsPrivate:   req.IsPrivate,
	}
	if err := s.albumRepo.Create(ctx, album, imageIDs...); err != nil {
		return nil, err
	}

	var err error
	album.Images, err = s.loadImages(ctx, album.ID)
	return album, err
}

func (s *AlbumService) GetByShortID(ctx context.Context, shortID string) (*domain.Album, error) {
	album, err := s.albumRepo.GetByShortID(ctx, shortID)
	if err != nil {
		return nil, err
	}
	album.Images, err = s.loadImages(ctx, album.ID)
	return album, err
}

func (s *AlbumService) ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Album, error) {
	return s.albumRepo.ListByUser(ctx, userID)
}

func (s *AlbumService) AddImage(ctx context.Context, albumShortID string, imageShortID string, userID uuid.UUID) error {
	album, err := s.albumRepo.GetByShortID(ctx, albumShortID)
	if err != nil {
		return err
	}
	if album.UserID != userID {
		return domain.ErrForbidden
	}
	img, err := s.imageRepo.GetByShortID(ctx, imageShortID)
	if err != nil {
		return err
	}
	if img.UserID == nil || *img.UserID != userID {
		return domain.ErrForbidden
	}
	return s.albumRepo.AddImage(ctx, album.ID, img.ID, 0)
}

func (s *AlbumService) RemoveImage(ctx context.Context, albumShortID string, imageShortID string, userID uuid.UUID) error {
	album, err := s.albumRepo.GetByShortID(ctx, albumShortID)
	if err != nil {
		return err
	}
	if album.UserID != userID {
		return domain.ErrForbidden
	}
	img, err := s.imageRepo.GetByShortID(ctx, imageShortID)
	if err != nil {
		return err
	}
	return s.albumRepo.RemoveImage(ctx, album.ID, img.ID)
}

func (s *AlbumService) Delete(ctx context.Context, shortID string, userID uuid.UUID) error {
	album, err := s.albumRepo.GetByShortID(ctx, shortID)
	if err != nil {
		return err
	}
	if album.UserID != userID {
		return domain.ErrForbidden
	}
	return s.albumRepo.Delete(ctx, album.ID)
}

func (s *AlbumService) loadImages(ctx context.Context, albumID uuid.UUID) ([]domain.Image, error) {
	imgs, err := s.albumRepo.GetImages(ctx, albumID)
	if err != nil {
		return nil, err
	}
	result := make([]domain.Image, len(imgs))
	for i, img := range imgs {
		result[i] = *img
	}
	return result, nil
}
