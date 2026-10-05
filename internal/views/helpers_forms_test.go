package views

import "testing"

func TestSanitizesNestedObjectNames(t *testing.T) {
	if got := sanitizeObjectName("account[settings]"); got != "account_settings" {
		t.Errorf("got %s", got)
	}
	if got := sanitizeObjectName("user"); got != "user" {
		t.Errorf("got %s", got)
	}
}

// The fields of first_runs/show, accounts/users/_user, accounts/edit and users/profiles as the
// reference renders them (tests/golden).
func TestFormFieldsLikeRails(t *testing.T) {
	form := FormWith(RouteFirstRun()).Model("user").Class("center max-width")
	fields := form.FileField("avatar", NewAttrs().Class("input").Accept("image/*").Data("upload_preview_target", "input").Data("action", "upload-preview#previewImage")) +
		form.TextField("name", nil, NewAttrs().Class("input").Autocomplete("name").Placeholder("Name").Autofocus().Required(true).Data("1p-ignore", true)) +
		form.EmailField("email_address", nil, NewAttrs().Class("input").Autocomplete("username").Placeholder("Email address").Required(true)) +
		form.PasswordField("password", NewAttrs().Class("input").Autocomplete("new-password").Placeholder("Password").Required(true).Maxlength(72)) +
		Button("x", NewAttrs().Class("btn btn--reversed center txt-large").Type("submit"))
	want := `<form class="center max-width" enctype="multipart/form-data" action="/first_run" accept-charset="UTF-8" method="post">` +
		`<input class="input" accept="image/*" data-upload-preview-target="input" data-action="upload-preview#previewImage" type="file" name="user[avatar]" id="user_avatar" />` +
		`<input class="input" autocomplete="name" placeholder="Name" autofocus="autofocus" required="required" data-1p-ignore="true" type="text" name="user[name]" id="user_name" />` +
		`<input class="input" autocomplete="username" placeholder="Email address" required="required" type="email" name="user[email_address]" id="user_email_address" />` +
		`<input class="input" autocomplete="new-password" placeholder="Password" required="required" maxlength="72" size="72" type="password" name="user[password]" id="user_password" />` +
		`<button name="button" type="submit" class="btn btn--reversed center txt-large">x</button></form>`
	if got := FormWithBlock(fields, form); got != HTML(want) {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	user := FormWith(RouteAccountUser(1)).Model("user").Data("controller", "form").Method("patch")
	checkBox := user.CheckBox("role", NewAttrs().Data("action", "form#submit").Hidden().ID(DomID("user", 127326141, "role")).Disabled(true), "administrator", "member", "administrator")
	if want := `<input name="user[role]" disabled="disabled" type="hidden" value="member" /><input data-action="form#submit" hidden="hidden" id="role_user_127326141" disabled="disabled" type="checkbox" value="administrator" checked="checked" name="user[role]" />`; checkBox != HTML(want) {
		t.Errorf("got %s", checkBox)
	}
	if got := user.Open(); got != `<form data-controller="form" action="/account/users/1" accept-charset="UTF-8" method="post"><input type="hidden" name="_method" value="patch" />` {
		t.Errorf("got %s", got)
	}

	settings := FormWith("/account").Model("account").Method("patch").FieldsFor("settings")
	if got := settings.HiddenField("restrict_room_creation_to_administrators", nil, NewAttrs().Value("true")); got != `<input value="true" type="hidden" name="account[settings][restrict_room_creation_to_administrators]" id="account_settings_restrict_room_creation_to_administrators" />` {
		t.Errorf("got %s", got)
	}

	profile := FormWith(RouteUserProfile()).Model("user").Method("patch").Data("controller", "form")
	if got := profile.TextArea("bio", nil, NewAttrs().Class("input txt-large").Placeholder("A few words about yourself…").Maxlength(200).Rows(3)); got != "<textarea class=\"input txt-large\" placeholder=\"A few words about yourself…\" maxlength=\"200\" rows=\"3\" name=\"user[bio]\" id=\"user_bio\">\n</textarea>" {
		t.Errorf("got %s", got)
	}
	bio := "<b>"
	if got := profile.TextArea("bio", &bio, nil); got != "<textarea name=\"user[bio]\" id=\"user_bio\">\n&lt;b&gt;</textarea>" {
		t.Errorf("got %s", got)
	}

	if got := HiddenFieldTag("push_subscription_endpoint", nil, NewAttrs().Data("sessions_target", "pushSubscriptionEndpoint")); got != `<input type="hidden" name="push_subscription_endpoint" id="push_subscription_endpoint" data-sessions-target="pushSubscriptionEndpoint" />` {
		t.Errorf("got %s", got)
	}

	transfer := FormWith("/session/transfers/x").Method("put").AutoSubmit().Open()
	if transfer != `<form data-controller="auto-submit" action="/session/transfers/x" accept-charset="UTF-8" method="post"><input type="hidden" name="_method" value="put" />` {
		t.Errorf("got %s", transfer)
	}
}

func TestButtonToLikeRails(t *testing.T) {
	got := ButtonToBlock("x", RouteAccountBotKey(394959859), NewAttrs().Method("put").Class("btn full-width txt--small btn--negative").Aria("label", "Generate a new key").Data("turbo_confirm", "Are you sure you want to change the bot key? All usage of this bot must be updated."))
	want := `<form class="button_to" method="post" action="/account/bots/394959859/key"><input type="hidden" name="_method" value="put" /><button class="btn full-width txt--small btn--negative" aria-label="Generate a new key" data-turbo-confirm="Are you sure you want to change the bot key? All usage of this bot must be updated." type="submit">x</button></form>`
	if got != HTML(want) {
		t.Errorf("got %s", got)
	}
	if got := ButtonTo("/account/join_code", NewAttrs().Class("btn btn--regenerate"), "x"); got != `<form class="button_to" method="post" action="/account/join_code"><button class="btn btn--regenerate" type="submit">x</button></form>` {
		t.Errorf("got %s", got)
	}
	if got := ButtonTo("/s", NewAttrs().Method("get").Attr("form_class", "f"), "x"); got != `<form class="f" method="get" action="/s"><button type="submit">x</button></form>` {
		t.Errorf("got %s", got)
	}
}
