package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stub struct {
	err   error
	out   string
	name  string
	calls int
}

func (s *stub) Complete(context.Context, Request) (string, error) {
	s.calls++
	return s.out, s.err
}
func (s *stub) Name() string { return s.name }

func TestNewFunc(t *testing.T) {
	c := NewFunc("custom", func(_ context.Context, req Request) (string, error) { return req.System + "/" + req.Prompt, nil })
	out, err := c.Complete(context.Background(), Request{System: "s", Prompt: "p"})
	require.NoError(t, err)
	assert.Equal(t, "s/p", out)
	assert.Equal(t, "custom", c.Name())
}

func TestFallback_UsesThePrimaryWhenItWorks(t *testing.T) {
	primary, secondary := &stub{out: "local", name: "ollama:m"}, &stub{out: "cloud", name: "claude"}
	f := &Fallback{Primary: primary, Secondary: secondary}

	out, err := f.Complete(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "local", out)
	assert.Equal(t, 0, secondary.calls)
	assert.Equal(t, "ollama:m+claude", f.Name())
}

func TestFallback_FallsBackWhileThePrimaryFailsAndRecovers(t *testing.T) {
	primary, secondary := &stub{err: errors.New("down"), name: "ollama:m"}, &stub{out: "cloud", name: "claude"}
	f := &Fallback{Primary: primary, Secondary: secondary}

	for i := 0; i < 3; i++ {
		out, err := f.Complete(context.Background(), Request{})
		require.NoError(t, err)
		assert.Equal(t, "cloud", out)
	}
	assert.True(t, f.failing.Load(), "an outage is remembered, so it is logged once and not per call")
	assert.Equal(t, 3, primary.calls, "the primary is tried every time, so recovery is noticed")

	primary.err, primary.out = nil, "local"
	out, err := f.Complete(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "local", out)
	assert.False(t, f.failing.Load())
}

func TestFallback_BothFailing(t *testing.T) {
	f := &Fallback{Primary: &stub{err: errors.New("down"), name: "a"}, Secondary: &stub{err: errors.New("also down"), name: "b"}}
	_, err := f.Complete(context.Background(), Request{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "also down", "the caller sees the last error")
}

func TestFallback_ACancelledRequestDoesNotTriggerTheFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	secondary := &stub{out: "cloud", name: "claude"}
	f := &Fallback{Primary: &stub{err: ctx.Err(), name: "ollama:m"}, Secondary: secondary}

	_, err := f.Complete(ctx, Request{})
	require.Error(t, err)
	assert.Equal(t, 0, secondary.calls, "a cancelled request would be wasted on the second backend")
	assert.False(t, f.failing.Load(), "and it is not an outage")
}
