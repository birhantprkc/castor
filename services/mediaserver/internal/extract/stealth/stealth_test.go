package stealth

import (
	"regexp"
	"strings"
	"testing"
)

func TestTheScriptFillsEveryPlaceholderAndPatchesToStringFirst(t *testing.T) {
	js, err := New().script()
	if err != nil {
		t.Fatal(err)
	}
	if placeholder := regexp.MustCompile(`__[A-Z_]+__`).FindString(js); placeholder != "" {
		t.Errorf("the script still carries %s", placeholder)
	}
	first, _ := scripts.ReadFile("js/01_tostring.js")
	if !strings.HasPrefix(js, "(() => {\n"+string(first)) {
		t.Error("the toString patch does not run before the patches it makes look native")
	}
}
