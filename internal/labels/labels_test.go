package labels_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/labels"
)

// brLabelCharset is the set of bytes br accepts in a label.
func brLabelCharset(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func TestSession_IsThePrefixAndTheID(t *testing.T) {
	require.Equal(t, "session:01a14743-2ec7-79eb-a9ab-39c56ca73589", labels.Session("01a14743-2ec7-79eb-a9ab-39c56ca73589"))
}

func TestUser_EscapesAnEmailIntoBrsCharset(t *testing.T) {
	got := labels.User("bryn@signals.io")
	require.Equal(t, "user:bryn_40signals_2eio", got)
	require.True(t, brLabelCharset(got), "%q is in br's charset", got)
	require.LessOrEqual(t, len(got), labels.MaxLen)
}

func TestUser_RoundTripsAnEmail(t *testing.T) {
	for _, email := range []string{
		"bryn@signals.io",
		"a@b.c",
		"first.last+tag@sub.example.co.uk",
		"UPPER@EXAMPLE.COM",
		"a+b@e.co",
	} {
		t.Run(email, func(t *testing.T) {
			label := labels.User(email)
			require.True(t, brLabelCharset(label), "%q is in br's charset", label)
			require.LessOrEqual(t, len(label), labels.MaxLen)
			back, ok := labels.EmailOf(label)
			require.True(t, ok, "%q decodes", label)
			require.Equal(t, email, back)
		})
	}
}

func TestUser_DistinctEmailsGiveDistinctLabels(t *testing.T) {
	// The escape is injective: no two of these may collide.
	emails := []string{"a@b.c", "a_40b_2ec", "a.b@c", "a@b_c", "a-b@c", "a@b-c"}
	seen := map[string]string{}
	for _, email := range emails {
		label := labels.User(email)
		if other, dup := seen[label]; dup {
			t.Fatalf("%q and %q both give %q", email, other, label)
		}
		seen[label] = email
	}
}

func TestUser_ALongEmailIsCutToFitAndStaysUnique(t *testing.T) {
	long := strings.Repeat("a", 60) + "@example.com"
	label := labels.User(long)
	require.True(t, brLabelCharset(label), "%q is in br's charset", label)
	require.LessOrEqual(t, len(label), labels.MaxLen, "%q fits br's cap", label)
	_, ok := labels.EmailOf(label)
	require.False(t, ok, "a cut label does not decode")

	// A different long email gives a different label.
	other := labels.User(strings.Repeat("a", 60) + "@example.org")
	require.NotEqual(t, label, other)
}

func TestUser_ALongEmailThatSharesAPrefixStillDiffers(t *testing.T) {
	// Two emails whose escaped forms agree for longer than the kept prefix
	// must still differ, because the hash covers the whole email.
	base := strings.Repeat("x", 40)
	a := labels.User(base + "@one.example")
	b := labels.User(base + "@two.example")
	require.NotEqual(t, a, b)
	require.LessOrEqual(t, len(a), labels.MaxLen)
	require.LessOrEqual(t, len(b), labels.MaxLen)
}

func TestUser_TheCapIsExact(t *testing.T) {
	// An email whose escaped form exactly fills the cap is not cut.
	// "user:" is 5 bytes, so 45 bytes of escaped body fit.
	body := strings.Repeat("a", 45)
	label := labels.User(body)
	require.Equal(t, "user:"+body, label)
	require.Len(t, label, labels.MaxLen)

	// One byte more is cut.
	label = labels.User(body + "a")
	require.Len(t, label, labels.MaxLen)
	require.NotEqual(t, "user:"+body+"a", label)
}

func TestEmailOf_RefusesALabelThatIsNotAUserLabel(t *testing.T) {
	for _, label := range []string{"session:abc", "epic:foo", "user", ""} {
		_, ok := labels.EmailOf(label)
		require.False(t, ok, "%q is not a user label", label)
	}
}

func TestSessionOf_ReadsTheID(t *testing.T) {
	id, ok := labels.SessionOf("session:01a14743")
	require.True(t, ok)
	require.Equal(t, "01a14743", id)

	_, ok = labels.SessionOf("user:bryn_40signals_2eio")
	require.False(t, ok)
}

func TestHas_FindsALabel(t *testing.T) {
	held := []string{"epic:x", "session:s1"}
	require.True(t, labels.Has(held, "session:s1"))
	require.False(t, labels.Has(held, "session:s2"))
	require.False(t, labels.Has(nil, "session:s1"))
}
