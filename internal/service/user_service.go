package service

import (
	"errors"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type userService struct {
	userRepo repository.UserRepository
}

func NewUserService(userRepo repository.UserRepository) UserService {
	return &userService{userRepo: userRepo}
}

func (s *userService) Register(user *models.User, password string) error {
	// Check if user already exists
	existing, _ := s.userRepo.GetByEmail(user.Email)
	if existing != nil {
		return errors.New("user with this email already exists")
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user.Password = string(hashedPassword)

	return s.userRepo.Create(user)
}

func (s *userService) GetByID(id uint) (*models.User, error) {
	return s.userRepo.GetByID(id)
}

func (s *userService) GetByEmail(email string) (*models.User, error) {
	return s.userRepo.GetByEmail(email)
}

func (s *userService) Update(id uint, updates map[string]interface{}) (*models.User, error) {
	user, err := s.userRepo.GetByID(id)
	if err != nil {
		return nil, err
	}

	// Check if email is being updated and if it's already taken
	if newEmail, ok := updates["email"].(string); ok && newEmail != user.Email {
		existing, _ := s.userRepo.GetByEmail(newEmail)
		if existing != nil {
			return nil, errors.New("user with this email already exists")
		}
		user.Email = newEmail
	}

	// Apply other updates
	if firstName, ok := updates["first_name"].(string); ok {
		user.FirstName = firstName
	}
	if lastName, ok := updates["last_name"].(string); ok {
		user.LastName = lastName
	}
	if role, ok := updates["role"].(models.UserRole); ok {
		user.Role = role
	}
	if isActive, ok := updates["is_active"].(bool); ok {
		user.IsActive = isActive
	}
	
	// Handle password update
	if password, ok := updates["password"].(string); ok && password != "" {
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		user.Password = string(hashedPassword)
	}

	if err := s.userRepo.Update(user); err != nil {
		return nil, err
	}

	return user, nil
}

func (s *userService) Delete(id uint) error {
	return s.userRepo.Delete(id)
}

func (s *userService) List(offset, limit int) ([]models.User, int64, error) {
	users, err := s.userRepo.List(offset, limit)
	if err != nil {
		return nil, 0, err
	}
	
	total, err := s.userRepo.Count()
	if err != nil {
		return nil, 0, err
	}
	
	return users, total, nil
}