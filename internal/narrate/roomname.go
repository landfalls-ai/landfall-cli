package narrate

import (
	"strings"
	"unicode"

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
//
// The title is the server's, set by an alert payload or by any member of the
// organization, and it is printed straight into the person's terminal (the
// status line, `landfall rooms`). So it is cleaned before it is kept: see
// Clean.
func RoomNameOf(f *client.ContextFrame) RoomName {
	if f == nil {
		return RoomName{}
	}
	return RoomName{DisplayID: f.Incident.DisplayID, Title: f.Incident.Title}.Clean()
}

// Clean is the name as it may be printed to a terminal: every control
// character removed (see Printable), on one line, bounded. RoomNameOf cleans
// what it reads; a name from anywhere else (a state file, a hook answer from
// another process) is cleaned again where it is printed.
func (n RoomName) Clean() RoomName {
	return RoomName{
		DisplayID: oneLine(Printable(n.DisplayID), nameMax),
		Title:     oneLine(Printable(n.Title), nameMax),
	}
}

// Printable removes what a terminal would act on instead of printing: the C0
// controls (U+0000 to U+001F), DEL (U+007F) and the C1 controls (U+0080 to
// U+009F). ESC and the 8-bit CSI are among them, which is what starts an ANSI
// or OSC sequence that could recolour, retitle or rewrite the person's
// terminal. A control that is whitespace (tab, newline, NEL) becomes a space
// so the words on either side stay apart. Invalid UTF-8 becomes U+FFFD.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			if unicode.IsSpace(r) {
				return ' '
			}
			return -1
		}
		return r
	}, s)
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
	n = n.Clean()
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
