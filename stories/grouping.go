package stories

import "fmt"

// MaxDays is the most days of headlines a Grouping groups (0: all kept).
const MaxDays = 30

// Grouping is the app's grouping settings that the server follows: the text a headline is grouped by, the slider's
// strictness, and the days of headlines grouped (0: all kept).
type Grouping struct {
	Text       StoryText `json:"-"`
	TextName   string    `json:"text"`
	Strictness int       `json:"strictness"`
	Days       int       `json:"days"`
}

// Check says what is out of range.
func (g Grouping) Check() error {
	if g.Strictness < StrictnessMin || g.Strictness > StrictnessMax {
		return fmt.Errorf("strictness is %d; it takes %d to %d", g.Strictness, StrictnessMin, StrictnessMax)
	}
	if g.Days < 0 || g.Days > MaxDays {
		return fmt.Errorf("days is %d; it takes 0 to %d", g.Days, MaxDays)
	}
	if _, ok := storyTextNames[g.Text]; !ok {
		return fmt.Errorf("unknown text %d", g.Text)
	}
	return nil
}
