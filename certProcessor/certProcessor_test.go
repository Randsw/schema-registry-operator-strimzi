package certprocessor

import (
	"testing"
	"unicode"
)

func TestGeneratePasswordLenght(t *testing.T) {
	expectedLenght := 24
	password := GeneratePassword(24, true, false)

	if len(password) != expectedLenght {
		t.Errorf("Output %q not equal to expected %q", len(password), expectedLenght)
		return
	}
	t.Log("Password lenght correct")
}

func TestGeneratePasswordLetter(t *testing.T) {
	result := true
	var badSymbol rune
	password := GeneratePassword(12, false, false)
	for _, r := range password {
		if !unicode.IsLetter(r) {
			result = false
			badSymbol = r
		}
	}
	if !result {
		t.Errorf("Output has not only letter. Bad- %s", string(badSymbol))
		return
	}
	t.Log("Password consist of letters")
}

func TestGeneratePasswordNumber(t *testing.T) {
	result := true
	var badSymbol rune
	password := GeneratePassword(12, true, false)
	for _, r := range password {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			result = false
			badSymbol = r
		}
	}
	if !result {
		t.Errorf("Output has not only letter and digits. Bad - %s", string(badSymbol))
		return
	}
	t.Log("Password consist of letters and digits")
}

func TestGeneratePasswordSpecial(t *testing.T) {
	result := false
	password := GeneratePassword(12, true, true)
	for _, r := range password {
		if !unicode.IsLetter(r) || !unicode.IsDigit(r) {
			result = true
		}
	}
	if !result {
		t.Error("No special symbol in password")
		return
	}
	t.Log("Password has special symbol")
}
