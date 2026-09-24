package auth

// Role is a member's role in a session.
type Role string

const (
	RoleDM        Role = "dm"
	RolePlayer    Role = "player"
	RoleSpectator Role = "spectator"
)

func (r Role) Valid() bool {
	switch r {
	case RoleDM, RolePlayer, RoleSpectator:
		return true
	}
	return false
}

// DefaultCaps lists the capabilities a role starts with. A player's
// token:control and actor:edit capabilities depend on which character they
// control, so they are granted by the session rather than listed here.
func (r Role) DefaultCaps() []string {
	switch r {
	case RoleDM:
		return []string{
			"session:admin", "token:control:*", "actor:edit:*", "fog:edit",
			"dice:roll", "dice:secret", "encounter:manage", "view:all",
		}
	case RolePlayer:
		return []string{"dice:roll"}
	}
	return []string{}
}
