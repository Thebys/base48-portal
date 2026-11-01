package db

import "strings"

// DisplayName is how the portal names a member: nickname, else real name,
// else e-mail. Blank values count as missing.
func (u User) DisplayName() string {
	if name := strings.TrimSpace(u.Username.String); name != "" {
		return name
	}
	if name := strings.TrimSpace(u.Realname.String); name != "" {
		return name
	}
	return u.Email
}
