package shared

import (
	"strings"

	"github.com/google/uuid"
)

// BillPerson — one person of an event bill (0.4.0-3, quick create tab อีเวนต์): me, an event member,
// one of my contacts, or a typed name. A contact / name that isn't a member yet is added to the
// event (by any member who may add bills).
type BillPerson struct {
	Me         bool       `json:"me"`
	MemberID   *uuid.UUID `json:"member_id"`
	ContactID  *uuid.UUID `json:"contact_id"`
	PersonName string     `json:"person_name" binding:"omitempty,max=100"`
}

func (p BillPerson) Empty() bool {
	return !p.Me && p.MemberID == nil && p.ContactID == nil && strings.TrimSpace(p.PersonName) == ""
}

// BillMember — a person in the bill and their share.
type BillMember struct {
	BillPerson
	Share float64 `json:"share" binding:"gte=0"`
}

// BillPeople — who paid a bill (one payer; default me) and who is in it with what share. The payer's
// own share is the rest of the bill.
type BillPeople struct {
	Payer   *BillPerson  `json:"payer"   binding:"omitempty"`
	Members []BillMember `json:"members" binding:"omitempty,dive"`
}

func (b BillPeople) Set() bool { return b.Payer != nil || len(b.Members) > 0 }
