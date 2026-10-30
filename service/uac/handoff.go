package uac

import (
	"time"
)

type Handoff struct {
	Data           any
	Session        *Session
	ChallengeToken string
	Expires        time.Time
}

func (c *Client) HoldLogin(data any, challengeToken string, session *Session) (string, error) {
	ticket, err := Random()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, previous := range c.handoffs {
		if time.Now().After(previous.Expires) {
			delete(c.handoffs, key)
		}
	}
	if len(c.handoffs) >= 5000 {
		return "", ErrAuthentication
	}
	c.handoffs[ticket] = Handoff{Data: data, ChallengeToken: challengeToken, Session: session, Expires: time.Now().Add(5 * time.Minute)}
	return ticket, nil
}

func (c *Client) TakeLogin(ticket string) (*Handoff, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	handoff, ok := c.handoffs[ticket]
	delete(c.handoffs, ticket)
	if !ok || time.Now().After(handoff.Expires) {
		return nil, ErrAuthentication
	}
	if handoff.ChallengeToken != "" {
		c.handoffs["challenge:"+handoff.ChallengeToken] = handoff
	}
	return &handoff, nil
}

func (c *Client) FinishChallenge(challengeToken, sid string) error {
	c.mu.Lock()
	handoff, ok := c.handoffs["challenge:"+challengeToken]
	delete(c.handoffs, "challenge:"+challengeToken)
	c.mu.Unlock()
	if !ok || time.Now().After(handoff.Expires) {
		return ErrAuthentication
	}
	return c.Attach(sid, handoff.Session)
}
