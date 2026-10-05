package views

import "testing"

// translation_button as the reference renders it (tests/golden/a/first_run.html).
func TestTranslationButtonLikeRails(t *testing.T) {
	want := `<details class="position-relative" data-controller="popup" data-action="keydown.esc-&gt;popup#close toggle-&gt;popup#toggle click@document-&gt;popup#closeOnClickOutside" data-popup-orientation-top-class="popup-orientation-top"><summary class="btn" tabindex="-1"><img aria-hidden="true" class="color-icon" src="/assets/globe-8c54d23b.svg" width="20" height="20" /><span class="for-screen-reader">Translate</span></summary><div class="language-list-menu shadow" data-popup-target="menu"><dl class="language-list"><dt>🇺🇸</dt><dd class="margin-none">Enter your name</dd><dt>🇪🇸</dt><dd class="margin-none">Introduce tu nombre</dd><dt>🇫🇷</dt><dd class="margin-none">Entrez votre nom</dd><dt>🇮🇳</dt><dd class="margin-none">अपना नाम दर्ज करें</dd><dt>🇩🇪</dt><dd class="margin-none">Geben Sie Ihren Namen ein</dd><dt>🇧🇷</dt><dd class="margin-none">Insira seu nome</dd><dt>🇯🇵</dt><dd class="margin-none">お名前を入力してください</dd></dl></div></details>`
	if got := TranslationButton(testContext(), "user_name"); got != HTML(want) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
