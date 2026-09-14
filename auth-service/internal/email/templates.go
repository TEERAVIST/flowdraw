package email

import (
	"fmt"
	"html"
)

type content struct{ subject, text, html string }

func verificationContent(m VerificationMessage) content {
	u := html.EscapeString(m.URL)
	return content{"Verify your email address", fmt.Sprintf("Verify your email address: %s\n\nThis link expires soon and can be used once.", m.URL), fmt.Sprintf(`<p>Verify your email address.</p><p><a href="%s">Verify email</a></p><p>This link expires soon and can be used once.</p>`, u)}
}

func resetContent(m PasswordResetMessage) content {
	u := html.EscapeString(m.URL)
	return content{"Reset your password", fmt.Sprintf("Reset your password: %s\n\nIf you did not request this, ignore this email.", m.URL), fmt.Sprintf(`<p>A password reset was requested.</p><p><a href="%s">Reset password</a></p><p>If you did not request this, ignore this email.</p>`, u)}
}

func alertContent(m SecurityAlertMessage) content {
	e := html.EscapeString(m.Event)
	return content{"Security notice", "Security notice: " + m.Event, `<p>Security notice: ` + e + `</p>`}
}
