package service

import (
	"errors"
	"testing"

	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/repository/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/bcrypt"
)

type UserServiceTestSuite struct {
	suite.Suite
	mockRepo    *mocks.UserRepository
	userService UserService
}

func (suite *UserServiceTestSuite) SetupTest() {
	suite.mockRepo = new(mocks.UserRepository)
	suite.userService = NewUserService(suite.mockRepo)
}

func (suite *UserServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
}

func (suite *UserServiceTestSuite) TestRegister_Success() {
	user := &models.User{
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
		Role:      models.RoleCustomer,
	}

	suite.mockRepo.On("GetByEmail", user.Email).Return(nil, errors.New("not found"))
	suite.mockRepo.On("Create", mock.MatchedBy(func(u *models.User) bool {
		// Verify password is hashed
		err := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte("password123"))
		return err == nil && u.Email == user.Email
	})).Return(nil)

	err := suite.userService.Register(user, "password123")
	assert.NoError(suite.T(), err)
}

func (suite *UserServiceTestSuite) TestRegister_UserExists() {
	existingUser := &models.User{
		Email: "existing@example.com",
	}

	newUser := &models.User{
		Email:     "existing@example.com",
		FirstName: "Test",
		LastName:  "User",
	}

	suite.mockRepo.On("GetByEmail", existingUser.Email).Return(existingUser, nil)

	err := suite.userService.Register(newUser, "password123")
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "user with this email already exists", err.Error())
}

func (suite *UserServiceTestSuite) TestGetByID_Success() {
	expectedUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
	}

	suite.mockRepo.On("GetByID", uint(1)).Return(expectedUser, nil)

	user, err := suite.userService.GetByID(1)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedUser, user)
}

func (suite *UserServiceTestSuite) TestGetByID_NotFound() {
	suite.mockRepo.On("GetByID", uint(999)).Return(nil, errors.New("not found"))

	user, err := suite.userService.GetByID(999)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), user)
}

func (suite *UserServiceTestSuite) TestGetByEmail_Success() {
	expectedUser := &models.User{
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
	}

	suite.mockRepo.On("GetByEmail", "test@example.com").Return(expectedUser, nil)

	user, err := suite.userService.GetByEmail("test@example.com")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedUser, user)
}

func (suite *UserServiceTestSuite) TestUpdate_Success() {
	existingUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}

	updates := map[string]interface{}{
		"first_name": "Updated",
		"last_name":  "Name",
	}

	suite.mockRepo.On("GetByID", uint(1)).Return(existingUser, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(u *models.User) bool {
		return u.FirstName == "Updated" && u.LastName == "Name"
	})).Return(nil)

	user, err := suite.userService.Update(1, updates)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Updated", user.FirstName)
	assert.Equal(suite.T(), "Name", user.LastName)
}

func (suite *UserServiceTestSuite) TestUpdate_EmailConflict() {
	existingUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
	}

	conflictingUser := &models.User{
		BaseModel: models.BaseModel{ID: 2},
		Email:     "conflict@example.com",
	}

	updates := map[string]interface{}{
		"email": "conflict@example.com",
	}

	suite.mockRepo.On("GetByID", uint(1)).Return(existingUser, nil)
	suite.mockRepo.On("GetByEmail", "conflict@example.com").Return(conflictingUser, nil)

	user, err := suite.userService.Update(1, updates)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "user with this email already exists", err.Error())
	assert.Nil(suite.T(), user)
}

func (suite *UserServiceTestSuite) TestUpdate_PasswordChange() {
	existingUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		Password:  "old_hash",
	}

	updates := map[string]interface{}{
		"password": "newpassword123",
	}

	suite.mockRepo.On("GetByID", uint(1)).Return(existingUser, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(u *models.User) bool {
		// Verify new password is properly hashed
		err := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte("newpassword123"))
		return err == nil
	})).Return(nil)

	user, err := suite.userService.Update(1, updates)
	assert.NoError(suite.T(), err)
	assert.NotEqual(suite.T(), "old_hash", user.Password)
	assert.NotEqual(suite.T(), "newpassword123", user.Password) // Should be hashed
}

func (suite *UserServiceTestSuite) TestDelete_Success() {
	suite.mockRepo.On("Delete", uint(1)).Return(nil)

	err := suite.userService.Delete(1)
	assert.NoError(suite.T(), err)
}

func (suite *UserServiceTestSuite) TestList_Success() {
	expectedUsers := []models.User{
		{BaseModel: models.BaseModel{ID: 1}, Email: "user1@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, Email: "user2@example.com"},
	}

	suite.mockRepo.On("List", 0, 10).Return(expectedUsers, nil)
	suite.mockRepo.On("Count").Return(int64(2), nil)

	users, total, err := suite.userService.List(0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedUsers, users)
	assert.Equal(suite.T(), int64(2), total)
}

func (suite *UserServiceTestSuite) TestList_Error() {
	suite.mockRepo.On("List", 0, 10).Return(nil, errors.New("database error"))

	users, total, err := suite.userService.List(0, 10)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), users)
	assert.Equal(suite.T(), int64(0), total)
}

func TestUserServiceTestSuite(t *testing.T) {
	suite.Run(t, new(UserServiceTestSuite))
}