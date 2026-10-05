package views

import "strings"

// TranslationsHelper: the language popups beside form fields.

// TranslationsFor is translations_for(key).
func TranslationsFor(key string) HTML {
	for _, translation := range translations {
		if translation.key != key {
			continue
		}
		var items strings.Builder
		for _, entry := range translation.entries {
			items.WriteString(string(ContentTagText("dt", nil, entry[0])))
			items.WriteString(string(ContentTagText("dd", NewAttrs().Class("margin-none"), entry[1])))
		}
		return ContentTag("dl", NewAttrs().Class("language-list"), HTML(items.String()))
	}
	panic("unknown translation key " + key)
}

// TranslationButton is translation_button(key).
func TranslationButton(ctx *ViewContext, key string) HTML {
	summary := ContentTag("summary", NewAttrs().Class("btn").Tabindex(-1),
		ImageTag(ctx, "globe.svg", NewAttrs().Size(20).AriaHidden().Class("color-icon"))+
			ContentTagText("span", NewAttrs().Class("for-screen-reader"), "Translate"))
	menu := ContentTag("div", NewAttrs().Class("language-list-menu shadow").Data("popup_target", "menu"), TranslationsFor(key))
	details := NewAttrs().
		Class("position-relative").
		Data("controller", "popup").
		Data("action", "keydown.esc->popup#close toggle->popup#toggle click@document->popup#closeOnClickOutside").
		Data("popup_orientation_top_class", "popup-orientation-top")
	return ContentTag("details", details, summary+menu)
}
