package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type scimGroupMemberRepository struct {
	db *gorm.DB
}

// NewSCIMGroupMemberRepository returns the SCIM group membership repository
// (#506) over db.
func NewSCIMGroupMemberRepository(db *gorm.DB) SCIMGroupMemberRepository {
	return &scimGroupMemberRepository{db: db}
}

// WithTx binds the repository to tx.
func (r *scimGroupMemberRepository) WithTx(tx *gorm.DB) SCIMGroupMemberRepository {
	return &scimGroupMemberRepository{db: tx}
}

// ListMemberIDs returns the user ids of every row of groupValue, ascending,
// visible or not.
func (r *scimGroupMemberRepository) ListMemberIDs(groupValue string) ([]uint, error) {
	ids := []uint{}
	if err := r.db.Model(&models.SCIMGroupMember{}).Where("group_value = ?", groupValue).
		Order("user_id ASC").Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// ListVisibleMembers returns, in one query, the visible members of each of
// groupValues by value, id ascending. A visible member is a live users row
// whose role is not customer. A value without visible members is absent.
func (r *scimGroupMemberRepository) ListVisibleMembers(groupValues []string) (map[string][]uint, error) {
	members := map[string][]uint{}
	if len(groupValues) == 0 {
		return members, nil
	}
	var rows []struct {
		GroupValue string
		UserID     uint
	}
	if err := r.db.Table("scim_group_members").
		Select("scim_group_members.group_value, scim_group_members.user_id").
		Joins("JOIN users ON users.id = scim_group_members.user_id").
		Where("scim_group_members.group_value IN ? AND users.deleted_at IS NULL AND users.role <> ?", groupValues, models.RoleCustomer).
		Order("scim_group_members.user_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		members[row.GroupValue] = append(members[row.GroupValue], row.UserID)
	}
	return members, nil
}

// GroupsOfUsers returns the group values of each of userIDs by user, in
// stored order (id ascending). A user without rows is absent.
func (r *scimGroupMemberRepository) GroupsOfUsers(userIDs []uint) (map[uint][]string, error) {
	groups := map[uint][]string{}
	if len(userIDs) == 0 {
		return groups, nil
	}
	var rows []models.SCIMGroupMember
	if err := r.db.Where("user_id IN ?", userIDs).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		groups[row.UserID] = append(groups[row.UserID], row.GroupValue)
	}
	return groups, nil
}

// Add inserts one row per user id; a pair that already exists is kept as it
// is (INSERT ... ON CONFLICT DO NOTHING, INSERT IGNORE on MySQL/MariaDB).
func (r *scimGroupMemberRepository) Add(groupValue string, userIDs []uint) error {
	if len(userIDs) == 0 {
		return nil
	}
	rows := make([]models.SCIMGroupMember, len(userIDs))
	for i, id := range userIDs {
		rows[i] = models.SCIMGroupMember{GroupValue: groupValue, UserID: id}
	}
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// Remove deletes the rows of groupValue for userIDs; a missing row is no
// error.
func (r *scimGroupMemberRepository) Remove(groupValue string, userIDs []uint) error {
	if len(userIDs) == 0 {
		return nil
	}
	return r.db.Where("group_value = ? AND user_id IN ?", groupValue, userIDs).Delete(&models.SCIMGroupMember{}).Error
}
