package game

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// characterFields are the editable fields of a character card. Nil means
// "not given": create uses a default, update leaves the field alone.
type characterFields struct {
	Kind         *ActorKind `json:"kind"`
	Name         *string    `json:"name"`
	Class        *string    `json:"class"`
	Level        *int       `json:"level"`
	AC           *int       `json:"ac"`
	Speed        *int       `json:"speed"`
	InitBonus    *int       `json:"init_bonus"`
	MaxHP        *int       `json:"max_hp"`
	Controllers  *[]UserID  `json:"controllers"`
	RollsOwnDice *bool      `json:"rolls_own_dice"`
}

const (
	maxNameLen = 32
	maxHP      = 9999
)

// set validates the given fields and writes them to c.
func (f characterFields) set(s *State, c *Character) error {
	if f.Kind != nil {
		if k := *f.Kind; k != KindPC && k != KindNPC && k != KindMonster {
			return reject(CodeInvalidTarget, "kind must be pc, npc, or monster")
		}
		c.Kind = *f.Kind
	}
	if f.Name != nil {
		name := strings.TrimSpace(*f.Name)
		if name == "" || utf8.RuneCountInString(name) > maxNameLen {
			return reject(CodeInvalidTarget, "name must be 1 to 32 characters")
		}
		c.Name = name
	}
	if f.Class != nil {
		class := strings.TrimSpace(*f.Class)
		if utf8.RuneCountInString(class) > maxNameLen {
			return reject(CodeInvalidTarget, "class must be at most 32 characters")
		}
		c.Class = class
	}
	ints := []struct {
		v       *int
		dst     *int
		lo, hi  int
		message string
	}{
		{f.Level, &c.Level, 0, 30, "level must be between 0 and 30"},
		{f.AC, &c.AC, 0, 40, "AC must be between 0 and 40"},
		{f.Speed, &c.Speed, 0, 200, "speed must be between 0 and 200 feet"},
		{f.InitBonus, &c.InitBonus, -10, 20, "initiative bonus must be between -10 and +20"},
		{f.MaxHP, &c.HP.Max, 1, maxHP, "max HP must be between 1 and 9999"},
	}
	for _, i := range ints {
		if i.v == nil {
			continue
		}
		if *i.v < i.lo || *i.v > i.hi {
			return reject(CodeInvalidTarget, i.message)
		}
		*i.dst = *i.v
	}
	c.HP.Current = min(c.HP.Current, c.HP.Max) // a lowered max caps current HP
	if f.Controllers != nil {
		controllers := []UserID{}
		for _, u := range *f.Controllers {
			if s.Members[u] == nil {
				return reject(CodeInvalidTarget, "controller "+string(u)+" is not a member")
			}
			if !slices.Contains(controllers, u) {
				controllers = append(controllers, u)
			}
		}
		c.Controllers = controllers
	}
	if f.RollsOwnDice != nil {
		c.RollsOwnDice = *f.RollsOwnDice
	}
	return nil
}

func decideCreateCharacter(s *State, cmd Command, env Env) ([]Payload, error) {
	var f characterFields
	if err := decodeArgs(cmd, &f); err != nil {
		return nil, err
	}
	c := Character{
		ID: ActorID(env.NewID("act")), Kind: KindMonster, Level: 1, AC: 10, Speed: 30,
		Conditions: []Condition{}, Controllers: []UserID{},
	}
	if !s.IsDM(cmd.By) {
		// Players make their own PC and nothing else.
		if (f.Kind != nil && *f.Kind != KindPC) || f.Controllers != nil {
			return nil, reject(CodeForbidden, "players can only create their own PC")
		}
		c.Kind, c.Controllers = KindPC, []UserID{cmd.By}
	}
	if f.Name == nil || f.MaxHP == nil {
		return nil, reject(CodeInvalidTarget, "name and max HP are required")
	}
	if err := f.set(s, &c); err != nil {
		return nil, err
	}
	c.HP.Current = c.HP.Max
	return []Payload{CharacterCreated{Character: c}}, nil
}

type updateCharacterArgs struct {
	Actor ActorID `json:"actor"`
	characterFields
}

func decideUpdateCharacter(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a updateCharacterArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	cur, err := editable(s, cmd.By, a.Actor)
	if err != nil {
		return nil, err
	}
	if !s.IsDM(cmd.By) && (a.Kind != nil || a.Controllers != nil) {
		return nil, reject(CodeForbidden, "only the DM can change kind or controllers")
	}
	c := *cloneCharacter(*cur)
	if err := a.set(s, &c); err != nil {
		return nil, err
	}
	return []Payload{CharacterUpdated{Character: c}}, nil
}

type actorArgs struct {
	Actor ActorID `json:"actor"`
}

func decideDeleteCharacter(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can delete characters")
	}
	var a actorArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if s.Actors[a.Actor] == nil {
		return nil, reject(CodeInvalidTarget, "no such character")
	}
	var evs []Payload
	if t := s.TokenFor(a.Actor); t != nil {
		evs = append(evs, TokenRemoved{Token: t.ID})
	}
	return append(evs, CharacterDeleted{Actor: a.Actor}), nil
}

// editable returns the character if user may edit it.
func editable(s *State, user UserID, id ActorID) (*Character, error) {
	c := s.Actors[id]
	if c == nil {
		return nil, reject(CodeInvalidTarget, "no such character")
	}
	if !s.CanEdit(user, c) {
		return nil, reject(CodeForbidden, "that is not your character")
	}
	return c, nil
}

type adjustHPArgs struct {
	Actor ActorID `json:"actor"`
	Delta int     `json:"delta"` // negative for damage
}

func decideAdjustHP(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a adjustHPArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	c, err := editable(s, cmd.By, a.Actor)
	if err != nil {
		return nil, err
	}
	if a.Delta == 0 || a.Delta < -maxHP || a.Delta > maxHP {
		return nil, reject(CodeInvalidTarget, "delta must be between -9999 and 9999, and not 0")
	}
	hp := c.HP.Heal(a.Delta)
	if a.Delta < 0 {
		hp = c.HP.Damage(-a.Delta)
	}
	return []Payload{HPChanged{Actor: c.ID, HP: hp, Delta: a.Delta}}, nil
}

type setTempHPArgs struct {
	Actor ActorID `json:"actor"`
	Temp  int     `json:"temp"`
}

// decideSetTempHP grants temporary HP. They don't stack: the higher of the
// old and new amounts is kept, per the rules. 0 clears them.
func decideSetTempHP(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a setTempHPArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	c, err := editable(s, cmd.By, a.Actor)
	if err != nil {
		return nil, err
	}
	if a.Temp < 0 || a.Temp > maxHP {
		return nil, reject(CodeInvalidTarget, "temp HP must be between 0 and 9999")
	}
	hp := c.HP
	hp.Temp = 0
	if a.Temp > 0 {
		hp.Temp = max(c.HP.Temp, a.Temp)
	}
	return []Payload{HPChanged{Actor: c.ID, HP: hp}}, nil
}

type conditionArgs struct {
	Actor  ActorID `json:"actor"`
	Name   string  `json:"name"`
	Source string  `json:"source"`
}

func decideAddCondition(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a conditionArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	c, err := editable(s, cmd.By, a.Actor)
	if err != nil {
		return nil, err
	}
	name, source := strings.TrimSpace(a.Name), strings.TrimSpace(a.Source)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen || utf8.RuneCountInString(source) > maxNameLen {
		return nil, reject(CodeInvalidTarget, "condition and source must be at most 32 characters")
	}
	if findCondition(c, name) >= 0 {
		return nil, reject(CodeInvalidTarget, c.Name+" is already "+name)
	}
	return []Payload{ConditionAdded{Actor: c.ID, Condition: Condition{Name: name, Source: source}}}, nil
}

func decideRemoveCondition(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a conditionArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	c, err := editable(s, cmd.By, a.Actor)
	if err != nil {
		return nil, err
	}
	i := findCondition(c, strings.TrimSpace(a.Name))
	if i < 0 {
		return nil, reject(CodeInvalidTarget, c.Name+" is not "+a.Name)
	}
	return []Payload{ConditionRemoved{Actor: c.ID, Name: c.Conditions[i].Name}}, nil
}

// findCondition matches names case-insensitively, so "prone" finds "Prone".
func findCondition(c *Character, name string) int {
	return slices.IndexFunc(c.Conditions, func(x Condition) bool { return strings.EqualFold(x.Name, name) })
}
