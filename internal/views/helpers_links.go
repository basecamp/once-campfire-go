package views

import "strings"

// link_to, link_to_if and mail_to (UrlHelper).

// LinkTo is link_to(url, options) { content }.
func LinkTo(url string, options *Attrs, content HTML) HTML {
	return ContentTag("a", linkOptions(url, options), content)
}

// linkOptions is link_to's attributes: href goes after the given options.
func linkOptions(url string, options *Attrs) *Attrs {
	return options.put("href", textAttr(url))
}

// LinkToText is link_to(text, url, options) with a plain-text name.
func LinkToText(text, url string, options *Attrs) HTML {
	return LinkTo(url, options, Text(text))
}

// LinkToIf is link_to_if(condition, name, url, options): just the escaped name when false.
func LinkToIf(condition bool, text, url string, options *Attrs) HTML {
	if condition {
		return LinkToText(text, url, options)
	}
	return Text(text)
}

// MailTo is mail_to(email): the address percent-escaped (ERB::Util.url_encode, keeping "@") in
// the href, and the plain address as the link text.
func MailTo(email string) HTML {
	encoded := strings.ReplaceAll(urlEncode(email), "%40", "@")
	return ContentTag("a", NewAttrs().Attr("href", "mailto:"+encoded), Text(email))
}
