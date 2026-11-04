package identity

import "strings"

type otpText struct{ Subject, Body string }

// Built-in texts for the sign-in e-mail (not taken from the i18n bundles). Fallback: en.
var otpTexts = map[string]otpText{
	"en": {"Your Beeloy sign-in code", "Your Beeloy code: {code}\n\nIt is valid for {minutes} minutes. If you did not request it, ignore this message."},
	"bg": {"Вашият код за вход в Beeloy", "Вашият код за Beeloy: {code}\n\nВалиден е {minutes} минути. Ако не сте го заявявали, игнорирайте това съобщение."},
	"ru": {"Ваш код для входа в Beeloy", "Ваш код Beeloy: {code}\n\nОн действует {minutes} минут. Если вы его не запрашивали, проигнорируйте это письмо."},
	"uk": {"Ваш код для входу в Beeloy", "Ваш код Beeloy: {code}\n\nВін дійсний {minutes} хвилин. Якщо ви його не запитували, проігноруйте цей лист."},
}

// OTPMessage renders subject and body for the locale.
func OTPMessage(locale, code string, minutes string) (subject, body string) {
	t, ok := otpTexts[locale]
	if !ok {
		t = otpTexts["en"]
	}
	body = strings.NewReplacer("{code}", code, "{minutes}", minutes).Replace(t.Body)
	return t.Subject, body
}
