package service

import (
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type LabelServiceTestSuite struct {
	suite.Suite
	mockRepo *mocks.LabelRepository
	service  LabelService
}

func (suite *LabelServiceTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
}

func (suite *LabelServiceTestSuite) SetupTest() {
	suite.mockRepo = new(mocks.LabelRepository)
	suite.service = NewLabelService(suite.mockRepo)
}

func (suite *LabelServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
}

func (suite *LabelServiceTestSuite) TestCreate_Success() {
	label := &models.Label{Name: "Urgent", Color: "#FF0000"}

	suite.mockRepo.On("ExistsByNameInsensitive", "Urgent", uint(0)).Return(false, nil)
	suite.mockRepo.On("Create", label).Return(nil).Run(func(args mock.Arguments) {
		args.Get(0).(*models.Label).ID = 7
	})

	err := suite.service.Create(label)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(7), label.ID)
}

// The name is stored exactly as it was validated, so the surrounding
// whitespace has to be gone before the duplicate check runs — otherwise
// " Urgent " would sail past a check that "Urgent" already exists.
func (suite *LabelServiceTestSuite) TestCreate_TrimsNameBeforeCheckingForDuplicates() {
	label := &models.Label{Name: "   Urgent  ", Color: "#ff0000"}

	suite.mockRepo.On("ExistsByNameInsensitive", "Urgent", uint(0)).Return(false, nil)
	suite.mockRepo.On("Create", mock.MatchedBy(func(l *models.Label) bool {
		return l.Name == "Urgent"
	})).Return(nil)

	assert.NoError(suite.T(), suite.service.Create(label))
	assert.Equal(suite.T(), "Urgent", label.Name)
}

func (suite *LabelServiceTestSuite) TestCreate_DuplicateNameIsRejectedCaseInsensitively() {
	label := &models.Label{Name: "urgent", Color: "#FF0000"}

	suite.mockRepo.On("ExistsByNameInsensitive", "urgent", uint(0)).Return(true, nil)

	err := suite.service.Create(label)
	assert.ErrorIs(suite.T(), err, apperrors.ErrDuplicateLabelName)
	suite.mockRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

// The pre-check is advisory; two concurrent creates can both pass it. The
// repository's unique-index error must reach the caller unchanged so the
// handler still answers 409.
func (suite *LabelServiceTestSuite) TestCreate_PropagatesTheRepositoryDuplicateError() {
	label := &models.Label{Name: "Urgent", Color: "#FF0000"}

	suite.mockRepo.On("ExistsByNameInsensitive", "Urgent", uint(0)).Return(false, nil)
	suite.mockRepo.On("Create", mock.Anything).Return(apperrors.ErrDuplicateLabelName)

	err := suite.service.Create(label)
	assert.ErrorIs(suite.T(), err, apperrors.ErrDuplicateLabelName)
}

func (suite *LabelServiceTestSuite) TestCreate_RejectsABlankName() {
	err := suite.service.Create(&models.Label{Name: "   ", Color: "#FF0000"})
	assert.ErrorIs(suite.T(), err, apperrors.ErrInvalidLabelName)
	suite.mockRepo.AssertNotCalled(suite.T(), "ExistsByNameInsensitive", mock.Anything, mock.Anything)
}

func (suite *LabelServiceTestSuite) TestCreate_RejectsANameLongerThanTheColumn() {
	name := ""
	for i := 0; i < 51; i++ {
		name += "x"
	}

	err := suite.service.Create(&models.Label{Name: name, Color: "#FF0000"})
	assert.ErrorIs(suite.T(), err, apperrors.ErrInvalidLabelName)
}

func (suite *LabelServiceTestSuite) TestCreate_RejectsMalformedColours() {
	for _, colour := range []string{"", "FF0000", "#FFF", "#GGGGGG", "#FF00000", "red"} {
		err := suite.service.Create(&models.Label{Name: "Colourful", Color: colour})
		assert.ErrorIsf(suite.T(), err, apperrors.ErrInvalidLabelColor, "colour %q must be rejected", colour)
	}
}

func (suite *LabelServiceTestSuite) TestCreate_AcceptsBothHexCases() {
	for _, colour := range []string{"#ff8800", "#FF8800", "#Ff8800"} {
		label := &models.Label{Name: "Mixed", Color: colour}
		suite.mockRepo.On("ExistsByNameInsensitive", "Mixed", uint(0)).Return(false, nil).Once()
		suite.mockRepo.On("Create", mock.Anything).Return(nil).Once()

		assert.NoErrorf(suite.T(), suite.service.Create(label), "colour %q must be accepted", colour)
	}
}

func (suite *LabelServiceTestSuite) TestUpdate_AllowsTheLabelToKeepItsOwnName() {
	label := &models.Label{BaseModel: models.BaseModel{ID: 3}, Name: "Urgent", Color: "#00FF00"}

	// The excludeID is what lets a pure recolour through.
	suite.mockRepo.On("ExistsByNameInsensitive", "Urgent", uint(3)).Return(false, nil)
	suite.mockRepo.On("Update", label).Return(nil)

	assert.NoError(suite.T(), suite.service.Update(label))
}

func (suite *LabelServiceTestSuite) TestUpdate_RejectsAnotherLabelsName() {
	label := &models.Label{BaseModel: models.BaseModel{ID: 3}, Name: "Taken", Color: "#00FF00"}

	suite.mockRepo.On("ExistsByNameInsensitive", "Taken", uint(3)).Return(true, nil)

	err := suite.service.Update(label)
	assert.ErrorIs(suite.T(), err, apperrors.ErrDuplicateLabelName)
	suite.mockRepo.AssertNotCalled(suite.T(), "Update", mock.Anything)
}

func (suite *LabelServiceTestSuite) TestGetByID_MissingRowIsNotFound() {
	suite.mockRepo.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound)

	label, err := suite.service.GetByID(9)
	assert.Nil(suite.T(), label)
	assert.True(suite.T(), apperrors.IsNotFound(err))
}

// A failing database and a missing row are different outcomes: only the latter
// may be reported as not-found, or a broken connection would answer 404.
func (suite *LabelServiceTestSuite) TestGetByID_DatabaseFailureIsNotSwallowedAsNotFound() {
	suite.mockRepo.On("GetByID", uint(9)).Return(nil, errors.New("connection refused"))

	_, err := suite.service.GetByID(9)
	assert.Error(suite.T(), err)
	assert.False(suite.T(), apperrors.IsNotFound(err))
}

func (suite *LabelServiceTestSuite) TestDelete_Success() {
	suite.mockRepo.On("GetByID", uint(4)).Return(&models.Label{BaseModel: models.BaseModel{ID: 4}}, nil)
	suite.mockRepo.On("Delete", uint(4)).Return(nil)

	assert.NoError(suite.T(), suite.service.Delete(4))
}

func (suite *LabelServiceTestSuite) TestDelete_MissingLabelIsNotFound() {
	suite.mockRepo.On("GetByID", uint(4)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Delete(4)
	assert.True(suite.T(), apperrors.IsNotFound(err))
	suite.mockRepo.AssertNotCalled(suite.T(), "Delete", mock.Anything)
}

func (suite *LabelServiceTestSuite) TestList_NeverReturnsANilSlice() {
	suite.mockRepo.On("List").Return(nil, nil)

	labels, err := suite.service.List()
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), labels)
	assert.Empty(suite.T(), labels)
}

func (suite *LabelServiceTestSuite) TestList_PassesThroughOrderAndCounts() {
	suite.mockRepo.On("List").Return([]models.Label{
		{BaseModel: models.BaseModel{ID: 1}, Name: "alpha", Color: "#111111", TaskCount: 3},
		{BaseModel: models.BaseModel{ID: 2}, Name: "beta", Color: "#222222"},
	}, nil)

	labels, err := suite.service.List()
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), labels, 2)
	assert.Equal(suite.T(), int64(3), labels[0].TaskCount)
}

func TestLabelServiceTestSuite(t *testing.T) {
	suite.Run(t, new(LabelServiceTestSuite))
}
