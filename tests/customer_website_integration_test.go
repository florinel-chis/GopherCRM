package tests

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func (suite *CustomerIntegrationTestSuite) TestWebsiteRoundTripAndErasure() {
	w := suite.makeRequestWithAuth("POST", "/api/v1/customers", map[string]string{
		"first_name": "Website", "last_name": "Owner", "email": "website@example.com",
		"website": "  https://example.com/about?q=1  ",
	}, suite.adminToken)
	suite.Require().Equal(http.StatusCreated, w.Code, w.Body.String())
	var response struct{ Data models.Customer }
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	id := response.Data.ID
	path := fmt.Sprintf("/api/v1/customers/%d", id)
	suite.Equal("https://example.com/about?q=1", response.Data.Website)

	checkWebsite := func(want string) {
		w := suite.makeRequestWithAuth("GET", path, nil, suite.adminToken)
		suite.Require().Equal(http.StatusOK, w.Code, w.Body.String())
		suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
		suite.Equal(want, response.Data.Website)
		var stored models.Customer
		suite.Require().NoError(suite.db.First(&stored, id).Error)
		suite.Equal(want, stored.Website)
	}
	checkWebsite("https://example.com/about?q=1")
	for _, change := range []struct {
		body map[string]interface{}
		want string
	}{
		{map[string]interface{}{"website": " http://example.com/new "}, "http://example.com/new"},
		{map[string]interface{}{"phone": "123"}, "http://example.com/new"},
		{map[string]interface{}{"website": nil}, "http://example.com/new"},
		{map[string]interface{}{"website": ""}, ""},
		{map[string]interface{}{"website": "https://example.com"}, "https://example.com"},
	} {
		w := suite.makeRequestWithAuth("PUT", path, change.body, suite.adminToken)
		suite.Require().Equal(http.StatusOK, w.Code, w.Body.String())
		checkWebsite(change.want)
	}

	w = suite.makeRequestWithAuth("GET", "/api/v1/customers", nil, suite.adminToken)
	suite.Require().Equal(http.StatusOK, w.Code)
	suite.Contains(w.Body.String(), `"website":"https://example.com"`)
	w = suite.makeRequestWithAuth("GET", "/api/v1/customers/export", nil, suite.adminToken)
	suite.Require().Equal(http.StatusOK, w.Code)
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	suite.Require().NoError(err)
	suite.Require().Len(rows, 2)
	suite.Equal("website", rows[0][len(rows[0])-1])
	suite.Equal("https://example.com", rows[1][len(rows[1])-1])

	w = suite.makeRequestWithAuth("DELETE", path, nil, suite.adminToken)
	suite.Require().Equal(http.StatusNoContent, w.Code, w.Body.String())
	var erased models.Customer
	suite.Require().NoError(suite.db.Unscoped().First(&erased, id).Error)
	suite.Empty(erased.Website)
	suite.True(erased.DeletedAt.Valid)
}

func (suite *CustomerIntegrationTestSuite) TestInvalidWebsiteDoesNotWrite() {
	customer := suite.seedCustomer(&models.Customer{
		FirstName: "Valid", LastName: "Owner", Email: "valid@example.com", Website: "https://example.com",
	})
	for _, website := range []string{"invalid", "javascript:alert(1)", "ftp://example.com", "https:///path", "https://example.com/" + strings.Repeat("x", 237)} {
		w := suite.makeRequestWithAuth("POST", "/api/v1/customers", map[string]string{
			"first_name": "Invalid", "last_name": "Website", "email": "invalid@example.com", "website": website,
		}, suite.adminToken)
		suite.Equal(http.StatusBadRequest, w.Code, w.Body.String())
		w = suite.makeRequestWithAuth("PUT", fmt.Sprintf("/api/v1/customers/%d", customer.ID), map[string]string{
			"website": website, "first_name": "Changed",
		}, suite.adminToken)
		suite.Equal(http.StatusBadRequest, w.Code, w.Body.String())
	}
	var stored models.Customer
	suite.Require().NoError(suite.db.First(&stored, customer.ID).Error)
	suite.Equal("https://example.com", stored.Website)
	suite.Equal("Valid", stored.FirstName)
	var count int64
	suite.Require().NoError(suite.db.Model(&models.Customer{}).Count(&count).Error)
	suite.Equal(int64(1), count)
}

func (suite *LeadIntegrationTestSuite) TestConversionPersistsWebsiteAndRejectsInvalidWebsite() {
	lead := &models.Lead{
		FirstName: "Converted", LastName: "Owner", Email: "converted-website@example.com",
		OwnerID: suite.adminUser.ID, Status: models.LeadStatusQualified,
	}
	suite.Require().NoError(suite.db.Create(lead).Error)
	path := fmt.Sprintf("/api/v1/leads/%d/convert", lead.ID)
	for _, website := range []string{"javascript:alert(1)", "https://example.com/" + strings.Repeat("x", 237)} {
		w := suite.makeRequestWithAuth("POST", path, map[string]string{"website": website}, suite.adminToken)
		suite.Equal(http.StatusBadRequest, w.Code, w.Body.String())
		var unchanged models.Lead
		suite.Require().NoError(suite.db.First(&unchanged, lead.ID).Error)
		suite.Nil(unchanged.CustomerID)
		suite.Equal(models.LeadStatusQualified, unchanged.Status)
		var count int64
		suite.Require().NoError(suite.db.Model(&models.Customer{}).Where("email = ?", lead.Email).Count(&count).Error)
		suite.Zero(count)
	}
	w := suite.makeRequestWithAuth("POST", path, map[string]string{"website": "  https://converted.example/about  "}, suite.adminToken)
	suite.Require().Equal(http.StatusOK, w.Code, w.Body.String())
	var response struct{ Data models.Customer }
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	suite.Equal("https://converted.example/about", response.Data.Website)
	var stored models.Customer
	suite.Require().NoError(suite.db.First(&stored, response.Data.ID).Error)
	suite.Equal(response.Data.Website, stored.Website)
}
