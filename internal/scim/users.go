package scim

import (
	"strconv"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// UserNameAttribute is the name attribute of a User resource.
type UserNameAttribute struct {
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
}

// UserEmail is one entry of emails.
type UserEmail struct {
	Value   string `json:"value"`
	Type    string `json:"type"`
	Primary bool   `json:"primary"`
}

// UserMeta is the meta attribute of a User resource. created and
// lastModified are RFC 3339 in UTC.
type UserMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created"`
	LastModified string `json:"lastModified"`
	Location     string `json:"location"`
}

// User is the core User resource (RFC 7643 §4.1) as this provider serves it
// (#469): the account's id, email as userName and as the one work address,
// its names and whether it is active. externalId is omitted when empty. No
// role, password or other attribute is served.
type User struct {
	Schemas    []string          `json:"schemas"`
	ID         string            `json:"id"`
	ExternalID string            `json:"externalId,omitempty"`
	UserName   string            `json:"userName"`
	Name       UserNameAttribute `json:"name"`
	Emails     []UserEmail       `json:"emails"`
	Active     bool              `json:"active"`
	Meta       UserMeta          `json:"meta"`
}

// NewUser builds the User resource of u, with meta.location under baseURL
// (PUBLIC_BASE_URL, trailing slashes dropped) and externalID ("" omits it).
func NewUser(baseURL string, u *models.User, externalID string) User {
	id := strconv.FormatUint(uint64(u.ID), 10)
	return User{
		Schemas:    []string{UserSchema},
		ID:         id,
		ExternalID: externalID,
		UserName:   u.Email,
		Name:       UserNameAttribute{GivenName: u.FirstName, FamilyName: u.LastName},
		Emails:     []UserEmail{{Value: u.Email, Type: "work", Primary: true}},
		Active:     u.IsActive,
		Meta: UserMeta{
			ResourceType: "User",
			Created:      u.CreatedAt.UTC().Format(time.RFC3339),
			LastModified: u.UpdatedAt.UTC().Format(time.RFC3339),
			Location:     location(baseURL, "/Users/"+id),
		},
	}
}

// NewUserList is the ListResponse of one page: totalResults is total,
// itemsPerPage the page's length, startIndex the effective start index.
// Resources is never null.
func NewUserList(total, startIndex int, users []User) ListResponse[User] {
	if users == nil {
		users = []User{}
	}
	return ListResponse[User]{
		Schemas:      []string{ListResponseSchema},
		TotalResults: total,
		ItemsPerPage: len(users),
		StartIndex:   startIndex,
		Resources:    users,
	}
}
