package views

import (
	"encoding/json"
	"testing"
)

// users/avatars/show.svg as the reference renders it (tests/golden/a/avatar_three_initials.svg).
func TestAvatarSvgFitsThreeInitials(t *testing.T) {
	want := `<svg version="1.1" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"
  viewBox="0 0 512 512" class="avatar" aria-hidden="true">
  <defs>
    <clipPath id="porthole">
      <circle cx="50%" cy="50%" r="50%" />
    </clipPath>
  </defs>

  <g>
    <rect width="100%" height="100%" rx="50" fill="#5D618F" />

    <text x="50%" y="50%" fill="#FFFFFF"
      text-anchor="middle" dy="0.35em"
      textLength="85%" lengthAdjust="spacingAndGlyphs"
      font-family="-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Helvetica, Arial, sans-serif"
      font-size="230"
      font-weight="800"
      letter-spacing="-5">
      ABC
    </text>
  </g>
</svg>
`
	if got := UsersAvatarsShowSvg(773523956, Initials("Anna Bea Cole")); got != want {
		t.Errorf("got %q", got)
	}
}

// The reference's pwa_manifest_is_valid_json_whatever_the_account_is_called.
func TestManifestIsValidJSONWhateverTheAccountIsCalled(t *testing.T) {
	name := `Back\slash "quoted" <b>&amp;</b>`
	body := PwaManifestJson(&name, "/account/logo?size=small&v=1", "/account/logo?v=1", "http://campfire.test", func(path string) string { return "/assets/" + path })
	var manifest struct {
		Name  string
		Icons []struct{ Src string }
	}
	if err := json.Unmarshal([]byte(body), &manifest); err != nil {
		t.Fatal(err, body)
	}
	if manifest.Name != name || manifest.Icons[0].Src != "/account/logo?size=small&v=1" {
		t.Errorf("got %+v", manifest)
	}
}
