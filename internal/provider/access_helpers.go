package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

const (
	accessScopeOrganization = "organization"
	accessScopeWorkspace    = "workspace"

	membershipStatusActive  = "active"
	membershipStatusPending = "pending"
)

var errRoleNotFound = errors.New("role not found")

type roleAPI struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description"`
	OrganizationID string   `json:"organization_id"`
	Permissions    []string `json:"permissions"`
	AccessScope    string   `json:"access_scope"`
}

type rolePayload struct {
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type permissionAPI struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	AccessScope string `json:"access_scope"`
}

type organizationAPI struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Handle      string    `json:"handle"`
	CreatedAt   time.Time `json:"created_at"`
}

type memberListQuery struct {
	Limit      int      `query:"limit"`
	Offset     int      `query:"offset"`
	Emails     []string `query:"emails"`
	SortBy     string   `query:"sort_by"`
	SortByDesc bool     `query:"sort_by_desc"`
}

func (q memberListQuery) URLQuery() url.Values {
	values := url.Values{}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		values.Set("offset", strconv.Itoa(q.Offset))
	}
	for _, email := range q.Emails {
		if normalized := normalizeMembershipEmail(email); normalized != "" {
			values.Add("emails", normalized)
		}
	}
	if q.SortBy != "" {
		values.Set("sort_by", q.SortBy)
	}
	if q.SortByDesc {
		values.Set("sort_by_desc", strconv.FormatBool(q.SortByDesc))
	}
	return values
}

type memberIdentityAPI struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	FullName    string   `json:"full_name"`
	DisplayName string   `json:"display_name"`
	AvatarURL   string   `json:"avatar_url"`
	RoleID      string   `json:"role_id"`
	RoleName    string   `json:"role_name"`
	UserID      string   `json:"user_id"`
	LSUserID    string   `json:"ls_user_id"`
	TenantID    string   `json:"tenant_id"`
	TenantIDs   []string `json:"tenant_ids"`
	IsDisabled  bool     `json:"is_disabled"`
}

type pendingIdentityAPI struct {
	ID             string   `json:"id"`
	Email          string   `json:"email"`
	RoleID         string   `json:"role_id"`
	RoleName       string   `json:"role_name"`
	OrgRoleID      string   `json:"org_role_id"`
	OrgRoleName    string   `json:"org_role_name"`
	TenantID       string   `json:"tenant_id"`
	TenantIDs      []string `json:"tenant_ids"`
	OrganizationID string   `json:"organization_id"`
}

type orgInvitePayload struct {
	Email  string `json:"email"`
	RoleID string `json:"role_id,omitempty"`
}

type workspaceMemberPayload struct {
	UserID        string `json:"user_id,omitempty"`
	OrgIdentityID string `json:"org_identity_id,omitempty"`
	LSUserID      string `json:"ls_user_id,omitempty"`
	RoleID        string `json:"role_id,omitempty"`
}

type membershipReadResult struct {
	Status            string
	IdentityID        string
	PendingIdentityID string
	LSUserID          string
	UserID            string
	RoleID            string
	RoleName          string
}

func normalizeMembershipEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func orgMembershipID(email string) string {
	return fmt.Sprintf("org/current/email/%s", normalizeMembershipEmail(email))
}

func workspaceMembershipID(workspaceID string, email string) string {
	return fmt.Sprintf("workspace/%s/email/%s", strings.TrimSpace(workspaceID), normalizeMembershipEmail(email))
}

func workspaceTenantOption(workspaceID string) option.RequestOption {
	return option.WithTenantID(strings.TrimSpace(workspaceID))
}

func findRoleByID(roles []roleAPI, id string, accessScope string) (roleAPI, error) {
	for _, role := range roles {
		if role.ID != id {
			continue
		}
		if role.AccessScope != accessScope {
			return roleAPI{}, fmt.Errorf("role %s is %q scoped, expected %q", id, role.AccessScope, accessScope)
		}
		return role, nil
	}
	return roleAPI{}, fmt.Errorf("%w: role %s was not found", errRoleNotFound, id)
}

func findRoleByLookup(roles []roleAPI, name string, displayName string, accessScope string) (roleAPI, error) {
	name = strings.TrimSpace(name)
	displayName = strings.TrimSpace(displayName)
	if name == "" && displayName == "" {
		return roleAPI{}, errors.New("one of name or display_name must be provided")
	}

	matches := make([]roleAPI, 0, 1)
	for _, role := range roles {
		if role.AccessScope != accessScope {
			continue
		}
		if name != "" && role.Name == name {
			matches = append(matches, role)
			continue
		}
		if displayName != "" && role.DisplayName == displayName {
			matches = append(matches, role)
		}
	}

	switch len(matches) {
	case 0:
		return roleAPI{}, fmt.Errorf("no %s role matched name %q or display_name %q", accessScope, name, displayName)
	case 1:
		role := matches[0]
		if name != "" && displayName != "" && (role.Name != name || role.DisplayName != displayName) {
			return roleAPI{}, fmt.Errorf("no %s role matched both name %q and display_name %q", accessScope, name, displayName)
		}
		return role, nil
	default:
		if name != "" && displayName != "" {
			for _, role := range matches {
				if role.Name == name && role.DisplayName == displayName {
					return role, nil
				}
			}
			return roleAPI{}, fmt.Errorf("no %s role matched both name %q and display_name %q", accessScope, name, displayName)
		}
		return roleAPI{}, fmt.Errorf("multiple %s roles matched name %q or display_name %q", accessScope, name, displayName)
	}
}

func membershipByEmail(active []memberIdentityAPI, pending []pendingIdentityAPI, email string) (membershipReadResult, bool) {
	normalized := normalizeMembershipEmail(email)
	for _, member := range active {
		if normalizeMembershipEmail(member.Email) != normalized || member.IsDisabled {
			continue
		}
		return membershipReadResult{
			Status:     membershipStatusActive,
			IdentityID: member.ID,
			LSUserID:   member.LSUserID,
			UserID:     member.UserID,
			RoleID:     member.RoleID,
			RoleName:   member.RoleName,
		}, true
	}
	for _, invite := range pending {
		if normalizeMembershipEmail(invite.Email) != normalized {
			continue
		}
		return membershipReadResult{
			Status:            membershipStatusPending,
			PendingIdentityID: invite.ID,
			RoleID:            firstNonEmpty(invite.RoleID, invite.OrgRoleID),
			RoleName:          firstNonEmpty(invite.RoleName, invite.OrgRoleName),
		}, true
	}
	return membershipReadResult{}, false
}

func validateRoleScope(ctx context.Context, client *langsmith.Client, roleID string, accessScope string) error {
	roleID = strings.TrimSpace(roleID)
	if roleID == "" {
		return errors.New("role_id must not be empty")
	}
	roles, err := listRoles(ctx, client)
	if err != nil {
		return err
	}
	_, err = findRoleByID(roles, roleID, accessScope)
	return err
}

func readMembershipLists(ctx context.Context, client *langsmith.Client, activePath string, pendingPath string, email string, opts ...option.RequestOption) (membershipReadResult, bool, error) {
	query := memberListQuery{
		Limit:      100,
		Emails:     []string{email},
		SortBy:     "created_at",
		SortByDesc: true,
	}

	var active []memberIdentityAPI
	if err := client.Get(ctx, activePath, query, &active, opts...); err != nil {
		return membershipReadResult{}, false, err
	}

	var pending []pendingIdentityAPI
	if err := client.Get(ctx, pendingPath, query, &pending, opts...); err != nil {
		return membershipReadResult{}, false, err
	}

	result, ok := membershipByEmail(active, pending, email)
	return result, ok, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func listRoles(ctx context.Context, client *langsmith.Client) ([]roleAPI, error) {
	var roles []roleAPI
	if err := client.Get(ctx, "api/v1/orgs/current/roles", nil, &roles); err != nil {
		return nil, err
	}
	return roles, nil
}
