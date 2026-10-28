package personal_debts

import (
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// The one-way split mirror (a linked contact's copy of my split, "เพิ่มในหนี้ของฉัน" /
// "อัปเดตตาม") was retired in 0.4.1-2 S16: a split with a linked contact is a shared bill now
// (projects, BE-1); a plain transaction split just keeps its own debts.

func (s *Service) WithNotifications(n *notifications.Service) { s.notifs = n }
