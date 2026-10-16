package channeltest

import (
	"context"
	"slices"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.MemberChecker = (*Checker)(nil)

// Checker is a Channel whose platform can say who belongs to the team, as
// Slack and Discord can: it implements core.MemberChecker.
type Checker struct {
	*Channel

	mu sync.Mutex
	// Members answers CheckMember by external id; a missing id is an outsider.
	Members map[string]core.Membership
	// MemberErr, when set, fails every CheckMember: the platform didn't answer.
	MemberErr error
	// Scope is what MemberScope reports.
	Scope string
	calls []MemberCall
	// Gate, when set, is received from before each CheckMember answers, so
	// a test can hold a lookup in flight.
	Gate chan struct{}
}

// MemberCall is one CheckMember call.
type MemberCall struct {
	ExternalID string
	Teams      []string
}

// NewChecker returns a Checker with Telegram-like caps.
func NewChecker(name string) *Checker {
	return &Checker{Channel: New(name), Members: map[string]core.Membership{}}
}

func (c *Checker) CheckMember(ctx context.Context, externalID string, teams []string) (core.Membership, error) {
	c.mu.Lock()
	c.calls = append(c.calls, MemberCall{ExternalID: externalID, Teams: slices.Clone(teams)})
	gate := c.Gate
	c.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return core.Membership{}, ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.MemberErr != nil {
		return core.Membership{}, c.MemberErr
	}
	m, ok := c.Members[externalID]
	if !ok {
		return core.Membership{Status: core.MemberOutsider}, nil
	}
	return m, nil
}

func (c *Checker) MemberScope() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Scope
}

// SetMember sets what CheckMember says about externalID.
func (c *Checker) SetMember(externalID string, m core.Membership) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Members[externalID] = m
}

// SetMemberErr makes CheckMember fail (nil restores answers).
func (c *Checker) SetMemberErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.MemberErr = err
}

// SetScope changes what MemberScope reports.
func (c *Checker) SetScope(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Scope = s
}

// MemberCalls returns a copy of the CheckMember calls so far.
func (c *Checker) MemberCalls() []MemberCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}
