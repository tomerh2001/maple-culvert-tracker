package commands

import (
	"strings"
	"testing"
)

// Members kept trying to submit their own scores because /culvert-help's
// "How scores are added" step read like an instruction to them. The gate must
// stay on the heading line itself, where nobody can miss it.
func TestHelpTextGatesSubmissionStepToAdmins(t *testing.T) {
	heading := ""
	for _, line := range strings.Split(helpText, "\n") {
		if strings.HasPrefix(line, "**3)") {
			heading = line
			break
		}
	}
	if heading == "" {
		t.Fatal("no step 3 heading in helpText")
	}
	if !strings.Contains(heading, "admins & submitters only") {
		t.Fatalf("step 3 heading %q does not say it is admins & submitters only", heading)
	}
	if !strings.Contains(helpText, "You never submit your own scores") {
		t.Error("helpText no longer tells members they do not submit their own scores")
	}
}

// The next-reset line is a placeholder: helpCommand fills it in per render, so
// a stale hard-coded time can never ship in the embed.
func TestHelpTextResetPlaceholderIsRendered(t *testing.T) {
	if !strings.Contains(helpText, "%%RESET%%") {
		t.Fatal("helpText lost its %%RESET%% placeholder")
	}
	rendered := strings.ReplaceAll(helpText, "%%RESET%%", culvertResetLine())
	if strings.Contains(rendered, "%%RESET%%") {
		t.Error("placeholder survived rendering")
	}
	if !strings.HasPrefix(culvertResetLine(), "<t:") {
		t.Errorf("reset line %q is not a Discord timestamp", culvertResetLine())
	}
}
