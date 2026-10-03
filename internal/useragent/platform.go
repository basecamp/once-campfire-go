package useragent

import "strings"

type Platform struct {
	IOS, Android, Mac, Windows, Chrome, Firefox, Safari, Edge, Mobile, Desktop, AppleMessages bool
	Browser, OperatingSystem                                                                  string
}

func (a Agent) applicationOS() Value {
	if a.Platform.Raised {
		return raised
	}
	for _, pair := range [][2]string{{"Android", "Android"}, {"iPad", "iPad"}, {"iPhone", "iPhone"}, {"Macintosh", "macOS"}, {"Windows", "Windows"}, {"CrOS", "ChromeOS"}} {
		if strings.Contains(a.Platform.Text, pair[0]) {
			return value(pair[1])
		}
	}
	if strings.Contains(a.OS.Text, "Linux") {
		return value("Linux")
	}
	return a.OS
}
func (a Agent) View() Platform {
	p := Platform{IOS: containsAny(a.raw, "iPhone", "iPad"), Android: strings.Contains(a.raw, "Android"), Mac: strings.Contains(a.raw, "Macintosh"), Browser: a.Browser.Text, OperatingSystem: a.applicationOS().Text}
	p.Chrome = strings.Contains(p.Browser, "Chrome")
	p.Firefox = containsAny(p.Browser, "Firefox", "FxiOS")
	p.Safari = strings.Contains(p.Browser, "Safari")
	p.Edge = strings.Contains(p.Browser, "Edg")
	p.Mobile = p.IOS || p.Android
	p.Desktop = !p.Mobile
	p.Windows = p.OperatingSystem == "Windows"
	lower := strings.ToLower(a.raw)
	p.AppleMessages = strings.Contains(lower, "facebookexternalhit") && strings.Contains(lower, "twitterbot")
	return p
}
func (a Agent) Blocked() (bool, bool) {
	if a.Version.Raised {
		return false, true
	}
	if !a.Version.Valid || strings.TrimSpace(a.Version.Text) == "" {
		return false, false
	}
	if a.Browser.Raised || !a.Browser.Valid {
		return false, true
	}
	browser := strings.ToLower(a.Browser.Text)
	minimum, known := map[string]string{"safari": "17.2", "chrome": "120", "firefox": "121", "opera": "104", "internet explorer": ""}[browser]
	return known && (minimum == "" || Compare(a.Version.Text, minimum) < 0) && !a.Bot, false
}
