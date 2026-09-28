package narrate

import (
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// RoomName is how a room is named to a person: the incident's display id
// ("Acme 42", the organization's name and the incident's number, as the web
// app shows it) when the server sent one, and its title. Never the incident's
// UUID, which nobody recognizes and which the web app never shows.
type RoomName struct {
	DisplayID string `json:"displayId,omitempty"`
	Title     string `json:"title,omitempty"`
}

// RoomNameOf reads the name off a context frame. A nil frame names nothing.
func RoomNameOf(f *client.ContextFrame) RoomName {
	if f == nil {
		return RoomName{}
	}
	return RoomName{
		DisplayID: oneLine(f.Incident.DisplayID, nameMax),
		Title:     oneLine(f.Incident.Title, nameMax),
	}
}

// IsZero reports that nothing names the room yet.
func (n RoomName) IsZero() bool { return n.DisplayID == "" && n.Title == "" }

// statusTitleMax bounds a title on the status line, which shares one line of
// the person's terminal with everything else their host draws there.
const statusTitleMax = 40

// StatusHead is the start of the status line for a room: its display id when
// known ("🔴 Acme 42"), else its title ("🔴 landfall: Checkout 5xx spike"),
// else just "🔴 landfall" until the room's brief has been read once.
func (n RoomName) StatusHead() string {
	switch {
	case n.DisplayID != "":
		return "🔴 " + n.DisplayID
	case n.Title != "":
		title := n.Title
		if r := []rune(title); len(r) > statusTitleMax {
			title = strings.TrimRight(string(r[:statusTitleMax-1]), " ") + "…"
		}
		return "🔴 landfall: " + title
	default:
		return "🔴 landfall"
	}
}

// nameMax bounds a name as it is kept: on one line, and short enough that a
// runaway title cannot fill a state file or a hook answer.
const nameMax = 200
