package tg

import (
	"sync"

	tele "gopkg.in/telebot.v4"
)

// FSM step identifiers.
const (
	stepAccLabel    = "acc_label"    // entering account label (scratch: forum)
	stepAccToken    = "acc_token"    // entering lolz token (scratch: forum,label)
	stepAccLogin    = "acc_login"    // entering mipped login (scratch: label)
	stepAccPassword = "acc_password" // entering mipped password (scratch: label,login)

	stepThrRef      = "thr_ref"      // entering thread ref/URL (scratch: accountID,forum)
	stepThrInterval = "thr_interval" // entering interval for new thread (scratch: accountID,forum,ref)

	stepEditInterval = "edit_interval" // editing a thread's interval (scratch: threadID)
	stepEditProxy    = "edit_proxy"    // editing an account proxy (scratch: accountID)
	stepEditCookies  = "edit_cookies"  // pasting Mipped session cookies (scratch: accountID)
	stepEditSetting  = "edit_setting"  // editing a setting (scratch: settingKey)
)

// state holds the in-progress input for one user.
type state struct {
	step       string
	forum      string
	label      string
	login      string
	ref        string
	accountID  int64
	threadID   int64
	settingKey string
}

type fsmStore struct {
	mu     sync.Mutex
	states map[int64]*state
}

func newFSM() *fsmStore { return &fsmStore{states: make(map[int64]*state)} }

func (f *fsmStore) get(id int64) *state {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[id]
}

func (f *fsmStore) set(id int64, s *state) {
	f.mu.Lock()
	f.states[id] = s
	f.mu.Unlock()
}

func (f *fsmStore) clear(id int64) {
	f.mu.Lock()
	delete(f.states, id)
	f.mu.Unlock()
}

// onText routes free-form messages according to the user's FSM state. With no
// active state, it shows the main menu.
func (tb *Bot) onText(c tele.Context) error {
	s := c.Sender()
	if s == nil {
		return nil
	}
	st := tb.fsm.get(s.ID)
	if st == nil {
		return tb.showMain(c)
	}
	switch st.step {
	case stepAccLabel:
		return tb.inputAccLabel(c, st)
	case stepAccToken:
		return tb.inputAccToken(c, st)
	case stepAccLogin:
		return tb.inputAccLogin(c, st)
	case stepAccPassword:
		return tb.inputAccPassword(c, st)
	case stepThrRef:
		return tb.inputThrRef(c, st)
	case stepThrInterval:
		return tb.inputThrInterval(c, st)
	case stepEditInterval:
		return tb.inputEditInterval(c, st)
	case stepEditProxy:
		return tb.inputEditProxy(c, st)
	case stepEditCookies:
		return tb.inputEditCookies(c, st)
	case stepEditSetting:
		return tb.inputEditSetting(c, st)
	default:
		tb.fsm.clear(s.ID)
		return tb.showMain(c)
	}
}

// cancelRow builds a one-button row that aborts input and goes to `unique`.
func cancelRow(m *tele.ReplyMarkup, unique string, args ...string) tele.Row {
	return m.Row(m.Data("⬅️ Отмена", unique, args...))
}
