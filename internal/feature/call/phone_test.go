package call

import "testing"

func TestIsPhoneSource(t *testing.T) {
	cases := map[string]bool{
		"phone:jag":    true,
		"phone:hannah": true,
		"phone:":       true,
		"":             false,
		"dot-kitchen":  false,
		"jag-phone":    false,
		"Phone:jag":    false, // convention is lower-case
	}
	for id, want := range cases {
		if got := IsPhoneSource(id); got != want {
			t.Errorf("IsPhoneSource(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestPhoneDisplayName(t *testing.T) {
	if got := PhoneDisplayName("phone:jag"); got != "jag" {
		t.Errorf("PhoneDisplayName(phone:jag) = %q, want %q", got, "jag")
	}
	if got := PhoneDisplayName("phone:"); got != "" {
		t.Errorf("PhoneDisplayName(phone:) = %q, want empty", got)
	}
}