// Package ideas implements Beeloy Ideas: public proposals with a controlled status lifecycle and votes.
package ideas

import (
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	Submitted   Status = "submitted"
	UnderReview Status = "under_review"
	NeedsInfo   Status = "needs_info"
	Planned     Status = "planned"
	InProgress  Status = "in_progress"
	Delivered   Status = "delivered"
	Declined    Status = "declined"
	Duplicate   Status = "duplicate"
)

// transitions lists the allowed status changes (terminal statuses have none).
var transitions = map[Status][]Status{
	Submitted:   {UnderReview, NeedsInfo, Declined, Duplicate},
	NeedsInfo:   {UnderReview, Declined},
	UnderReview: {Planned, NeedsInfo, Declined, Duplicate},
	Planned:     {InProgress, Declined},
	InProgress:  {Delivered, Planned},
}

func CanTransition(from, to Status) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

func (s Status) Terminal() bool { return s == Delivered || s == Declined || s == Duplicate }

var categories = map[string]bool{"product": true, "community": true, "content": true, "other": true}

type Author struct {
	UserID   *uuid.UUID `json:"user_id"`
	Nickname string     `json:"nickname"`
	Slug     string     `json:"slug"`
}

type Idea struct {
	ID              uuid.UUID  `json:"id"`
	Slug            string     `json:"slug"`
	Author          Author     `json:"author"`
	Title           string     `json:"title"`
	Body            string     `json:"body"`
	Category        string     `json:"category"`
	Lang            string     `json:"lang"`
	Status          Status     `json:"status"`
	TeamReply       *string    `json:"team_reply"`
	TeamReplyAt     *time.Time `json:"team_reply_at"`
	DuplicateOfSlug *string    `json:"duplicate_of_slug"`
	VotesCount      int        `json:"votes_count"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ViewerVoted     bool       `json:"viewer_voted"`
}

type HistoryItem struct {
	From *Status   `json:"from"`
	To   Status    `json:"to"`
	Note string    `json:"note"`
	At   time.Time `json:"at"`
}

type ListFilter struct {
	Status   *Status
	Category *string
	Sort     string // "new" (default) | "top"
	AuthorID *uuid.UUID
}

type CreateReq struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Category string `json:"category"`
	Locale   string `json:"locale"`
}

type EditReq struct {
	Title    *string `json:"title"`
	Body     *string `json:"body"`
	Category *string `json:"category"`
}
